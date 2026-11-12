package controller

import (
	"context"
	"fmt"
	"slices"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
	"github.com/cybozu-go/ofen/internal/imgmanager"
)

type Runner struct {
	k8sClient   client.Client
	imagePuller *imgmanager.ImagePuller
	logger      logr.Logger
	queue       workqueue.TypedRateLimitingInterface[imgmanager.Task]
	recorder    events.EventRecorder
	notifyCh    chan<- event.TypedGenericEvent[*ofenv1.NodeImageSet]
}

func NewRunner(
	k8sClient client.Client,
	imagePuller *imgmanager.ImagePuller,
	logger logr.Logger,
	queue workqueue.TypedRateLimitingInterface[imgmanager.Task],
	recorder events.EventRecorder,
	notifyCh chan<- event.TypedGenericEvent[*ofenv1.NodeImageSet],
) *Runner {
	return &Runner{
		k8sClient:   k8sClient,
		imagePuller: imagePuller,
		logger:      logger,
		queue:       queue,
		recorder:    recorder,
		notifyCh:    notifyCh,
	}
}

func (r *Runner) Start(ctx context.Context) error {
	r.logger.Info("starting runner")
	defer r.logger.Info("runner stopped")

	r.runWorker(ctx)
	return nil
}

func (r *Runner) runWorker(ctx context.Context) {
	for {
		item, shutdown := r.queue.Get()
		if shutdown {
			return
		}

		func() {
			defer r.queue.Done(item)
			select {
			case <-ctx.Done():
				r.logger.Info("context cancelled, skipping task processing")
				return
			default:
			}

			err := r.processTask(ctx, item)
			if err != nil {
				r.logger.Error(err, "failed to process task", "task", item)
				r.queue.AddRateLimited(item)
				return
			}
			r.queue.Forget(item)
			// Notify only on success. A failed task is still waiting for download if it failed before
			// pulling the image, so the reconciler would add it again and skip the backoff.
			r.notify(ctx, item.NodeImageSetName)
		}()
	}
}

func (r *Runner) notify(ctx context.Context, nodeImageSetName string) {
	nodeImageSet := &ofenv1.NodeImageSet{}
	nodeImageSet.Name = nodeImageSetName
	select {
	case r.notifyCh <- event.TypedGenericEvent[*ofenv1.NodeImageSet]{Object: nodeImageSet}:
	case <-ctx.Done():
	}
}

func (r *Runner) processTask(ctx context.Context, task imgmanager.Task) error {
	r.logger.Info("processing image", "task", task)

	if exists := r.imagePuller.IsImageExists(ctx, task.Ref); exists {
		r.logger.Info("image already exists", "image", task.Ref)
		return nil
	}

	var nodeImageSet ofenv1.NodeImageSet
	if err := r.k8sClient.Get(ctx, client.ObjectKey{Name: task.NodeImageSetName}, &nodeImageSet); err != nil {
		if apierrors.IsNotFound(err) {
			r.logger.Info("NodeImageSet not found, skipping task", "task", task)
			return nil
		}
		return fmt.Errorf("failed to get NodeImageSet %s: %w", task.NodeImageSetName, err)
	}
	if nodeImageSet.DeletionTimestamp != nil || !slices.Contains(nodeImageSet.Spec.Images, task.Ref) {
		r.logger.Info("image is no longer required, skipping task", "task", task)
		return nil
	}

	secrets, err := getImagePullSecrets(ctx, r.k8sClient, &nodeImageSet)
	if err != nil {
		return err
	}

	r.recorder.Eventf(&nodeImageSet, nil, corev1.EventTypeNormal, "ImageDownloading", "ImageDownloading", "downloading image %s on %s", task.Ref, nodeImageSet.Spec.NodeName)
	err = r.imagePuller.PullImage(ctx, nodeImageSet.Name, task.Ref, nodeImageSet.Spec.RegistryPolicy, &secrets)
	if err != nil {
		r.recorder.Eventf(&nodeImageSet, nil, corev1.EventTypeWarning, "ImageDownloadFailed", "ImageDownloadFailed", "failed to download image %s: %v on %s", task.Ref, err, nodeImageSet.Spec.NodeName)
		return err
	}

	r.recorder.Eventf(&nodeImageSet, nil, corev1.EventTypeNormal, "ImageDownloaded", "ImageDownloaded", "successfully downloaded image %s on %s", task.Ref, nodeImageSet.Spec.NodeName)
	r.logger.Info("successfully processed image", "image", task.Ref)
	return nil
}

func getImagePullSecrets(ctx context.Context, c client.Reader, nodeImageSet *ofenv1.NodeImageSet) ([]corev1.Secret, error) {
	imagePrefetchNamespace := nodeImageSet.Labels[constants.OwnerImagePrefetchNamespace]
	secrets := make([]corev1.Secret, 0, len(nodeImageSet.Spec.ImagePullSecrets))
	for _, secretRef := range nodeImageSet.Spec.ImagePullSecrets {
		var secret corev1.Secret
		err := c.Get(ctx, client.ObjectKey{Namespace: imagePrefetchNamespace, Name: secretRef.Name}, &secret)
		if apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("failed to get image pull secret %s: %w", secretRef.Name, err)
		}
		secrets = append(secrets, secret)
	}
	return secrets, nil
}

func (r *Runner) NeedLeaderElection() bool {
	return true
}

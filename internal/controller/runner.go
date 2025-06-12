package controller

import (
	"context"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/imgmanager"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

type Runner struct {
	client           client.Client
	containerdClient imgmanager.ContainerdClient
	ImagePuller      *imgmanager.ImagePuller
	logger           logr.Logger
	channel          chan<- event.TypedGenericEvent[*ofenv1.NodeImageSet]
	queue            workqueue.TypedRateLimitingInterface[Task]
	recorder         record.EventRecorder
}

type Task struct {
	Ref              string
	RegistryPolicy   ofenv1.RegistryPolicy
	NodeImageSetName string
	Secrets          *[]corev1.Secret
}

func NewRunner(k8sClient client.Client, containerdClient imgmanager.ContainerdClient, imagePuller *imgmanager.ImagePuller, logger logr.Logger, channel chan<- event.TypedGenericEvent[*ofenv1.NodeImageSet], queue workqueue.TypedRateLimitingInterface[Task], recorder record.EventRecorder) *Runner {
	return &Runner{
		client:           k8sClient,
		containerdClient: containerdClient,
		ImagePuller:      imagePuller,
		logger:           logger,
		channel:          channel,
		queue:            queue,
		recorder:         recorder,
	}
}

func (r *Runner) Start(ctx context.Context) error {
	r.logger.Info("starting runner")
	defer r.logger.Info("runner stopped")
	defer r.queue.ShutDown()

	r.runWorker(ctx)
	return nil
}

func (r *Runner) runWorker(ctx context.Context) {
	go func() {
		<-ctx.Done()
		r.logger.Info("context cancelled, shutting down queue")
		r.queue.ShutDown()
	}()

	for {
		item, shutdown := r.queue.Get()
		if shutdown {
			return
		}

		func() {
			defer r.queue.Done(item)

			select {
			case <-ctx.Done():
				r.logger.Info("context done, skipping task processing")
				return
			default:
			}

			err := r.processTask(ctx, item)
			if err != nil {
				r.logger.Error(err, "failed to process task", "task", item)
				r.queue.AddRateLimited(item)
			} else {
				r.queue.Forget(item)
			}
		}()
	}
}

func (r *Runner) processTask(ctx context.Context, task Task) error {
	r.logger.Info("processing image", "task", task)

	var nodeImageSet ofenv1.NodeImageSet
	err := r.client.Get(ctx, client.ObjectKey{Name: task.NodeImageSetName}, &nodeImageSet)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.logger.Info("skipping task for deleted NodeImageSet", "name", task.NodeImageSetName)
			return nil
		}
		return err
	}

	if nodeImageSet.DeletionTimestamp != nil {
		r.logger.Info("skipping task for NodeImageSet that is being deleted", "name", task.NodeImageSetName)
		return nil
	}

	if exists := r.ImagePuller.IsImageExists(ctx, task.Ref); exists {
		r.logger.Info("image already exists", "image", task.Ref)
		return nil
	}

	r.recorder.Eventf(&nodeImageSet, corev1.EventTypeNormal, "ImageDownloading", "downloading image %s on %s", task.Ref, nodeImageSet.Spec.NodeName)
	err = r.ImagePuller.PullImage(ctx, nodeImageSet.Name, task.Ref, task.RegistryPolicy, task.Secrets)
	if err != nil {
		r.recorder.Eventf(&nodeImageSet, corev1.EventTypeWarning, "ImageDownloadFailed", "failed to download image %s: %v on %s", task.Ref, err, nodeImageSet.Spec.NodeName)
		return err
	}

	r.recorder.Eventf(&nodeImageSet, corev1.EventTypeNormal, "ImageDownloaded", "successfully downloaded image %s on %s", task.Ref, nodeImageSet.Spec.NodeName)
	r.logger.Info("successfully processed image", "image", task.Ref)
	return nil
}

func (r *Runner) NeedLeaderElection() bool {
	return true
}

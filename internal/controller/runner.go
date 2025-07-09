package controller

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	"github.com/cybozu-go/ofen/internal/imgmanager"
)

type Runner struct {
	ImagePuller *imgmanager.ImagePuller
	logger      logr.Logger
	queue       workqueue.TypedRateLimitingInterface[imgmanager.Task]
	recorder    record.EventRecorder
}

func NewRunner(imagePuller *imgmanager.ImagePuller, logger logr.Logger, queue workqueue.TypedRateLimitingInterface[imgmanager.Task], recorder record.EventRecorder) *Runner {
	return &Runner{
		ImagePuller: imagePuller,
		logger:      logger,
		queue:       queue,
		recorder:    recorder,
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
			} else {
				r.queue.Forget(item)
			}
		}()
	}
}

func (r *Runner) processTask(ctx context.Context, task imgmanager.Task) error {
	r.logger.Info("processing image", "task", task)

	if exists := r.ImagePuller.IsImageExists(ctx, task.Ref); exists {
		r.logger.Info("image already exists", "image", task.Ref)
		return nil
	}

	r.recorder.Eventf(task.NodeImageSet, corev1.EventTypeNormal, "ImageDownloading", "downloading image %s on %s", task.Ref, task.NodeImageSet.Spec.NodeName)
	err := r.ImagePuller.PullImage(ctx, task.NodeImageSet.Name, task.Ref, task.RegistryPolicy, task.Secrets)
	if err != nil {
		r.recorder.Eventf(task.NodeImageSet, corev1.EventTypeWarning, "ImageDownloadFailed", "failed to download image %s: %v on %s", task.Ref, err, task.NodeImageSet.Spec.NodeName)
		return err
	}

	r.recorder.Eventf(task.NodeImageSet, corev1.EventTypeNormal, "ImageDownloaded", "successfully downloaded image %s on %s", task.Ref, task.NodeImageSet.Spec.NodeName)
	r.logger.Info("successfully processed image", "image", task.Ref)
	return nil
}

func (r *Runner) NeedLeaderElection() bool {
	return true
}

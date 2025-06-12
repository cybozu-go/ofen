package controller

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/typeurl/v2"
	"github.com/go-logr/logr"

	eventtypes "github.com/containerd/containerd/api/events"
)

type ContainerdEventWatcher struct {
	containerdClient ContainerdClient
	logger           logr.Logger
	eventNotifyCh    chan<- string
	monitoredImages  []string
	cancel           context.CancelFunc
}

func NewContainerdEventWatcher(
	containerdClient ContainerdClient,
	logger logr.Logger,
	eventNotifyCh chan<- string,
	monitoredImages []string,
) *ContainerdEventWatcher {
	return &ContainerdEventWatcher{
		containerdClient: containerdClient,
		logger:           logger,
		eventNotifyCh:    eventNotifyCh,
		monitoredImages:  monitoredImages,
	}
}

func (w *ContainerdEventWatcher) Start(ctx context.Context) {
	w.logger.Info("starting containerd event watcher", "monitoredImages", w.monitoredImages)
	ctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	eventsCh, errCh := w.containerdClient.Subscribe(ctx, w.monitoredImages)

	for {
		var e *events.Envelope
		select {
		case <-ctx.Done():
			w.logger.Info("containerd event watcher stopped")
			return
		case err := <-errCh:
			w.logger.Error(err, "error receiving events from containerd")
			continue
		case e = <-eventsCh:
			if e == nil {
				continue
			}
			deleteImageName, err := w.handleEvent(e)
			if err != nil {
				w.logger.Error(err, "failed to handle event", "event", e)
				continue
			}
			if deleteImageName != "" {
				w.logger.Info("image deletion event received", "image", deleteImageName)
				w.eventNotifyCh <- deleteImageName
			}
		}
	}
}

func (w *ContainerdEventWatcher) Stop() {
	w.logger.Info("stopping containerd event watcher")
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
}

func (w *ContainerdEventWatcher) handleEvent(e *events.Envelope) (string, error) {
	v, err := typeurl.UnmarshalAny(e.Event)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal event: %w", err)
	}

	switch event := v.(type) {
	case *eventtypes.ImageDelete:
		return event.GetName(), nil
	default:
		w.logger.Info("received unsupported event type", "eventType", fmt.Sprintf("%T", event))
	}

	return "", nil
}

package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
	"github.com/cybozu-go/ofen/internal/imgmanager"
)

type ContainerdEventWatcher struct {
	k8sClient        client.Client
	containerdClient imgmanager.ContainerdClient
	imagePuller      *imgmanager.ImagePuller
	logger           logr.Logger
	nodeName         string
	eventNotifyCh    chan<- event.TypedGenericEvent[*ofenv1.NodeImageSet]
}

func NewContainerdEventWatcher(
	k8sClient client.Client,
	containerdClient imgmanager.ContainerdClient,
	imagePuller *imgmanager.ImagePuller,
	logger logr.Logger,
	nodeName string,
	eventNotifyCh chan<- event.TypedGenericEvent[*ofenv1.NodeImageSet],
) *ContainerdEventWatcher {
	return &ContainerdEventWatcher{
		k8sClient:        k8sClient,
		containerdClient: containerdClient,
		imagePuller:      imagePuller,
		logger:           logger,
		nodeName:         nodeName,
		eventNotifyCh:    eventNotifyCh,
	}
}

func (w *ContainerdEventWatcher) Start(ctx context.Context) error {
	w.logger.Info("starting containerd event watcher")

	eventsCh, err := w.imagePuller.SubscribeDeleteEvent(ctx)
	if err != nil {
		return fmt.Errorf("failed to subscribe to containerd events: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("containerd event watcher stopped")
			return nil
		case deleteImageName := <-eventsCh:
			if deleteImageName != "" {
				w.logger.Info("received image deletion event", "imageName", deleteImageName)
				w.notifyController(ctx, deleteImageName)
			}
		}
	}
}

func (w *ContainerdEventWatcher) notifyController(ctx context.Context, imageName string) {
	var nodeImageSetList ofenv1.NodeImageSetList
	err := w.k8sClient.List(ctx, &nodeImageSetList, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			constants.NodeName: w.nodeName,
		}),
	})
	if err != nil {
		w.logger.Error(err, "failed to list NodeImageSet for node", "nodeName", w.nodeName)
		return
	}

	for _, nis := range nodeImageSetList.Items {
		for _, image := range nis.Spec.Images {
			if image == imageName {
				w.logger.Info("notifying controller of image removal from containerd", "imageName", imageName, "nodeImageSetName", nis.Name)
				select {
				case w.eventNotifyCh <- event.TypedGenericEvent[*ofenv1.NodeImageSet]{
					Object: nis.DeepCopy(),
				}:
				case <-ctx.Done():
					w.logger.Info("context cancelled while notifying controller", "imageName", imageName)
					return
				}
			}
		}
	}
}

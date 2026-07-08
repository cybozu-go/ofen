package controller

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
	"github.com/cybozu-go/ofen/internal/imgmanager"
)

// ImageUnpinner periodically reconciles the pin state of prefetched images on the node.
//
// ofen pins an image when it pulls it so that kubelet's image garbage collection does
// not delete it before a Pod uses it. Once an image is in use by a container, kubelet's
// own in-use protection takes over, so ofen unpins it and lets kubelet manage its
// lifecycle again. Images that are no longer desired by any NodeImageSet are also
// unpinned so they do not stay pinned forever and exhaust node storage.
type ImageUnpinner struct {
	k8sClient        client.Client
	containerdClient imgmanager.ContainerdClient
	logger           logr.Logger
	nodeName         string
	interval         time.Duration
}

func NewImageUnpinner(
	k8sClient client.Client,
	containerdClient imgmanager.ContainerdClient,
	logger logr.Logger,
	nodeName string,
	interval time.Duration,
) *ImageUnpinner {
	return &ImageUnpinner{
		k8sClient:        k8sClient,
		containerdClient: containerdClient,
		logger:           logger,
		nodeName:         nodeName,
		interval:         interval,
	}
}

func (u *ImageUnpinner) Start(ctx context.Context) error {
	u.logger.Info("starting image unpinner", "interval", u.interval)
	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			u.logger.Info("image unpinner stopped")
			return nil
		case <-ticker.C:
			if err := u.reconcilePins(ctx); err != nil {
				u.logger.Error(err, "failed to reconcile image pins")
			}
		}
	}
}

func (u *ImageUnpinner) reconcilePins(ctx context.Context) error {
	var nodeImageSetList ofenv1.NodeImageSetList
	if err := u.k8sClient.List(ctx, &nodeImageSetList, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			constants.NodeName: u.nodeName,
		}),
	}); err != nil {
		return err
	}

	desired := make(map[string]struct{})
	for _, nis := range nodeImageSetList.Items {
		if nis.DeletionTimestamp != nil {
			continue
		}
		for _, image := range nis.Spec.Images {
			desired[image] = struct{}{}
		}
	}

	// Only pinned images ever need action. Working from the pinned set keeps the
	// reconcile idempotent: once an image is unpinned it no longer appears here, so
	// we do not repeatedly log or attempt to unpin an image that is still in use.
	pinned, err := u.containerdClient.ListPinnedImages(ctx)
	if err != nil {
		return err
	}

	inUse, err := u.containerdClient.InUseImages(ctx, pinned)
	if err != nil {
		return err
	}
	inUseSet := make(map[string]struct{}, len(inUse))
	for _, ref := range inUse {
		inUseSet[ref] = struct{}{}
	}

	for _, ref := range pinned {
		if _, ok := inUseSet[ref]; ok {
			// The image now backs a container; kubelet protects in-use images from
			// garbage collection, so ofen's pin is no longer needed.
			u.logger.Info("unpinning in-use image", "image", ref)
			if err := u.containerdClient.UnpinImage(ctx, ref); err != nil {
				u.logger.Error(err, "failed to unpin in-use image", "image", ref)
			}
			continue
		}
		if _, ok := desired[ref]; !ok {
			// ofen pinned this image but no NodeImageSet desires it anymore (e.g. after
			// the owning ImagePrefetch is deleted), so unpin it rather than leave it
			// pinned forever.
			u.logger.Info("unpinning image no longer desired by any NodeImageSet", "image", ref)
			if err := u.containerdClient.UnpinImage(ctx, ref); err != nil {
				u.logger.Error(err, "failed to unpin undesired image", "image", ref)
			}
		}
	}

	return nil
}

func (u *ImageUnpinner) NeedLeaderElection() bool {
	return false
}

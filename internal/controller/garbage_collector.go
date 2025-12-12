package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
)

type nodeImageSetGarbageCollector struct {
	client   client.Client
	logger   logr.Logger
	interval time.Duration
}

func NewNodeImageSetGarbageCollector(client client.Client, logger logr.Logger, interval time.Duration) *nodeImageSetGarbageCollector {
	return &nodeImageSetGarbageCollector{
		client:   client,
		logger:   logger,
		interval: interval,
	}
}

func (gc *nodeImageSetGarbageCollector) NeedLeaderElection() bool {
	return true
}

func (gc *nodeImageSetGarbageCollector) Start(ctx context.Context) error {
	ticker := time.NewTicker(gc.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			gc.logger.Info("node image set garbage collector stopped")
			return ctx.Err()
		case <-ticker.C:
			gc.removeStaleNodeImageSets(ctx)
		}
	}
}

func (gc *nodeImageSetGarbageCollector) removeStaleNodeImageSets(ctx context.Context) error {
	gc.logger.Info("starting garbage collection of stale NodeImageSets")

	nodeImageSets := &ofenv1.NodeImageSetList{}
	if err := gc.client.List(ctx, nodeImageSets); err != nil {
		return fmt.Errorf("failed to list NodeImageSets: %w", err)
	}

	nodes := &corev1.NodeList{}
	if err := gc.client.List(ctx, nodes); err != nil {
		return fmt.Errorf("failed to list Nodes: %w", err)
	}

	nodeNames := make(map[string]bool)
	for _, node := range nodes.Items {
		nodeNames[node.Name] = true
	}

	for _, nis := range nodeImageSets.Items {
		nodeName := nis.Spec.NodeName
		if nodeNames[nodeName] {
			continue
		}

		err := gc.deleteNodeImageSet(ctx, nis.Name)
		if err != nil {
			return fmt.Errorf("failed to delete stale NodeImageSet %s: %w", nis.Name, err)
		}
		gc.logger.Info("deleted stale NodeImageSet", "name", nis.Name)
	}

	gc.logger.Info("completed garbage collection of stale NodeImageSets")
	return nil
}

func (gc *nodeImageSetGarbageCollector) deleteNodeImageSet(ctx context.Context, name string) error {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		nis := &ofenv1.NodeImageSet{}
		err := gc.client.Get(ctx, client.ObjectKey{Name: name}, nis)
		if err != nil {
			return client.IgnoreNotFound(err)
		}
		if !controllerutil.ContainsFinalizer(nis, constants.NodeImageSetFinalizer) {
			return nil
		}

		controllerutil.RemoveFinalizer(nis, constants.NodeImageSetFinalizer)
		return gc.client.Update(ctx, nis)
	})
	if err != nil {
		return fmt.Errorf("failed to remove finalizer from NodeImageSet %s: %w", name, err)
	}

	nis := &ofenv1.NodeImageSet{}
	nis.Name = name
	return client.IgnoreNotFound(gc.client.Delete(ctx, nis))
}

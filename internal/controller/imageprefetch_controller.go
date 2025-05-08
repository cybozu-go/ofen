package controller

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	ofenv1apply "github.com/cybozu-go/ofen/internal/applyconfigurations/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
	"github.com/cybozu-go/ofen/internal/util"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1apply "k8s.io/client-go/applyconfigurations/meta/v1"
)

// ImagePrefetchReconciler reconciles a ImagePrefetch object
type ImagePrefetchReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	ImagePullNodeLimit int
}

// +kubebuilder:rbac:groups=ofen.cybozu.io,resources=imageprefetches,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ofen.cybozu.io,resources=imageprefetches/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ofen.cybozu.io,resources=imageprefetches/finalizers,verbs=update
// +kubebuilder:rbac:groups=ofen.cybozu.io,resources=nodeimagesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch

func (r *ImagePrefetchReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var imgPrefetch ofenv1.ImagePrefetch
	if err := r.Get(ctx, req.NamespacedName, &imgPrefetch); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if imgPrefetch.DeletionTimestamp != nil {
		logger.Info("starting finalization")
		if err := r.finalize(ctx, &imgPrefetch); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to finalize: %w", err)
		}
		logger.Info("finished finalization")

		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&imgPrefetch, constants.ImagePrefetchFinalizer) {
		controllerutil.AddFinalizer(&imgPrefetch, constants.ImagePrefetchFinalizer)
		err := r.Update(ctx, &imgPrefetch)
		if err != nil {
			logger.Error(err, "failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	selectNodes, err := r.selectTargetNodes(ctx, &imgPrefetch)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to select target nodes: %w", err)
	}

	err = r.createOrUpdateNodeImageSet(ctx, &imgPrefetch, selectNodes)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create or update NodeImageSet: %w", err)
	}

	return r.updateStatus(ctx, &imgPrefetch, selectNodes)
}

func (r *ImagePrefetchReconciler) finalize(ctx context.Context, imgPrefetch *ofenv1.ImagePrefetch) error {
	logger := log.FromContext(ctx)
	if !controllerutil.ContainsFinalizer(imgPrefetch, constants.ImagePrefetchFinalizer) {
		return nil
	}

	logger.Info("deleting NodeImageSets")
	opts := []client.DeleteAllOfOption{
		client.MatchingLabels{
			constants.OwnerImagePrefetchNamespace: imgPrefetch.Namespace,
			constants.OwnerImagePrefetchName:      imgPrefetch.Name,
		},
	}
	err := r.DeleteAllOf(ctx, &ofenv1.NodeImageSet{}, opts...)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete NodeImageSets: %w", err)
		}
	}

	controllerutil.RemoveFinalizer(imgPrefetch, constants.ImagePrefetchFinalizer)
	return r.Update(ctx, imgPrefetch)
}

func (r *ImagePrefetchReconciler) selectTargetNodes(ctx context.Context, imgPrefetch *ofenv1.ImagePrefetch) ([]string, error) {
	logger := log.FromContext(ctx)

	opts := []client.ListOption{}
	if !util.IsLabelSelectorEmpty(&imgPrefetch.Spec.NodeSelector) {
		selector, err := metav1.LabelSelectorAsSelector(&imgPrefetch.Spec.NodeSelector)
		if err != nil {
			return nil, fmt.Errorf("failed to parse selector: %w", err)
		}
		opts = append(opts, &client.MatchingLabelsSelector{Selector: selector})
	}

	allNodes := &corev1.NodeList{}
	if err := r.List(ctx, allNodes, opts...); err != nil {
		return nil, err
	}
	readyNodes := filterReadyNodes(allNodes.Items)

	if imgPrefetch.Spec.AllNodes {
		return getNodeNames(readyNodes), nil
	}

	if imgPrefetch.Spec.Replicas > 0 {
		needsNodeSelection := isNeedNodeSelection(ctx, imgPrefetch, readyNodes)
		if needsNodeSelection {
			nodes, err := selectNodesByReplicas(ctx, imgPrefetch, readyNodes)
			if err != nil {
				return nil, fmt.Errorf("failed to select nodes by replicas: %w", err)
			}
			logger.Info("selected nodes by replicas", "nodes", nodes)

			return nodes, nil
		}

		return imgPrefetch.Status.SelectedNodes, nil
	}

	return nil, fmt.Errorf("failed to select target nodes")
}

func filterReadyNodes(nodes []corev1.Node) []corev1.Node {
	var readyNodes []corev1.Node
	for _, node := range nodes {
		if util.IsNodeReady(&node) {
			readyNodes = append(readyNodes, node)
		}
	}
	return readyNodes
}

func isNeedNodeSelection(ctx context.Context, imgPrefetch *ofenv1.ImagePrefetch, readyNodes []corev1.Node) bool {
	if len(imgPrefetch.Status.SelectedNodes) == 0 {
		return true
	}

	if imgPrefetch.Generation != imgPrefetch.Status.ObservedGeneration {
		return true
	}

	readyNodesName := getNodeNames(readyNodes)
	containUnhealthyNodes := false
	for _, node := range imgPrefetch.Status.SelectedNodes {
		if !slices.Contains(readyNodesName, node) {
			containUnhealthyNodes = true
			break
		}
	}

	return containUnhealthyNodes
}

func getNodeNames(nodes []corev1.Node) []string {
	nodeNames := []string{}
	for _, node := range nodes {
		nodeNames = append(nodeNames, node.Name)
	}

	return nodeNames
}

func selectNodesByReplicas(ctx context.Context, imgPrefetch *ofenv1.ImagePrefetch, readyNodes []corev1.Node) ([]string, error) {
	var selectNodes []string
	targetReplicas := imgPrefetch.Spec.Replicas

	if len(readyNodes) < targetReplicas {
		return nil, fmt.Errorf("not enough nodes available: %d < %d", len(readyNodes), targetReplicas)
	}

	readyNodesName := getNodeNames(readyNodes)
	for _, node := range imgPrefetch.Status.SelectedNodes {
		if len(selectNodes) >= targetReplicas {
			break
		}

		if slices.Contains(readyNodesName, node) {
			selectNodes = append(selectNodes, node)
		}
	}

	if len(selectNodes) < targetReplicas {
		sort.Slice(readyNodes, func(i, j int) bool {
			return len(readyNodes[i].Status.Images) < len(readyNodes[j].Status.Images)
		})

		for _, node := range readyNodes {
			if len(selectNodes) >= targetReplicas {
				break
			}

			if !slices.Contains(selectNodes, node.Name) {
				selectNodes = append(selectNodes, node.Name)
			}
		}
	}

	return selectNodes, nil
}

func (r *ImagePrefetchReconciler) createOrUpdateNodeImageSet(ctx context.Context, imgPrefetch *ofenv1.ImagePrefetch, selectedNodes []string) error {
	logger := log.FromContext(ctx)

	selectNodes := map[string]struct{}{}
	for i, nodeName := range selectedNodes {
		selectNodes[nodeName] = struct{}{}
		nodeImageSetName := getNodeImageSetName(imgPrefetch, nodeName)

		registryPolicy := ofenv1.RegistryPolicyMirrorOnly
		if i < r.ImagePullNodeLimit {
			registryPolicy = ofenv1.RegistryPolicyDefault
		}
		nodeImageSet := ofenv1apply.NodeImageSet(nodeImageSetName).
			WithLabels(labelSet(imgPrefetch, nodeName)).
			WithSpec(ofenv1apply.NodeImageSetSpec().
				WithImages(imgPrefetch.Spec.Images...).
				WithRegistryPolicy(registryPolicy).
				WithNodeName(nodeName).
				WithImagePullSecrets(imgPrefetch.Spec.ImagePullSecrets...),
			).
			WithStatus(
				ofenv1apply.NodeImageSetStatus().
					WithImagePrefetchGeneration(imgPrefetch.Generation),
			)

		if err := r.applyNodeImageSet(ctx, nodeImageSet, nodeImageSetName); err != nil {
			return fmt.Errorf("failed to apply NodeImageSet: %w", err)
		}

		if err := r.applyNodeImageSetStatus(ctx, nodeImageSet, nodeImageSetName); err != nil {
			return fmt.Errorf("failed to apply NodeImageSet status: %w", err)
		}
	}

	// Delete unnecessary NodeImageSets
	nodeImageSetList := &ofenv1.NodeImageSetList{}
	if err := r.List(ctx, nodeImageSetList, client.MatchingLabels(map[string]string{
		constants.OwnerImagePrefetchNamespace: imgPrefetch.Namespace,
		constants.OwnerImagePrefetchName:      imgPrefetch.Name,
	})); err != nil {
		return fmt.Errorf("failed to list NodeImageSets: %w", err)
	}

	for _, nodeImageSet := range nodeImageSetList.Items {
		if _, ok := selectNodes[nodeImageSet.Spec.NodeName]; !ok {
			if err := r.Delete(ctx, &nodeImageSet); err != nil {
				if errors.IsNotFound(err) {
					// already deleted
					continue
				}
				return fmt.Errorf("failed to delete NodeImageSet: %w", err)
			}

			logger.Info("delete NodeImageSet", "name", nodeImageSet.Name)
		}
	}

	return nil
}

func (r *ImagePrefetchReconciler) applyNodeImageSet(ctx context.Context, nodeImageSet *ofenv1apply.NodeImageSetApplyConfiguration, name string) error {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(nodeImageSet)
	if err != nil {
		return fmt.Errorf("failed to convert NodeImageSet: %w", err)
	}
	patch := &unstructured.Unstructured{Object: obj}

	var current ofenv1.NodeImageSet
	err = r.Get(ctx, client.ObjectKey{Name: name}, &current)
	if !errors.IsNotFound(err) && err != nil {
		return fmt.Errorf("failed to get NodeImageSet: %w", err)
	}

	currentApplyConfig, err := ofenv1apply.ExtractNodeImageSet(&current, constants.ImagePrefetchFieldManager)
	if err != nil {
		return fmt.Errorf("failed to extract NodeImageSet: %w", err)
	}
	if equality.Semantic.DeepEqual(currentApplyConfig, nodeImageSet) {
		return nil
	}

	return r.Patch(ctx, patch, client.Apply, &client.PatchOptions{
		FieldManager: constants.ImagePrefetchFieldManager,
		Force:        ptr.To(true),
	})
}

func (r *ImagePrefetchReconciler) applyNodeImageSetStatus(ctx context.Context, nodeImageSet *ofenv1apply.NodeImageSetApplyConfiguration, name string) error {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(nodeImageSet)
	if err != nil {
		return fmt.Errorf("failed to convert NodeImageSet status: %w", err)
	}
	patch := &unstructured.Unstructured{Object: obj}

	var current ofenv1.NodeImageSet
	err = r.Get(ctx, types.NamespacedName{Name: name}, &current)
	if !errors.IsNotFound(err) && err != nil {
		return fmt.Errorf("failed to get NodeImageSet for status update: %w", err)
	}

	currentStatusApplyConfig, err := ofenv1apply.ExtractNodeImageSetStatus(&current, constants.ImagePrefetchFieldManager)
	if err != nil {
		return fmt.Errorf("failed to extract NodeImageSet status: %w", err)
	}

	if equality.Semantic.DeepEqual(currentStatusApplyConfig, nodeImageSet) {
		return nil
	}

	return r.Status().Patch(ctx, patch, client.Apply, client.ForceOwnership, client.FieldOwner(constants.ImagePrefetchFieldManager))
}

func labelSet(imgPrefetch *ofenv1.ImagePrefetch, nodeName string) map[string]string {
	return map[string]string{
		constants.OwnerImagePrefetchNamespace: imgPrefetch.Namespace,
		constants.OwnerImagePrefetchName:      imgPrefetch.Name,
		constants.NodeName:                    nodeName,
	}
}

func getNodeImageSetName(imgPrefetch *ofenv1.ImagePrefetch, nodeName string) string {
	name := imgPrefetch.Name
	namespace := imgPrefetch.Namespace
	sha1 := sha1.New()
	io.WriteString(sha1, name+"\000"+namespace+"\000"+nodeName)
	hash := hex.EncodeToString(sha1.Sum(nil))
	return fmt.Sprintf("%s-%s-%s", constants.NodeImageSetPrefix, name, hash[:8])
}

func (r *ImagePrefetchReconciler) updateStatus(ctx context.Context, imgPrefetch *ofenv1.ImagePrefetch, selectedNodes []string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	sort.Strings(selectedNodes)
	imgPrefetchSSA := ofenv1apply.ImagePrefetch(imgPrefetch.Name, imgPrefetch.Namespace).
		WithStatus(
			ofenv1apply.ImagePrefetchStatus().
				WithObservedGeneration(imgPrefetch.Generation).
				WithSelectedNodes(selectedNodes...),
		)

	result := ctrl.Result{RequeueAfter: 10 * time.Second}

	nodeImageSets := &ofenv1.NodeImageSetList{}
	if err := r.List(ctx, nodeImageSets, client.MatchingLabels(map[string]string{
		constants.OwnerImagePrefetchNamespace: imgPrefetch.Namespace,
		constants.OwnerImagePrefetchName:      imgPrefetch.Name,
	})); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list NodeImageSets: %w", err)
	}

	status := calculateStatus(selectedNodes, nodeImageSets, imgPrefetch.Generation)
	imgPrefetchSSA.Status.WithDesiredNodes(status.desiredNodes)
	imgPrefetchSSA.Status.WithImagePulledNodes(status.availableNodes)
	imgPrefetchSSA.Status.WithImagePullingNodes(status.pullingNodes)
	imgPrefetchSSA.Status.WithImagePullFailedNodes(status.pullFailedNodes)

	if status.availableNodes == status.desiredNodes {
		logger.Info("ImagePrefetch is ready", "name", imgPrefetch.Name)
		imgPrefetchSSA.Status.WithConditions(
			metav1apply.Condition().
				WithType(ofenv1.ConditionReady).
				WithStatus(metav1.ConditionTrue).
				WithReason("ImagePrefetchReady").
				WithMessage("All nodes have the desired image").
				WithLastTransitionTime(metav1.Now()),
			metav1apply.Condition().
				WithType(ofenv1.ConditionProgressing).
				WithStatus(metav1.ConditionFalse).
				WithReason("ImagePrefetchFinished").
				WithMessage("All nodes have the desired image").
				WithLastTransitionTime(metav1.Now()),
		)
		result = ctrl.Result{}
	} else {
		imgPrefetchSSA.Status.WithConditions(
			metav1apply.Condition().
				WithType(ofenv1.ConditionReady).
				WithStatus(metav1.ConditionFalse).
				WithReason("ImagePrefetchProgressing").
				WithMessage("Waiting for all nodes to pull the image").
				WithLastTransitionTime(metav1.Now()),
			metav1apply.Condition().
				WithType(ofenv1.ConditionProgressing).
				WithStatus(metav1.ConditionTrue).
				WithReason("ImagePrefetchProgressing").
				WithMessage("Waiting for all nodes to pull the image").
				WithLastTransitionTime(metav1.Now()),
		)
	}

	if status.pullFailedNodes > 0 {
		imgPrefetchSSA.Status.WithConditions(
			metav1apply.Condition().
				WithType(ofenv1.ConditionImagePullFailed).
				WithStatus(metav1.ConditionTrue).
				WithReason("ImagePrefetchFailed").
				WithMessage("Some nodes failed to pull the image").
				WithLastTransitionTime(metav1.Now()),
		)
	} else {
		imgPrefetchSSA.Status.WithConditions(
			metav1apply.Condition().
				WithType(ofenv1.ConditionImagePullFailed).
				WithStatus(metav1.ConditionFalse).
				WithReason("NoImagePullFailed").
				WithMessage("No nodes have failed to pull the image").
				WithLastTransitionTime(metav1.Now()),
		)
	}

	if err := r.applyImagePrefetchStatus(ctx, imgPrefetchSSA, imgPrefetch.Name); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update ImagePrefetch status: %w", err)
	}

	return result, nil
}

type NodeImageSetStatus struct {
	desiredNodes    int
	availableNodes  int
	pullingNodes    int
	pullFailedNodes int
}

func calculateStatus(selectNodes []string, nodeImageSets *ofenv1.NodeImageSetList, generation int64) NodeImageSetStatus {
	status := NodeImageSetStatus{}
	status.desiredNodes = len(selectNodes)

	for _, nodeImageSet := range nodeImageSets.Items {
		if nodeImageSet.Status.ImagePrefetchGeneration != generation {
			// Skip if NodeImageSet has an old generation of ImagePrefetch.
			// This occurs when the ImagePrefetch controller has outdated NodeImageSet information.
			continue
		}

		if meta.IsStatusConditionTrue(nodeImageSet.Status.Conditions, ofenv1.ConditionImageAvailable) {
			status.availableNodes++
		}
		if meta.IsStatusConditionTrue(nodeImageSet.Status.Conditions, ofenv1.ConditionImageDownloadFailed) {
			status.pullFailedNodes++
		}
		if meta.IsStatusConditionTrue(nodeImageSet.Status.Conditions, ofenv1.ConditionImageDownloadComplete) &&
			!meta.IsStatusConditionTrue(nodeImageSet.Status.Conditions, ofenv1.ConditionImageAvailable) {
			status.pullingNodes++
		}
	}

	return status
}

func (r *ImagePrefetchReconciler) applyImagePrefetchStatus(ctx context.Context, imgPrefetch *ofenv1apply.ImagePrefetchApplyConfiguration, name string) error {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(imgPrefetch)
	if err != nil {
		return fmt.Errorf("failed to convert ImagePrefetch status: %w", err)
	}
	patch := &unstructured.Unstructured{Object: obj}

	var current ofenv1.ImagePrefetch
	err = r.Get(ctx, types.NamespacedName{Name: name}, &current)
	if !errors.IsNotFound(err) && err != nil {
		return fmt.Errorf("failed to get ImagePrefetch for status update: %w", err)
	}

	currentStatusApplyConfig, err := ofenv1apply.ExtractImagePrefetchStatus(&current, constants.ImagePrefetchFieldManager)
	if err != nil {
		return fmt.Errorf("failed to extract ImagePrefetch status: %w", err)
	}

	if equality.Semantic.DeepEqual(currentStatusApplyConfig, imgPrefetch) {
		return nil
	}

	return r.Status().Patch(ctx, patch, client.Apply, client.ForceOwnership, client.FieldOwner(constants.ImagePrefetchFieldManager))
}

// SetupWithManager sets up the controller with the Manager.
func (r *ImagePrefetchReconciler) SetupWithManager(mgr ctrl.Manager) error {
	nodeImageSetHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []ctrl.Request {
			nodeImageSet := obj.(*ofenv1.NodeImageSet)

			return []ctrl.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: nodeImageSet.Labels[constants.OwnerImagePrefetchNamespace],
						Name:      nodeImageSet.Labels[constants.OwnerImagePrefetchName],
					},
				},
			}
		})

	nodeHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []ctrl.Request {
			node := obj.(*corev1.Node)
			imagePrefetchList := &ofenv1.ImagePrefetchList{}
			err := r.List(ctx, imagePrefetchList)
			if err != nil {
				return nil
			}

			var requests []ctrl.Request
			for _, imgPrefetch := range imagePrefetchList.Items {
				if slices.Contains(imgPrefetch.Status.SelectedNodes, node.Name) {
					requests = append(requests, ctrl.Request{
						NamespacedName: types.NamespacedName{
							Namespace: imgPrefetch.Namespace,
							Name:      imgPrefetch.Name,
						},
					})
				}
			}

			return requests
		})

	return ctrl.NewControllerManagedBy(mgr).
		For(&ofenv1.ImagePrefetch{}).
		Watches(
			&ofenv1.NodeImageSet{},
			nodeImageSetHandler,
			builder.WithPredicates(
				predicate.Funcs{
					UpdateFunc: func(e event.UpdateEvent) bool {
						return true
					},
					DeleteFunc: func(e event.DeleteEvent) bool {
						return true
					},
				},
			),
		).
		Watches(
			&corev1.Node{},
			nodeHandler,
			builder.WithPredicates(
				predicate.Funcs{
					CreateFunc: func(e event.CreateEvent) bool {
						return true
					},
					DeleteFunc: func(e event.DeleteEvent) bool {
						return true
					},
				},
			),
		).
		Complete(r)
}

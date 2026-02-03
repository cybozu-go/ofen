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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	metav1apply "k8s.io/client-go/applyconfigurations/meta/v1"
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
	"github.com/cybozu-go/ofen/internal/metrics"
	"github.com/cybozu-go/ofen/internal/util"
)

// ImagePrefetchReconciler reconciles a ImagePrefetch object
type ImagePrefetchReconciler struct {
	client.Client
	Scheme                             *runtime.Scheme
	ImagePullNodeLimit                 int
	MaxConcurrentNodeImageSetCreations int
}

// +kubebuilder:rbac:groups=ofen.cybozu.io,resources=imageprefetches,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ofen.cybozu.io,resources=imageprefetches/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ofen.cybozu.io,resources=imageprefetches/finalizers,verbs=update
// +kubebuilder:rbac:groups=ofen.cybozu.io,resources=nodeimagesets,verbs=get;list;watch;create;update;patch;delete;deletecollection
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

	sort.Strings(selectNodes)
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
	r.removeMetrics(imgPrefetch)

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
		needsNodeSelection := isNeedNodeSelection(imgPrefetch, readyNodes)
		if needsNodeSelection {
			nodes, err := selectNodesByReplicas(imgPrefetch, readyNodes)
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

func isNeedNodeSelection(imgPrefetch *ofenv1.ImagePrefetch, readyNodes []corev1.Node) bool {
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

func selectNodesByReplicas(imgPrefetch *ofenv1.ImagePrefetch, readyNodes []corev1.Node) ([]string, error) {
	targetReplicas := imgPrefetch.Spec.Replicas
	selectNodes := make([]corev1.Node, 0, targetReplicas)

	if len(readyNodes) < targetReplicas {
		return nil, fmt.Errorf("not enough nodes available: %d < %d", len(readyNodes), targetReplicas)
	}

	readyNodesMap := make(map[string]corev1.Node)
	for _, node := range readyNodes {
		readyNodesMap[node.Name] = node
	}

	readyNodesName := getNodeNames(readyNodes)
	for _, nodeName := range imgPrefetch.Status.SelectedNodes {
		if len(selectNodes) >= targetReplicas {
			return getNodeNames(selectNodes), nil
		}
		if slices.Contains(readyNodesName, nodeName) {
			selectNodes = append(selectNodes, readyNodesMap[nodeName])
		}
	}

	for range readyNodes {
		if len(selectNodes) >= targetReplicas {
			return getNodeNames(selectNodes), nil
		}

		candidates := filterSelectNodes(readyNodes, selectNodes)
		if len(candidates) == 0 {
			return nil, fmt.Errorf("no more nodes available for selection")
		}

		zoneCount := getZoneCount(selectNodes)
		sort.Slice(candidates, func(i, j int) bool {
			si := scoreNode(candidates[i], zoneCount)
			sj := scoreNode(candidates[j], zoneCount)
			return si < sj
		})
		selectNodes = append(selectNodes, candidates[0])
	}

	return getNodeNames(selectNodes), nil
}

func getZoneCount(selectedNodes []corev1.Node) map[string]int {
	zoneCount := make(map[string]int)
	for _, node := range selectedNodes {
		zone := node.Labels[corev1.LabelTopologyZone]
		zoneCount[zone]++
	}
	return zoneCount
}

func filterSelectNodes(readyNodes []corev1.Node, selectedNodes []corev1.Node) []corev1.Node {
	var candidates []corev1.Node
	selectedSet := make(map[string]bool)
	for _, node := range selectedNodes {
		selectedSet[node.Name] = true
	}
	for _, node := range readyNodes {
		if !selectedSet[node.Name] {
			candidates = append(candidates, node)
		}
	}
	return candidates
}

func scoreNode(node corev1.Node, zoneCount map[string]int) int {
	zone := node.Labels[corev1.LabelTopologyZone]
	return zoneCount[zone]
}

func (r *ImagePrefetchReconciler) createOrUpdateNodeImageSet(ctx context.Context, imgPrefetch *ofenv1.ImagePrefetch, selectedNodes []string) error {
	logger := log.FromContext(ctx)

	var nodeImageSetList ofenv1.NodeImageSetList
	if err := r.List(ctx, &nodeImageSetList, client.MatchingLabels(map[string]string{
		constants.OwnerImagePrefetchNamespace: imgPrefetch.Namespace,
		constants.OwnerImagePrefetchName:      imgPrefetch.Name,
	})); err != nil {
		return fmt.Errorf("failed to list NodeImageSets: %w", err)
	}

	maxNodeImageSetsToProcess := getMaxNodeImageSetsToCreate(nodeImageSetList, r.MaxConcurrentNodeImageSetCreations, len(selectedNodes), imgPrefetch.Generation)
	selectNodes := map[string]struct{}{}
	for i, nodeName := range selectedNodes {
		// Limit the number of NodeImageSets created or updated in one reconciliation loop
		if i >= maxNodeImageSetsToProcess {
			return nil
		}

		selectNodes[nodeName] = struct{}{}
		nodeImageSetName, err := getNodeImageSetName(imgPrefetch, nodeName)
		if err != nil {
			return fmt.Errorf("failed to get NodeImageSet name: %w", err)
		}

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
	nodeImageSetList = ofenv1.NodeImageSetList{}
	if err := r.List(ctx, &nodeImageSetList, client.MatchingLabels(map[string]string{
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

func getMaxNodeImageSetsToCreate(currentNodeImageSets ofenv1.NodeImageSetList, maxConcurrentNodeImageSetCreations int, desiredNodeImageSetsCount int, generation int64) int {
	var readyNodeImageSetsCount = 0

	for _, nodeImageSet := range currentNodeImageSets.Items {
		if meta.IsStatusConditionTrue(nodeImageSet.Status.Conditions, ofenv1.ConditionImageAvailable) &&
			nodeImageSet.Status.ImagePrefetchGeneration == generation &&
			len(nodeImageSet.Spec.Images) == len(nodeImageSet.Status.ContainerImageStatuses) {
			readyNodeImageSetsCount++
		}
	}

	if readyNodeImageSetsCount < desiredNodeImageSetsCount {
		return readyNodeImageSetsCount + maxConcurrentNodeImageSetCreations
	}
	return desiredNodeImageSetsCount
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

func getNodeImageSetName(imgPrefetch *ofenv1.ImagePrefetch, nodeName string) (string, error) {
	name := imgPrefetch.Name
	namespace := imgPrefetch.Namespace
	hasher := sha1.New()
	if _, err := io.WriteString(hasher, name+"\000"+namespace+"\000"+nodeName); err != nil {
		return "", fmt.Errorf("failed to write string to sha1: %w", err)
	}
	hash := hex.EncodeToString(hasher.Sum(nil))
	return fmt.Sprintf("%s-%s-%s", constants.NodeImageSetNamePrefix, name, hash[:8]), nil
}

func (r *ImagePrefetchReconciler) updateStatus(ctx context.Context, imgPrefetch *ofenv1.ImagePrefetch, selectedNodes []string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
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

	if len(nodeImageSets.Items) == len(selectedNodes) {
		imgPrefetchSSA.Status.WithConditions(
			metav1apply.Condition().
				WithType(ofenv1.ConditionNodeImageSetsCreated).
				WithStatus(metav1.ConditionTrue).
				WithReason("NodeImageSetsCreated").
				WithMessage("All NodeImageSets have been created").
				WithLastTransitionTime(metav1.Now()),
		)
	} else {
		imgPrefetchSSA.Status.WithConditions(
			metav1apply.Condition().
				WithType(ofenv1.ConditionNodeImageSetsCreated).
				WithStatus(metav1.ConditionFalse).
				WithReason("NodeImageSetsCreating").
				WithMessage("Waiting for all NodeImageSets to be created").
				WithLastTransitionTime(metav1.Now()),
		)
	}

	if status.availableNodes == status.desiredNodes {
		logger.Info("ImagePrefetch is ready", "name", imgPrefetch.Name)
		imgPrefetchSSA.Status.WithConditions(
			metav1apply.Condition().
				WithType(ofenv1.ConditionReady).
				WithStatus(metav1.ConditionTrue).
				WithReason("ImagePrefetchReady").
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
		)
	}

	if status.pullFailedNodes > 0 {
		imgPrefetchSSA.Status.WithConditions(
			metav1apply.Condition().
				WithType(ofenv1.ConditionNoImagePullFailed).
				WithStatus(metav1.ConditionFalse).
				WithReason("ImagePrefetchFailed").
				WithMessage("Some nodes failed to pull the image").
				WithLastTransitionTime(metav1.Now()),
		)
	} else {
		imgPrefetchSSA.Status.WithConditions(
			metav1apply.Condition().
				WithType(ofenv1.ConditionNoImagePullFailed).
				WithStatus(metav1.ConditionTrue).
				WithReason("NoImagePullFailed").
				WithMessage("No nodes have failed to pull the image").
				WithLastTransitionTime(metav1.Now()),
		)
	}

	if err := r.applyImagePrefetchStatus(ctx, imgPrefetchSSA, imgPrefetch.Name); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update ImagePrefetch status: %w", err)
	}

	r.setMetrics(imgPrefetch)

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

		if nodeImageSet.Status.Conditions == nil {
			return status
		}

		if meta.IsStatusConditionTrue(nodeImageSet.Status.Conditions, ofenv1.ConditionImageAvailable) {
			status.availableNodes++
		}
		if !meta.IsStatusConditionTrue(nodeImageSet.Status.Conditions, ofenv1.ConditionImageDownloadSucceeded) {
			status.pullFailedNodes++
		}
		if !meta.IsStatusConditionTrue(nodeImageSet.Status.Conditions, ofenv1.ConditionImageAvailable) {
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

func (r *ImagePrefetchReconciler) setMetrics(imgPrefetch *ofenv1.ImagePrefetch) {
	if meta.IsStatusConditionTrue(imgPrefetch.Status.Conditions, ofenv1.ConditionReady) {
		metrics.ReadyVec.WithLabelValues(imgPrefetch.Namespace, imgPrefetch.Name).Set(1)
	} else {
		metrics.ReadyVec.WithLabelValues(imgPrefetch.Namespace, imgPrefetch.Name).Set(0)
	}

	metrics.ImagePulledNodesVec.WithLabelValues(imgPrefetch.Namespace, imgPrefetch.Name).Set(float64(imgPrefetch.Status.ImagePulledNodes))
	metrics.ImagePullFailedNodesVec.WithLabelValues(imgPrefetch.Namespace, imgPrefetch.Name).Set(float64(imgPrefetch.Status.ImagePullFailedNodes))
}

func (r *ImagePrefetchReconciler) removeMetrics(imgPrefetch *ofenv1.ImagePrefetch) {
	metrics.ReadyVec.DeleteLabelValues(imgPrefetch.Namespace, imgPrefetch.Name)
	metrics.ImagePulledNodesVec.DeleteLabelValues(imgPrefetch.Namespace, imgPrefetch.Name)
	metrics.ImagePullFailedNodesVec.DeleteLabelValues(imgPrefetch.Namespace, imgPrefetch.Name)
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
				if imgPrefetch.Spec.AllNodes {
					if util.IsLabelSelectorEmpty(&imgPrefetch.Spec.NodeSelector) {
						requests = append(requests, ctrl.Request{
							NamespacedName: types.NamespacedName{
								Namespace: imgPrefetch.Namespace,
								Name:      imgPrefetch.Name,
							},
						})
						continue
					}

					selector, err := metav1.LabelSelectorAsSelector(&imgPrefetch.Spec.NodeSelector)
					if err != nil {
						return nil
					}
					if selector.Matches(labels.Set(node.Labels)) {
						requests = append(requests, ctrl.Request{
							NamespacedName: types.NamespacedName{
								Namespace: imgPrefetch.Namespace,
								Name:      imgPrefetch.Name,
							},
						})
					}
					continue
				}

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
					CreateFunc: func(e event.CreateEvent) bool {
						return false
					},
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
					UpdateFunc: func(e event.UpdateEvent) bool {
						oldNode := e.ObjectOld.(*corev1.Node)
						newNode := e.ObjectNew.(*corev1.Node)
						// change node status from not ready to ready
						if !util.IsNodeReady(oldNode) && util.IsNodeReady(newNode) {
							return true
						}
						// change node status from ready to not ready
						if util.IsNodeReady(oldNode) && !util.IsNodeReady(newNode) {
							return true
						}

						return false
					},
					DeleteFunc: func(e event.DeleteEvent) bool {
						return true
					},
				},
			),
		).
		Complete(r)
}

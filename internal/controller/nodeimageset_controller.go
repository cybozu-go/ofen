package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	ofenv1apply "github.com/cybozu-go/ofen/internal/applyconfigurations/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	metav1apply "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// NodeImageSetReconciler reconciles a NodeImageSet object
type NodeImageSetReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	NodeName    string
	ImagePuller *imagePuller
	Recorder    record.EventRecorder
}

// +kubebuilder:rbac:groups=ofen.cybozu.io,resources=nodeimagesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ofen.cybozu.io,resources=nodeimagesets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ofen.cybozu.io,resources=nodeimagesets/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;update;patch

func (r *NodeImageSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var nodeImageSet ofenv1.NodeImageSet
	if err := r.Get(ctx, req.NamespacedName, &nodeImageSet); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if nodeImageSet.Spec.NodeName != r.NodeName {
		return ctrl.Result{}, nil
	}

	if nodeImageSet.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(&nodeImageSet, constants.NodeImageSetFinalizer) {
			r.ImagePuller.stop(&nodeImageSet)
			controllerutil.RemoveFinalizer(&nodeImageSet, constants.NodeImageSetFinalizer)
			if err := r.Update(ctx, &nodeImageSet); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&nodeImageSet, constants.NodeImageSetFinalizer) {
		controllerutil.AddFinalizer(&nodeImageSet, constants.NodeImageSetFinalizer)
		err := r.Update(ctx, &nodeImageSet)
		if err != nil {
			logger.Error(err, "failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	node := &corev1.Node{}
	if err := r.Get(ctx, client.ObjectKey{Name: r.NodeName}, node); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}

		node = nil
	}

	if node == nil || node.DeletionTimestamp != nil {
		logger.Info("node is not found or being deleted", "node", r.NodeName)
		if err := r.Delete(ctx, &nodeImageSet); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if err := r.reconcileNodeImageSet(ctx, &nodeImageSet); err != nil {
		return ctrl.Result{}, err
	}

	return r.updateStatus(ctx, &nodeImageSet)
}

func (r *NodeImageSetReconciler) reconcileNodeImageSet(ctx context.Context, nodeImageSet *ofenv1.NodeImageSet) error {
	logger := log.FromContext(ctx)

	imagePrefetchNamespace := nodeImageSet.Labels[constants.OwnerImagePrefetchNamespace]
	secrets := make([]corev1.Secret, 0, len(nodeImageSet.Spec.ImagePullSecrets))
	for _, secretRef := range nodeImageSet.Spec.ImagePullSecrets {
		var secret corev1.Secret
		err := r.Get(ctx, client.ObjectKey{Namespace: imagePrefetchNamespace, Name: secretRef.Name}, &secret)
		if apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			logger.Error(err, "failed to get image pull secret", "secret", secretRef.Name)
			return err
		}
		secrets = append(secrets, secret)
	}

	if err := r.ImagePuller.start(ctx, nodeImageSet, secrets, r.Recorder); err != nil {
		logger.Error(err, "failed to start image pull process")
		return err
	}

	return nil
}

func (r *NodeImageSetReconciler) updateStatus(ctx context.Context, nodeImageSet *ofenv1.NodeImageSet) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	result := ctrl.Result{RequeueAfter: 10 * time.Second}

	var node corev1.Node
	if err := r.Get(ctx, client.ObjectKey{Name: nodeImageSet.Spec.NodeName}, &node); err != nil {
		return ctrl.Result{}, err
	}

	var nodeImageList []string
	for _, image := range node.Status.Images {
		nodeImageList = append(nodeImageList, image.Names...)
	}

	desired := len(nodeImageSet.Spec.Images)
	downloaded := 0
	for _, image := range nodeImageSet.Spec.Images {
		if slices.Contains(nodeImageList, image) {
			downloaded++
		}
	}

	nodeImageSetSSA := ofenv1apply.NodeImageSet(nodeImageSet.Name).
		WithStatus(
			ofenv1apply.NodeImageSetStatus().
				WithDesiredImages(desired).
				WithAvailableImages(downloaded).
				WithObservedGeneration(nodeImageSet.Generation),
		)

	if desired == downloaded {
		logger.Info("all images are downloaded")
		nodeImageSetSSA.Status.WithConditions(
			metav1apply.Condition().
				WithType(ofenv1.ConditionImageAvailable).
				WithStatus(metav1.ConditionTrue).
				WithReason("ImageDownloadComplete").
				WithMessage("All images are downloaded"),
		)
		nodeImageSetSSA.Status.WithConditions(
			metav1apply.Condition().
				WithType(ofenv1.ConditionImageDownloadComplete).
				WithStatus(metav1.ConditionTrue).
				WithReason("ImageDownloadComplete").
				WithMessage("All images are downloaded").
				WithLastTransitionTime(metav1.Now()),
		)
		result = ctrl.Result{}
	} else {
		nodeImageSetSSA.Status.WithConditions(
			metav1apply.Condition().
				WithType(ofenv1.ConditionImageAvailable).
				WithStatus(metav1.ConditionFalse).
				WithReason("ImageDownloadIncomplete").
				WithMessage("Waiting for images to be downloaded"),
		)
		nodeImageSetSSA.Status.WithConditions(
			metav1apply.Condition().
				WithType(ofenv1.ConditionImageDownloadComplete).
				WithStatus(metav1.ConditionFalse).
				WithReason("ImageDownloadIncomplete").
				WithMessage("Waiting for images to be downloaded").
				WithLastTransitionTime(metav1.Now()),
		)
	}

	if err := r.applyNodeImageSetStatus(ctx, nodeImageSetSSA, nodeImageSet.Name); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}
	return result, nil
}

func (r *NodeImageSetReconciler) applyNodeImageSetStatus(ctx context.Context, nodeImageSetSSA *ofenv1apply.NodeImageSetApplyConfiguration, name string) error {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(nodeImageSetSSA)
	if err != nil {
		return fmt.Errorf("failed to convert to unstructured: %w", err)
	}
	patch := &unstructured.Unstructured{
		Object: obj,
	}

	var current ofenv1.NodeImageSet
	err = r.Get(ctx, types.NamespacedName{Name: name}, &current)
	if !apierrors.IsNotFound(err) && err != nil {
		return fmt.Errorf("failed to get NodeImageSet for status update: %w", err)
	}

	currentStatusApplyConfig, err := ofenv1apply.ExtractNodeImageSetStatus(&current, constants.NodeImageSetFieldManager)
	if err != nil {
		return fmt.Errorf("failed to extract NodeImageSet status: %w", err)
	}

	if equality.Semantic.DeepEqual(currentStatusApplyConfig, nodeImageSetSSA) {
		return nil
	}

	return r.Status().Patch(ctx, patch, client.Apply, client.ForceOwnership, client.FieldOwner(constants.NodeImageSetFieldManager))

}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeImageSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	nodeHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			logger := log.FromContext(ctx)
			node := obj.(*corev1.Node)

			var nodeImageSetList ofenv1.NodeImageSetList
			if err := r.List(ctx, &nodeImageSetList, &client.ListOptions{
				LabelSelector: labels.SelectorFromSet(
					labels.Set{
						constants.NodeName: node.Name,
					},
				),
			}); err != nil {
				logger.Error(err, "failed to list NodeImageSet")
				return nil
			}

			var requests []ctrl.Request
			for _, nis := range nodeImageSetList.Items {
				requests = append(requests, ctrl.Request{
					NamespacedName: client.ObjectKeyFromObject(&nis),
				})
			}
			return requests
		})

	return ctrl.NewControllerManagedBy(mgr).
		For(&ofenv1.NodeImageSet{}).
		Watches(
			&corev1.Node{},
			nodeHandler,
			builder.WithPredicates(nodePredicate()),
		).
		Complete(r)
}

func nodePredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode := e.ObjectOld.(*corev1.Node)
			newNode := e.ObjectNew.(*corev1.Node)
			return !equality.Semantic.DeepEqual(oldNode.Status.Images, newNode.Status.Images)
		},
		CreateFunc: func(e event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return true
		},
	}
}

package controller

import (
	"context"
	"slices"
	"time"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
		logger.Error(err, "failed to get NodeImageSet")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if nodeImageSet.Spec.NodeName != r.NodeName {
		logger.Info("NodeImageSet is not for this node", "node", nodeImageSet.Spec.NodeName)
		return ctrl.Result{}, nil
	}

	node := &corev1.Node{}
	if err := r.Get(ctx, client.ObjectKey{Name: r.NodeName}, node); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}

		node = nil
	}

	if node == nil || node.DeletionTimestamp != nil {
		if err := r.Delete(ctx, &nodeImageSet); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if err := r.reconcileNodeImageSet(ctx, &nodeImageSet, node); err != nil {
		return ctrl.Result{}, err
	}

	return r.updateStatus(ctx, &nodeImageSet)
}

func (r *NodeImageSetReconciler) reconcileNodeImageSet(ctx context.Context, nodeImageSet *ofenv1.NodeImageSet, node *corev1.Node) error {
	logger := log.FromContext(ctx)

	imagePrefetchNamespace := nodeImageSet.Labels[constants.OwnerImagePrefetchNamespace]
	var secrets []corev1.Secret
	for _, secretRef := range nodeImageSet.Spec.ImagePullSecrets {
		var secret corev1.Secret
		err := r.Get(ctx, client.ObjectKey{Namespace: imagePrefetchNamespace, Name: secretRef.Name}, &secret)
		if err != nil {
			logger.Error(err, "failed to get image pull secret", "secret", secretRef.Name)
			return err
		}
		secrets = append(secrets, secret)
	}

	return r.ImagePuller.start(ctx, nodeImageSet, secrets, r.Recorder)
}

func (r *NodeImageSetReconciler) updateStatus(ctx context.Context, nodeImageSet *ofenv1.NodeImageSet) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	meta.SetStatusCondition(&nodeImageSet.Status.Conditions, metav1.Condition{
		Type:    ofenv1.ConditionImageAvailable,
		Status:  metav1.ConditionFalse,
		Reason:  "ImageDownloadInProgress",
		Message: "Image download is in progress",
	})
	meta.SetStatusCondition(&nodeImageSet.Status.Conditions, metav1.Condition{
		Type:    ofenv1.ConditionImageDownloadComplete,
		Status:  metav1.ConditionFalse,
		Reason:  "ImageDownloadInProgress",
		Message: "Image download is in progress",
	})

	result := ctrl.Result{RequeueAfter: 10 * time.Second}
	var node corev1.Node
	if err := r.Get(ctx, client.ObjectKey{Name: nodeImageSet.Spec.NodeName}, &node); err != nil {
		return ctrl.Result{}, err
	}

	var nodeImageList []string
	for _, image := range node.Status.Images {
		nodeImageList = append(nodeImageList, image.Names...)
	}

	desired := len(nodeImageSet.Spec.ImageSet)
	downloaded := 0
	for _, image := range nodeImageSet.Spec.ImageSet {
		if slices.Contains(nodeImageList, image.Image) {
			downloaded++
		}
	}

	nodeImageSet.Status.DesiredImages = desired
	nodeImageSet.Status.AvailableImages = downloaded

	if desired == downloaded {
		logger.Info("all images are downloaded")
		meta.SetStatusCondition(&nodeImageSet.Status.Conditions, metav1.Condition{
			Type:    ofenv1.ConditionImageAvailable,
			Status:  metav1.ConditionTrue,
			Reason:  "ImageDownloadComplete",
			Message: "All images are downloaded",
		})
		meta.SetStatusCondition(&nodeImageSet.Status.Conditions, metav1.Condition{
			Type:    ofenv1.ConditionImageDownloadComplete,
			Status:  metav1.ConditionTrue,
			Reason:  "ImageDownloadComplete",
			Message: "All images are downloaded",
		})
		result = ctrl.Result{}
	}

	return result, r.Status().Update(ctx, nodeImageSet)
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
				requests = append(requests, reconcile.Request{
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

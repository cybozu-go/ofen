package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	metav1apply "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	ofenv1apply "github.com/cybozu-go/ofen/internal/applyconfigurations/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
	"github.com/cybozu-go/ofen/internal/imgmanager"
)

// NodeImageSetReconciler reconciles a NodeImageSet object
type NodeImageSetReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	NodeName         string
	ImagePuller      *imgmanager.ImagePuller
	ContainerdClient imgmanager.ContainerdClient
	Recorder         record.EventRecorder
	Queue            workqueue.TypedRateLimitingInterface[imgmanager.Task]
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
			logger.Info("starting finalization")
			r.ImagePuller.DeleteNodeImageSetStatus(nodeImageSet.Name)
			controllerutil.RemoveFinalizer(&nodeImageSet, constants.NodeImageSetFinalizer)
			if err := r.Update(ctx, &nodeImageSet); err != nil {
				return ctrl.Result{}, err
			}
			logger.Info("finished finalization")
		}

		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&nodeImageSet, constants.NodeImageSetFinalizer) {
		err := r.Update(ctx, &nodeImageSet)
		if err != nil {
			logger.Error(err, "failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
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

	if !r.ImagePuller.IsExistsNodeImageSetStatus(nodeImageSet.Name) {
		logger.Info("initializing NodeImageSet status")
		r.ImagePuller.NewNodeImageSetStatus(nodeImageSet.Name)
	}

	if nodeImageSet.Generation != nodeImageSet.Status.ObservedGeneration {
		r.ImagePuller.UpdateNodeImageSetStatus(nodeImageSet.Name, nodeImageSet.Spec.Images)
	}

	pendingImages := r.collectPendingImages(ctx, nodeImageSet)
	if len(pendingImages) == 0 {
		logger.Info("no images to process")
		return nil
	}

	for _, image := range pendingImages {
		task := imgmanager.Task{
			Ref:            image,
			RegistryPolicy: nodeImageSet.Spec.RegistryPolicy,
			NodeImageSet:   nodeImageSet,
			Secrets:        &secrets,
		}
		r.Queue.Add(task)
	}

	return nil
}

func (r *NodeImageSetReconciler) collectPendingImages(ctx context.Context, nodeImageSet *ofenv1.NodeImageSet) []string {
	if nodeImageSet.Generation != nodeImageSet.Status.ObservedGeneration {
		return nodeImageSet.Spec.Images
	}

	var images []string
	for _, image := range nodeImageSet.Spec.Images {
		status, _, err := r.ImagePuller.GetImageStatus(ctx, nodeImageSet.Name, image, nodeImageSet.Spec.RegistryPolicy)
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to get image status", "image", image)
			continue
		}
		if status == ofenv1.WaitingForImageDownload {
			images = append(images, image)
		}
	}
	return images
}

func (r *NodeImageSetReconciler) updateStatus(ctx context.Context, nodeImageSet *ofenv1.NodeImageSet) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("updating NodeImageSet status")
	result := ctrl.Result{RequeueAfter: 10 * time.Second}

	desiredImage := len(nodeImageSet.Spec.Images)
	downloadedImage, failedImage := r.calculateImageStatus(ctx, nodeImageSet)

	nodeImageSetSSA := ofenv1apply.NodeImageSet(nodeImageSet.Name).
		WithStatus(
			ofenv1apply.NodeImageSetStatus().
				WithDesiredImages(desiredImage).
				WithAvailableImages(downloadedImage).
				WithDownloadFailedImages(failedImage).
				WithObservedGeneration(nodeImageSet.Generation),
		)

	for _, image := range nodeImageSet.Spec.Images {
		var errMsg string
		status, errMsg, err := r.ImagePuller.GetImageStatus(ctx, nodeImageSet.Name, image, nodeImageSet.Spec.RegistryPolicy)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get image status for %s: %w", image, err)
		}

		nodeImageSetSSA.Status.WithContainerImageStatuses(
			ofenv1apply.ContainerImageStatus().
				WithImageRef(image).
				WithState(status).
				WithError(errMsg),
		)
	}

	if desiredImage == downloadedImage {
		logger.Info("all images are downloadedImage")
		meta.SetStatusCondition(&nodeImageSet.Status.Conditions, metav1.Condition{
			Type:    ofenv1.ConditionImageAvailable,
			Status:  metav1.ConditionTrue,
			Reason:  "ImageDownloadComplete",
			Message: "All images are downloadedImage",
		})
		result = ctrl.Result{}
	} else {
		meta.SetStatusCondition(&nodeImageSet.Status.Conditions, metav1.Condition{
			Type:    ofenv1.ConditionImageAvailable,
			Status:  metav1.ConditionFalse,
			Reason:  "ImageDownloadIncomplete",
			Message: "Waiting for images to be downloadedImage",
		})
	}

	if failedImage > 0 {
		meta.SetStatusCondition(&nodeImageSet.Status.Conditions, metav1.Condition{
			Type:    ofenv1.ConditionImageDownloadSucceeded,
			Status:  metav1.ConditionFalse,
			Reason:  "ImageDownloadFailed",
			Message: "Some images failed to download",
		})
	} else {
		meta.SetStatusCondition(&nodeImageSet.Status.Conditions, metav1.Condition{
			Type:    ofenv1.ConditionImageDownloadSucceeded,
			Status:  metav1.ConditionTrue,
			Reason:  "NoImagePullFailed",
			Message: "No image pull failed",
		})
	}

	for _, condition := range nodeImageSet.Status.Conditions {
		nodeImageSetSSA.Status.WithConditions(
			metav1apply.Condition().
				WithType(condition.Type).
				WithStatus(condition.Status).
				WithReason(condition.Reason).
				WithMessage(condition.Message).
				WithLastTransitionTime(condition.LastTransitionTime),
		)
	}

	if err := r.applyNodeImageSetStatus(ctx, nodeImageSetSSA, nodeImageSet.Name); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}
	return result, nil
}

func (r *NodeImageSetReconciler) calculateImageStatus(ctx context.Context, nis *ofenv1.NodeImageSet) (int, int) {
	downloadedImage := 0
	failed := 0

	for _, image := range nis.Spec.Images {
		status, _, err := r.ImagePuller.GetImageStatus(ctx, nis.Name, image, nis.Spec.RegistryPolicy)
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to get image status", "image", image)
			continue
		}
		switch status {
		case ofenv1.ImageDownloaded:
			downloadedImage++
		case ofenv1.ImageDownloadFailed:
			failed++
		case ofenv1.ImageDownloadTemporarilyFailed:
			// Do not reflect ImageDownloadTemporarilyFailed in the status.
			// It's a temporary error that resolves once the image is cached in the registry mirror.
		case ofenv1.ImageDownloadInProgress:
		default:
		}
	}
	return downloadedImage, failed
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

	if equality.Semantic.DeepEqual(currentStatusApplyConfig, nodeImageSetSSA.Status) {
		return nil
	}

	return r.Status().Patch(ctx, patch, client.Apply, client.ForceOwnership, client.FieldOwner(constants.NodeImageSetFieldManager))
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeImageSetReconciler) SetupWithManager(mgr ctrl.Manager, ch chan event.TypedGenericEvent[*ofenv1.NodeImageSet]) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ofenv1.NodeImageSet{}).
		WatchesRawSource(source.Channel(ch, &handler.TypedEnqueueRequestForObject[*ofenv1.NodeImageSet]{})).
		Complete(r)
}

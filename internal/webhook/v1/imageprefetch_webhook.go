package v1

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
)

// log is for logging in this package.
var imageprefetchlog = logf.Log.WithName("imageprefetch-resource")

// SetupImagePrefetchWebhookWithManager registers the webhook for ImagePrefetch in the manager.
func SetupImagePrefetchWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &ofenv1.ImagePrefetch{}).
		WithDefaulter(&ImagePrefetchCustomDefaulter{}).
		WithValidator(&ImagePrefetchCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-ofen-cybozu-io-v1-imageprefetch,mutating=true,failurePolicy=fail,sideEffects=None,groups=ofen.cybozu.io,resources=imageprefetches,verbs=create;update,versions=v1,name=mimageprefetch.kb.io,admissionReviewVersions=v1

type ImagePrefetchCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ admission.Defaulter[*ofenv1.ImagePrefetch] = &ImagePrefetchCustomDefaulter{}

// Default implements admission.Defaulter so a webhook will be registered for the Kind ImagePrefetch.
func (d *ImagePrefetchCustomDefaulter) Default(ctx context.Context, obj *ofenv1.ImagePrefetch) error {
	imageprefetchlog.Info("Defaulting for ImagePrefetch", "name", obj.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-ofen-cybozu-io-v1-imageprefetch,mutating=false,failurePolicy=fail,sideEffects=None,groups=ofen.cybozu.io,resources=imageprefetches,verbs=create;update,versions=v1,name=vimageprefetch.kb.io,admissionReviewVersions=v1

type ImagePrefetchCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ admission.Validator[*ofenv1.ImagePrefetch] = &ImagePrefetchCustomValidator{}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type ImagePrefetch.
func (v *ImagePrefetchCustomValidator) ValidateCreate(ctx context.Context, obj *ofenv1.ImagePrefetch) (admission.Warnings, error) {
	imageprefetchlog.Info("Validation for ImagePrefetch upon creation", "name", obj.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type ImagePrefetch.
func (v *ImagePrefetchCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *ofenv1.ImagePrefetch) (admission.Warnings, error) {
	imageprefetchlog.Info("Validation for ImagePrefetch upon update", "name", newObj.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type ImagePrefetch.
func (v *ImagePrefetchCustomValidator) ValidateDelete(ctx context.Context, obj *ofenv1.ImagePrefetch) (admission.Warnings, error) {
	imageprefetchlog.Info("Validation for ImagePrefetch upon deletion", "name", obj.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}

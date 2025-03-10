package v1

import (
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var imageprefetchlog = logf.Log.WithName("imageprefetch-resource")

// SetupWebhookWithManager will setup the manager to manage the webhooks
func (r *ImagePrefetch) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-ofen-cybozu-io-v1-imageprefetch,mutating=true,failurePolicy=fail,sideEffects=None,groups=ofen.cybozu.io,resources=imageprefetches,verbs=create;update,versions=v1,name=mimageprefetch.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &ImagePrefetch{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *ImagePrefetch) Default() {
	imageprefetchlog.Info("default", "name", r.Name)

	// TODO(user): fill in your defaulting logic.
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-ofen-cybozu-io-v1-imageprefetch,mutating=false,failurePolicy=fail,sideEffects=None,groups=ofen.cybozu.io,resources=imageprefetches,verbs=create;update,versions=v1,name=vimageprefetch.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &ImagePrefetch{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *ImagePrefetch) ValidateCreate() (admission.Warnings, error) {
	imageprefetchlog.Info("validate create", "name", r.Name)

	// TODO(user): fill in your validation logic upon object creation.
	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *ImagePrefetch) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	imageprefetchlog.Info("validate update", "name", r.Name)

	// TODO(user): fill in your validation logic upon object update.
	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *ImagePrefetch) ValidateDelete() (admission.Warnings, error) {
	imageprefetchlog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}

package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeImageSetSpec defines the desired state of NodeImageSet
type NodeImageSetSpec struct {
	// Images is a list of container images to be downloaded.
	Images []string `json:"images"`

	// Registry Policy is the policy for downloading images from the registry.
	RegistryPolicy RegistryPolicy `json:"registryPolicy"`

	// NodeName is the name of the node where the image is downloaded.
	NodeName string `json:"nodeName"`

	// ImagePullSecrets is a list of secret names that contain credentials for authenticating with container registries
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// ImagePrefetchGeneration is the generation of the image prefetch resource.
	// It is used to track the status of the image prefetch resource.
	// +optional
	ImagePrefetchGeneration int64 `json:"imagePrefetchGeneration,omitempty"`
}

type RegistryPolicy string

const (
	// RegistryPolicyDefault download images according to containerd host configuration.
	// If registry mirror are configured, it attempts to download images from registry mirror first,
	// then falls back to upstream registry.
	RegistryPolicyDefault RegistryPolicy = "Default"

	// RegistryPolicyMirrorOnly downloads images only from registry mirrors defined in the containerd host configuration.
	RegistryPolicyMirrorOnly RegistryPolicy = "MirrorOnly"
)

// NodeImageSetStatus defines the observed state of NodeImageSet
type NodeImageSetStatus struct {
	// The generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of an object's state
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DesiredImages is the number of images that need to be downloaded.
	// +optional
	// +kubebuilder:default:=0
	DesiredImages int `json:"desiredImages,omitempty"`

	// AvailableImages is the number of images that have completed downloading.
	// +optional
	// +kubebuilder:default:=0
	AvailableImages int `json:"availableImages,omitempty"`

	// DownloadFailedImages is the number of images that failed to download.
	// +optional
	// +kubebuilder:default:=0
	DownloadFailedImages int `json:"downloadFailedImages,omitempty"`

	// ContainerImageStatuses holds the status of each container image.
	// +optional
	ContainerImageStatuses []ContainerImageStatus `json:"containerImageStatuses,omitempty"`
}

type ContainerImageStatus struct {
	// ImageRef is the reference of the image.
	ImageRef string `json:"imageRef"`

	// Error is the error message for the image download.
	// +optional
	Error string `json:"error,omitempty"`

	// State is the state of the image download.
	// +optional
	State string `json:"lastState,omitempty"`
}

const (
	WaitingForImageDownload        = "WaitingForImageDownload"
	ImageDownloaded                = "ImageDownloaded"
	ImageDownloadInProgress        = "ImageDownloadInProgress"
	ImageDownloadFailed            = "ImageDownloadFailed"
	ImageDownloadTemporarilyFailed = "ImageDownloadTemporarilyFailed"
)

const (
	ConditionImageAvailable         = "ImageAvailable"
	ConditionImageDownloadSucceeded = "ImageDownloadSucceeded"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Desired",type="integer",JSONPath=".status.desiredImages",format="int32"
// +kubebuilder:printcolumn:name="Available",type="integer",JSONPath=".status.availableImages"
// +kubebuilder:printcolumn:name="Failed",type="integer",JSONPath=".status.downloadFailedImages"
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".spec.nodeName",priority=1

// NodeImageSet is the Schema for the nodeimagesets API
type NodeImageSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeImageSetSpec   `json:"spec,omitempty"`
	Status NodeImageSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeImageSetList contains a list of NodeImageSet
type NodeImageSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeImageSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeImageSet{}, &NodeImageSetList{})
}

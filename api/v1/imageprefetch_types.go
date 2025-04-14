package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ImagePrefetchSpec defines the desired state of ImagePrefetch
type ImagePrefetchSpec struct {
	// Images is a list of container images that will be pre-downloaded to the target nodes
	Images []string `json:"images"`

	// NodeSelector is a map of key-value pairs that specify which nodes should have the images pre-downloaded
	// +optional
	NodeSelector metav1.LabelSelector `json:"nodeSelector,omitempty"`

	// AllNodes indicates whether the images should be pre-downloaded to all nodes filtered by the NodeSelector in the cluster
	// +optional
	// +kubebuilder:default=false
	AllNodes bool `json:"allNodes,omitempty"`

	// Replicas is the number of nodes that should download the specified images
	// +optional
	Replicas int `json:"replicas,omitempty"`

	// ImagePullSecrets is a list of secret names that contain credentials for authenticating with container registries
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

// ImagePrefetchStatus defines the observed state of ImagePrefetch
type ImagePrefetchStatus struct {
	// The generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of an object's state
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DesiredNodes represents the number of nodes that should have the images pre-downloaded
	// +optional
	// +kubebuilder:default:=0
	DesiredNodes int `json:"desiredNodes,omitempty"`

	// ImagePulledNodes represents the number of nodes that have successfully pre-downloaded the images
	// +optional
	// +kubebuilder:default:=0
	ImagePulledNodes int `json:"imagePulledNodes,omitempty"`

	// ImagePullingNodes represents the number of nodes that are currently downloading the images
	// +optional
	// +kubebuilder:default:=0
	ImagePullingNodes int `json:"imagePullingNodes,omitempty"`

	// ImagePullFailedNodes represents the number of nodes that failed to download the images
	// +optional
	// +kubebuilder:default:=0
	ImagePullFailedNodes int `json:"imagePullFailedNodes,omitempty"`

	// SelectedNodes represents the nodes that have been selected to download the images
	// +optional
	SelectedNodes []string `json:"selectedNodes,omitempty"`
}

const (
	ConditionReady           = "Ready"
	ConditionProgressing     = "Progressing"
	ConditionImagePullFailed = "ImagePullFailed"
)

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ImagePrefetch is the Schema for the imageprefetches API
type ImagePrefetch struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ImagePrefetchSpec   `json:"spec,omitempty"`
	Status ImagePrefetchStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ImagePrefetchList contains a list of ImagePrefetch
type ImagePrefetchList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ImagePrefetch `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ImagePrefetch{}, &ImagePrefetchList{})
}

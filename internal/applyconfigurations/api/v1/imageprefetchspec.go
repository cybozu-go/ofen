// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1

import (
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/client-go/applyconfigurations/meta/v1"
)

// ImagePrefetchSpecApplyConfiguration represents a declarative configuration of the ImagePrefetchSpec type for use
// with apply.
type ImagePrefetchSpecApplyConfiguration struct {
	Images           []string                            `json:"images,omitempty"`
	NodeSelector     *v1.LabelSelectorApplyConfiguration `json:"nodeSelector,omitempty"`
	AllNodes         *bool                               `json:"allNodes,omitempty"`
	Replicas         *int                                `json:"replicas,omitempty"`
	ImagePullSecrets []corev1.LocalObjectReference       `json:"imagePullSecrets,omitempty"`
}

// ImagePrefetchSpecApplyConfiguration constructs a declarative configuration of the ImagePrefetchSpec type for use with
// apply.
func ImagePrefetchSpec() *ImagePrefetchSpecApplyConfiguration {
	return &ImagePrefetchSpecApplyConfiguration{}
}

// WithImages adds the given value to the Images field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the Images field.
func (b *ImagePrefetchSpecApplyConfiguration) WithImages(values ...string) *ImagePrefetchSpecApplyConfiguration {
	for i := range values {
		b.Images = append(b.Images, values[i])
	}
	return b
}

// WithNodeSelector sets the NodeSelector field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the NodeSelector field is set to the value of the last call.
func (b *ImagePrefetchSpecApplyConfiguration) WithNodeSelector(value *v1.LabelSelectorApplyConfiguration) *ImagePrefetchSpecApplyConfiguration {
	b.NodeSelector = value
	return b
}

// WithAllNodes sets the AllNodes field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the AllNodes field is set to the value of the last call.
func (b *ImagePrefetchSpecApplyConfiguration) WithAllNodes(value bool) *ImagePrefetchSpecApplyConfiguration {
	b.AllNodes = &value
	return b
}

// WithReplicas sets the Replicas field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Replicas field is set to the value of the last call.
func (b *ImagePrefetchSpecApplyConfiguration) WithReplicas(value int) *ImagePrefetchSpecApplyConfiguration {
	b.Replicas = &value
	return b
}

// WithImagePullSecrets adds the given value to the ImagePullSecrets field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the ImagePullSecrets field.
func (b *ImagePrefetchSpecApplyConfiguration) WithImagePullSecrets(values ...corev1.LocalObjectReference) *ImagePrefetchSpecApplyConfiguration {
	for i := range values {
		b.ImagePullSecrets = append(b.ImagePullSecrets, values[i])
	}
	return b
}

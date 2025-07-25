// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1

// ContainerImageStatusApplyConfiguration represents a declarative configuration of the ContainerImageStatus type for use
// with apply.
type ContainerImageStatusApplyConfiguration struct {
	ImageRef *string `json:"imageRef,omitempty"`
	Error    *string `json:"error,omitempty"`
	State    *string `json:"lastState,omitempty"`
}

// ContainerImageStatusApplyConfiguration constructs a declarative configuration of the ContainerImageStatus type for use with
// apply.
func ContainerImageStatus() *ContainerImageStatusApplyConfiguration {
	return &ContainerImageStatusApplyConfiguration{}
}

// WithImageRef sets the ImageRef field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the ImageRef field is set to the value of the last call.
func (b *ContainerImageStatusApplyConfiguration) WithImageRef(value string) *ContainerImageStatusApplyConfiguration {
	b.ImageRef = &value
	return b
}

// WithError sets the Error field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Error field is set to the value of the last call.
func (b *ContainerImageStatusApplyConfiguration) WithError(value string) *ContainerImageStatusApplyConfiguration {
	b.Error = &value
	return b
}

// WithState sets the State field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the State field is set to the value of the last call.
func (b *ContainerImageStatusApplyConfiguration) WithState(value string) *ContainerImageStatusApplyConfiguration {
	b.State = &value
	return b
}

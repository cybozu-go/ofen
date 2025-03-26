// Code generated by applyconfiguration-gen. DO NOT EDIT.

package applyconfigurations

import (
	v1 "github.com/cybozu-go/ofen/api/v1"
	apiv1 "github.com/cybozu-go/ofen/internal/applyconfigurations/api/v1"
	internal "github.com/cybozu-go/ofen/internal/applyconfigurations/internal"
	runtime "k8s.io/apimachinery/pkg/runtime"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	testing "k8s.io/client-go/testing"
)

// ForKind returns an apply configuration type for the given GroupVersionKind, or nil if no
// apply configuration type exists for the given GroupVersionKind.
func ForKind(kind schema.GroupVersionKind) interface{} {
	switch kind {
	// Group=ofen.cybozu.io, Version=v1
	case v1.SchemeGroupVersion.WithKind("NodeImageSet"):
		return &apiv1.NodeImageSetApplyConfiguration{}
	case v1.SchemeGroupVersion.WithKind("NodeImageSetSpec"):
		return &apiv1.NodeImageSetSpecApplyConfiguration{}
	case v1.SchemeGroupVersion.WithKind("NodeImageSetStatus"):
		return &apiv1.NodeImageSetStatusApplyConfiguration{}

	}
	return nil
}

func NewTypeConverter(scheme *runtime.Scheme) *testing.TypeConverter {
	return &testing.TypeConverter{Scheme: scheme, TypeResolver: internal.Parser()}
}

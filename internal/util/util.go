package util

import (
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func IsLabelSelectorEmpty(selector *metav1.LabelSelector) bool {
	if selector == nil {
		return true
	}
	emptySelector := metav1.LabelSelector{}
	return reflect.DeepEqual(*selector, emptySelector)
}

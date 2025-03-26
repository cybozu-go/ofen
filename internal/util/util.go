package util

import (
	"reflect"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func IsLabelSelectorEmpty(selector *metav1.LabelSelector) bool {
	if selector == nil {
		return true
	}
	emptySelector := metav1.LabelSelector{}
	return reflect.DeepEqual(*selector, emptySelector)
}

func IsNodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}

	return false
}

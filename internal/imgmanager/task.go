package imgmanager

import (
	corev1 "k8s.io/api/core/v1"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
)

type Task struct {
	Ref            string
	RegistryPolicy ofenv1.RegistryPolicy
	NodeImageSet   *ofenv1.NodeImageSet
	Secrets        *[]corev1.Secret
}

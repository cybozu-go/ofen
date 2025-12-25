package imgmanager

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
)

type Task struct {
	Ref            string
	RegistryPolicy ofenv1.RegistryPolicy
	NodeImageSet   *ofenv1.NodeImageSet
	Secrets        *[]corev1.Secret
}

func (t Task) String() string {
	secretNames := []string{}
	if t.Secrets != nil {
		for _, secret := range *t.Secrets {
			secretNames = append(secretNames, secret.Name)
		}
	}

	nodeImageSetName := ""
	if t.NodeImageSet != nil {
		nodeImageSetName = t.NodeImageSet.Name
	}

	return fmt.Sprintf(
		"ref=%s registryPolicy=%s secrets=%v nodeImageSet=%s",
		t.Ref, t.RegistryPolicy, secretNames, nodeImageSetName,
	)
}

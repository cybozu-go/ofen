package imgmanager

import (
	"log/slog"

	corev1 "k8s.io/api/core/v1"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
)

type Task struct {
	Ref            string
	RegistryPolicy ofenv1.RegistryPolicy
	NodeImageSet   *ofenv1.NodeImageSet
	Secrets        *[]corev1.Secret
}

func (t Task) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.String("ref", t.Ref),
		slog.String("registryPolicy", string(t.RegistryPolicy)),
	}

	if t.Secrets != nil {
		var secretNames []string
		for _, secret := range *t.Secrets {
			secretNames = append(secretNames, secret.Name)
		}
		attrs = append(attrs, slog.Any("secrets", secretNames))
	}

	if t.NodeImageSet != nil {
		attrs = append(attrs, slog.String("nodeImageSet", t.NodeImageSet.Name))
	}

	return slog.GroupValue(attrs...)
}

package imgmanager

import (
	"context"
	"sync"

	ofenv1 "github.com/cybozu-go/ofen/api/v1" // Added import
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	ctrl "sigs.k8s.io/controller-runtime/pkg/client"
)

type FakeContainerd struct {
	mu                 sync.Mutex
	pulledImages       map[string]bool
	pullErrorOverrides map[string]error
	k8sClient          ctrl.Client
	NodeName           string
}

func NewFakeContainerd(k8sClient ctrl.Client) *FakeContainerd {
	return &FakeContainerd{
		pulledImages:       make(map[string]bool),
		pullErrorOverrides: make(map[string]error),
		k8sClient:          k8sClient,
	}
}

func (f *FakeContainerd) SetNodeName(name string) {
	f.NodeName = name
}

func (f *FakeContainerd) IsImageExists(ctx context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	exists, ok := f.pulledImages[ref]
	if !ok {
		return false, nil
	}

	return exists, nil
}

// RegisterImagePullError allows registering a specific error to be returned
// when PullImage is called for a given image reference.
func (f *FakeContainerd) RegisterImagePullError(ref string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pullErrorOverrides[ref] = err
}

func (f *FakeContainerd) PullImage(ctx context.Context, ref string, policy ofenv1.RegistryPolicy) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Check for registered error overrides first
	if err, ok := f.pullErrorOverrides[ref]; ok {
		return err
	}

	f.pulledImages[ref] = true

	currentNode := &corev1.Node{}
	if err := f.k8sClient.Get(ctx, ctrl.ObjectKey{Name: f.NodeName}, currentNode); err != nil {
		return err
	}

	var updatedImageApplyConfigs []*applycorev1.ContainerImageApplyConfiguration
	if currentNode.Status.Images != nil {
		for _, img := range currentNode.Status.Images {
			imgApplyConfig := applycorev1.ContainerImage().
				WithNames(img.Names...).
				WithSizeBytes(img.SizeBytes)
			updatedImageApplyConfigs = append(updatedImageApplyConfigs, imgApplyConfig)
		}
	}

	newImageApplyConfig := applycorev1.ContainerImage().
		WithNames(ref).
		WithSizeBytes(100)
	updatedImageApplyConfigs = append(updatedImageApplyConfigs, newImageApplyConfig)
	desiredNodeApplyConfig := applycorev1.Node(f.NodeName).
		WithStatus(applycorev1.NodeStatus().
			WithImages(updatedImageApplyConfigs...))

	fieldManager := "fake-containerd"
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(desiredNodeApplyConfig)
	if err != nil {
		return err
	}
	patch := &unstructured.Unstructured{
		Object: obj,
	}

	return f.k8sClient.Status().Patch(ctx, patch, ctrl.Apply, ctrl.ForceOwnership,
		ctrl.FieldOwner(
			fieldManager,
		))
}

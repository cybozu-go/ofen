package v1

import (
	"context"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func makeImagePrefetch(name, namespace string) *ofenv1.ImagePrefetch {
	return &ofenv1.ImagePrefetch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

func deleteImagePrefetch(ctx context.Context, namespace string) {
	ipList := &ofenv1.ImagePrefetchList{}
	k8sClient.List(ctx, ipList, client.InNamespace(namespace))
	for i := range ipList.Items {
		ip := ipList.Items[i].DeepCopy()

		// Remove finalizer
		ip.Finalizers = nil
		Expect(k8sClient.Update(ctx, ip)).To(Succeed())

		Expect(k8sClient.Delete(ctx, ip)).To(Succeed())
		nsn := types.NamespacedName{
			Name:      ip.Name,
			Namespace: ip.Namespace,
		}
		Eventually(func() bool {
			err := k8sClient.Get(ctx, nsn, &ofenv1.ImagePrefetch{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
	}
}

var _ = Describe("ImagePrefetch Webhook", func() {
	name := "test-imageprefetch"
	namespace := "default"
	ctx := context.Background()

	AfterEach(func() {
		deleteImagePrefetch(ctx, namespace)
	})

	It("should allow creating ImagePrefetch with replicas", func() {
		ip := makeImagePrefetch(name, namespace)
		ip.Spec.Replicas = 2
		ip.Spec.Images = []string{"nginx:latest"}
		Expect(k8sClient.Create(ctx, ip)).To(Succeed())

		By("checking default values")
		Expect(ip.ObjectMeta.Finalizers).To(HaveLen(1))
		Expect(ip.ObjectMeta.Finalizers[0]).To(Equal(constants.ImagePrefetchFinalizer))
		Expect(ip.Spec.Replicas).To(BeNumerically("==", 2))
	})

	It("should allow creating ImagePrefetch with nodeSelector", func() {
		ip := makeImagePrefetch(name, namespace)
		ip.Spec.NodeSelector = metav1.LabelSelector{
			MatchLabels: map[string]string{
				"kubernetes.io/hostname": "test-1",
			},
		}
		ip.Spec.Images = []string{"nginx:latest"}
		Expect(k8sClient.Create(ctx, ip)).To(Succeed())

		By("checking default values")
		Expect(ip.ObjectMeta.Finalizers).To(HaveLen(1))
		Expect(ip.ObjectMeta.Finalizers[0]).To(Equal(constants.ImagePrefetchFinalizer))
		Expect(ip.Spec.NodeSelector.MatchLabels).To(HaveLen(1))
	})

})

package controller

import (
	"context"
	"time"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func testNodeImageSet(name string) *ofenv1.NodeImageSet {
	return &ofenv1.NodeImageSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: ofenv1.NodeImageSetSpec{
			NodeName: "test-node",
			Images: []string{
				"test-image",
			},
		},
	}
}

func createNode(ctx context.Context, name string, labels map[string]string) {
	node := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionTrue,
				},
			},
			Images: []corev1.ContainerImage{},
		},
	}
	err := k8sClient.Create(ctx, &node)
	Expect(err).NotTo(HaveOccurred())
}

var _ = Describe("NodeImageSet Controller", func() {
	Context("When reconciling a resource", func() {

		ctx := context.Background()
		var stopFunc func()

		BeforeEach(func() {
			mgr, err := ctrl.NewManager(cfg, ctrl.Options{
				Scheme:         scheme.Scheme,
				LeaderElection: false,
				Metrics: metricsserver.Options{
					BindAddress: "0",
				},
				Controller: config.Controller{
					SkipNameValidation: ptr.To(true),
				},
			})
			Expect(err).NotTo(HaveOccurred())

			reconciler := &NodeImageSetReconciler{
				Client:   mgr.GetClient(),
				Scheme:   scheme.Scheme,
				NodeName: "test-node",
			}
			err = reconciler.SetupWithManager(mgr)
			Expect(err).NotTo(HaveOccurred())

			ctx, cancel := context.WithCancel(ctx)
			stopFunc = cancel
			go func() {
				err = mgr.Start(ctx)
				if err != nil {
					panic(err)
				}
			}()
			time.Sleep(100 * time.Millisecond)

			createNode(ctx, "test-node", map[string]string{
				"kubernetes.io/hostname": "test-node",
			})
		})

		AfterEach(func() {
			stopFunc()
			time.Sleep(100 * time.Millisecond)
		})

		It("should reconcile NodeImageSet", func() {
			testName := "test-nodeimageset"
			nodeImageSet := testNodeImageSet(testName)
			err := k8sClient.Create(ctx, nodeImageSet)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				nodeImageSet := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName}, nodeImageSet)
				g.Expect(err).NotTo(HaveOccurred())

				conditionImageAvailable := meta.FindStatusCondition(nodeImageSet.Status.Conditions, ofenv1.ConditionImageAvailable)
				g.Expect(conditionImageAvailable).NotTo(BeNil())
				g.Expect(conditionImageAvailable.Status).To(Equal(metav1.ConditionFalse))
			}).Should(Succeed())
		})
	})
})

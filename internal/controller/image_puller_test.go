package controller

import (
	"context"
	"fmt"

	ofenv1 "github.com/cybozu-go/ofen/api/v1" // Add this import
	"github.com/cybozu-go/ofen/internal/imgmanager"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

var _ = Describe("ImagePuller", func() {
	ctx := context.Background()
	var imagePuller *imagePuller
	var fakeContainerdClient *imgmanager.FakeContainerd
	nodeName := "imagepuller-test-node"

	Context("When pulling an image", func() {
		BeforeEach(func() {
			fakeContainerdClient = imgmanager.NewFakeContainerd(k8sClient)
			fakeContainerdClient.SetNodeName(nodeName)
			log := ctrl.Log.WithName("controllers").WithName("NodeImageSet")
			ch := make(chan event.TypedGenericEvent[*ofenv1.NodeImageSet])
			imagePuller = NewImagePuller(log, fakeContainerdClient, k8sClient, ch)

			// Create a test node
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
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
			err := k8sClient.Create(ctx, node)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			imagePuller.StopAll()
			err := deleteNode(ctx, nodeName)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should pull the image successfully", func() {
			testName := "test-image-puller"
			image := fmt.Sprintf("%s:latest", testName)

			By("creating a NodeImageSet")
			nis := createNodeImageSet(testName).
				withNodeName(nodeName).
				withImages([]string{image}).
				withRegistryPolicy(ofenv1.RegistryPolicyDefault).
				build()

			By("starting the image pull process")
			clientset, err := kubernetes.NewForConfig(cfg)
			Expect(err).NotTo(HaveOccurred())
			eventBroadcaster := record.NewBroadcaster()
			eventBroadcaster.StartLogging(klog.Infof)
			eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{
				Interface: clientset.CoreV1().Events(""),
			})
			recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{})
			secrets := []corev1.Secret{}
			err = imagePuller.start(ctx, nis, secrets, recorder, false)
			Expect(err).NotTo(HaveOccurred())

			By("checking image is pulled")
			Eventually(func(g Gomega) {
				{
					exists, err := fakeContainerdClient.IsImageExists(ctx, image)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(exists).To(BeTrue())
				}
			}).Should(Succeed())
		})
	})
})

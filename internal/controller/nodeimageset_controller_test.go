package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/containerd/containerd/errdefs"
	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
	"github.com/cybozu-go/ofen/internal/imgmanager"
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

type nodeImageSetBuilder struct {
	object *ofenv1.NodeImageSet
}

func createNodeImageSet(name string) *nodeImageSetBuilder {
	return &nodeImageSetBuilder{
		object: &ofenv1.NodeImageSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

func (b *nodeImageSetBuilder) withNodeName(nodeName string) *nodeImageSetBuilder {
	b.object.Spec.NodeName = nodeName
	return b
}

func (b *nodeImageSetBuilder) withImages(images []string) *nodeImageSetBuilder {
	b.object.Spec.Images = images
	return b
}

func (b *nodeImageSetBuilder) withRegistryPolicy(policy ofenv1.RegistryPolicy) *nodeImageSetBuilder {
	b.object.Spec.RegistryPolicy = policy
	return b
}

func (b *nodeImageSetBuilder) WithLabels(labels map[string]string) *nodeImageSetBuilder {
	b.object.Labels = labels
	return b
}

func (b *nodeImageSetBuilder) build() *ofenv1.NodeImageSet {
	return b.object
}

var _ = Describe("NodeImageSet Controller", Serial, func() {
	Context("When reconciling a resource", func() {
		ctx := context.Background()
		var stopFunc func()
		nodeName := "nodeimageset-test-node"
		var fakeContainerdClient *imgmanager.FakeContainerd

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

			fakeContainerdClient = imgmanager.NewFakeContainerd(mgr.GetClient())
			fakeContainerdClient.SetNodeName(nodeName)
			log := ctrl.Log.WithName("controllers").WithName("NodeImageSet")
			imagePuller := NewImagePuller(log, fakeContainerdClient)

			reconciler := &NodeImageSetReconciler{
				Client:      mgr.GetClient(),
				Scheme:      scheme.Scheme,
				NodeName:    nodeName,
				ImagePuller: imagePuller,
				Recorder:    mgr.GetEventRecorderFor("nodeimageset-controller"),
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

			// Create a test node
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
					Labels: map[string]string{
						"kubernetes.io/hostname": nodeName,
					},
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

			_, err = ctrl.CreateOrUpdate(ctx, k8sClient, node, func() error {
				return nil
			})
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			stopFunc()
			time.Sleep(100 * time.Millisecond)
		})

		It("should pull images as specified in the NodeImageSet", func() {
			testName := "test-nodeimageset"
			By("creating a NodeImageSet resource")
			nodeImageSet := createNodeImageSet(testName).
				WithLabels(map[string]string{
					constants.NodeName: nodeName,
				}).
				withNodeName(nodeName).
				withImages([]string{fmt.Sprintf("test/%s:latest", testName)}).
				build()
			err := k8sClient.Create(ctx, nodeImageSet)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				nodeImageSet := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName}, nodeImageSet)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSet.Status.DesiredImages).To(Equal(1))
				g.Expect(nodeImageSet.Status.AvailableImages).To(Equal(1))

				node := &corev1.Node{}
				err = k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(node.Status.Images).To(HaveLen(1))
			}).Should(Succeed())

			By("cleaning up the NodeImageSet resource")
			deleteNodeImageSet(ctx, testName)
		})

		It("should delete the NodeImageSet resource when its associated Node is deleted", func() {
			testName := "should-delete-nodeimageset"
			By("creating a NodeImageSet resource")
			nodeImageSet := createNodeImageSet(testName).
				WithLabels(map[string]string{
					constants.NodeName: nodeName,
				}).
				withNodeName(nodeName).
				withImages([]string{fmt.Sprintf("test/%s:latest", testName)}).
				build()
			err := k8sClient.Create(ctx, nodeImageSet)
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				nodeImageSet := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName}, nodeImageSet)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSet.Status.DesiredImages).To(Equal(1))
				g.Expect(nodeImageSet.Status.AvailableImages).To(Equal(1))
			}).Should(Succeed())

			By("deleting the Node")
			node := &corev1.Node{}
			err = k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node)
			Expect(err).NotTo(HaveOccurred())
			err = k8sClient.Delete(ctx, node)
			Expect(err).NotTo(HaveOccurred())

			By("checking the NodeImageSet resource is deleted")
			Eventually(func(g Gomega) {
				nodeImageSet := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName}, nodeImageSet)
				g.Expect(err).To(HaveOccurred())
			}).Should(Succeed())
		})

		It("should reconcile the NodeImageSet when the count of images on the Node changes", func() {
			testName := "reconcile-on-node-image-num-change"
			image1 := fmt.Sprintf("test/%s-image1:latest", testName)
			image2 := fmt.Sprintf("test/%s-image2:latest", testName)

			By("creating a NodeImageSet resource with two images")
			nodeImageSet := createNodeImageSet(testName).
				WithLabels(map[string]string{
					constants.NodeName: nodeName,
				}).
				withNodeName(nodeName).
				withImages([]string{image1, image2}).
				build()
			err := k8sClient.Create(ctx, nodeImageSet)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for initial reconciliation and for both images to be available")
			Eventually(func(g Gomega) {
				currentNis := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName}, currentNis)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(currentNis.Status.DesiredImages).To(Equal(2))
				g.Expect(currentNis.Status.AvailableImages).To(Equal(2))

				node := &corev1.Node{}
				err = k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(node.Status.Images).To(HaveLen(2))
			}).Should(Succeed())

			By("manually updating the Node's status to reflect only one image present")
			node := &corev1.Node{}
			err = k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node)
			Expect(err).NotTo(HaveOccurred())

			node.Status.Images = []corev1.ContainerImage{
				{Names: []string{image1}, SizeBytes: 1024 * 1024},
			}
			err = k8sClient.Status().Update(ctx, node)
			Expect(err).NotTo(HaveOccurred())

			By("checking the NodeImageSet status is updated to reflect one available image")
			Eventually(func(g Gomega) {
				updatedNis := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName}, updatedNis)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(updatedNis.Status.DesiredImages).To(Equal(2))
				g.Expect(updatedNis.Status.AvailableImages).To(Equal(1))
			}).Should(Succeed())

			By("cleaning up the NodeImageSet resource")
			deleteNodeImageSet(ctx, testName)
		})

		It("should not reconcile NodeImageSets intended for other nodes", func() {
			testName := "no-reconcile-on-node-name-mismatch"
			image := fmt.Sprintf("test/%s-image:latest", testName)

			By("creating a NodeImageSet resource for a different node")
			nodeImageSet := createNodeImageSet(testName).
				WithLabels(map[string]string{
					// The constants.NodeName label is used by the controller's watch on Node objects
					// to enqueue relevant NodeImageSets if a Node changes.
					constants.NodeName: "other-node",
				}).
				// The spec.NodeName field is used by the Reconcile method to ensure
				// this controller instance only processes NodeImageSets for its assigned node.
				withNodeName("other-node").
				withImages([]string{image}).
				build()
			err := k8sClient.Create(ctx, nodeImageSet)
			Expect(err).NotTo(HaveOccurred())

			By("checking that the NodeImageSet status remains unchanged by this controller")
			Eventually(func(g Gomega) {
				currentNis := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName}, currentNis)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(currentNis.Status.DesiredImages).To(Equal(0))
				g.Expect(currentNis.Status.AvailableImages).To(Equal(0))
			}).Should(Succeed())

			By("cleaning up the NodeImageSet resource")
			deleteNodeImageSet(ctx, testName)
		})

		It("should update NodeImageSet status based on the actual image availability on the node", func() {
			testName := "update-status-on-node-status-change"
			image := fmt.Sprintf("test/%s-image:latest", testName)
			By("creating a NodeImageSet resource with one image")
			nodeImageSet := createNodeImageSet(testName).
				WithLabels(map[string]string{
					constants.NodeName: nodeName,
				}).
				withNodeName(nodeName).
				withImages([]string{image}).
				build()
			err := k8sClient.Create(ctx, nodeImageSet)
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				currentNis := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName}, currentNis)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(currentNis.Status.DesiredImages).To(Equal(1))
				g.Expect(currentNis.Status.AvailableImages).To(Equal(1))

				conditionAvailable := meta.FindStatusCondition(currentNis.Status.Conditions, ofenv1.ConditionImageAvailable)
				g.Expect(conditionAvailable).NotTo(BeNil())
				g.Expect(conditionAvailable.Status).To(Equal(metav1.ConditionTrue))
				conditionDownloadComplete := meta.FindStatusCondition(currentNis.Status.Conditions, ofenv1.ConditionImageDownloadComplete)
				g.Expect(conditionDownloadComplete).NotTo(BeNil())
				g.Expect(conditionDownloadComplete.Status).To(Equal(metav1.ConditionTrue))
				conditionDownloadFailed := meta.FindStatusCondition(currentNis.Status.Conditions, ofenv1.ConditionImageDownloadFailed)
				g.Expect(conditionDownloadFailed).NotTo(BeNil())
				g.Expect(conditionDownloadFailed.Status).To(Equal(metav1.ConditionFalse))
			}).Should(Succeed())

			By("cleaning up the NodeImageSet resource")
			deleteNodeImageSet(ctx, testName)
		})

		It("should increment DownloadFailedImages when an image pull fails", func() {
			testName := "update-status-on-image-pull-fail"
			image := fmt.Sprintf("test/%s:latest", testName)
			fakeContainerdClient.RegisterImagePullError(image, errdefs.ErrUnavailable)

			By("creating a NodeImageSet resource with one image")
			nodeImageSet := createNodeImageSet(testName).
				WithLabels(map[string]string{
					constants.NodeName: nodeName,
				}).
				withNodeName(nodeName).
				withImages([]string{image}).
				build()
			err := k8sClient.Create(ctx, nodeImageSet)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				currentNis := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName}, currentNis)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(currentNis.Status.DesiredImages).To(Equal(1))
				g.Expect(currentNis.Status.AvailableImages).To(Equal(0))
				g.Expect(currentNis.Status.DownloadFailedImages).To(Equal(1))
			}).Should(Succeed())

			By("cleaning up the NodeImageSet resource")
			deleteNodeImageSet(ctx, testName)
		})

		It("should increment DownloadFailedImages when an image is not found and RegistryPolicy is not MirrorOnly", func() {
			testName := "update-status-on-image-pull-not-found"
			image1 := fmt.Sprintf("test/%s:latest", testName)
			image2 := fmt.Sprintf("test/%s:not-found-image", testName)
			fakeContainerdClient.RegisterImagePullError(image2, errdefs.ErrNotFound)

			By("creating a NodeImageSet resource with two images")
			nodeImageSet := createNodeImageSet(testName).
				WithLabels(map[string]string{
					constants.NodeName: nodeName,
				}).
				withNodeName(nodeName).
				withImages([]string{image1, image2}).
				build()
			err := k8sClient.Create(ctx, nodeImageSet)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				currentNis := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName}, currentNis)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(currentNis.Status.DesiredImages).To(Equal(2))
				g.Expect(currentNis.Status.AvailableImages).To(Equal(1))
				g.Expect(currentNis.Status.DownloadFailedImages).To(Equal(1))
			}).Should(Succeed())

			By("cleaning up the NodeImageSet resource")
			deleteNodeImageSet(ctx, testName)
		})

		It("should not increment DownloadFailedImages for a not-found error when RegistryPolicy is MirrorOnly", func() {
			testName := "update-status-on-image-pull-not-found-mirror-only"
			image1 := fmt.Sprintf("test/%s:latest", testName) // Assume this image pulls successfully
			image2 := fmt.Sprintf("test/%s:not-found-image", testName)
			fakeContainerdClient.RegisterImagePullError(image2, errdefs.ErrNotFound)

			By("creating a NodeImageSet resource with two images and MirrorOnly policy")
			nodeImageSet := createNodeImageSet(testName).
				WithLabels(map[string]string{
					constants.NodeName: nodeName,
				}).
				withNodeName(nodeName).
				withImages([]string{image1, image2}).
				withRegistryPolicy(ofenv1.RegistryPolicyMirrorOnly).
				build()
			err := k8sClient.Create(ctx, nodeImageSet)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				currentNis := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName}, currentNis)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(currentNis.Status.DesiredImages).To(Equal(2))
				g.Expect(currentNis.Status.AvailableImages).To(Equal(1))      // image1 is available
				g.Expect(currentNis.Status.DownloadFailedImages).To(Equal(0)) // image2 not found with MirrorOnly should not count as failed
			}).Should(Succeed())

			By("cleaning up the NodeImageSet resource")
			deleteNodeImageSet(ctx, testName)
		})
	})
})

func deleteNodeImageSet(ctx context.Context, name string) {
	nodeImageSet := &ofenv1.NodeImageSet{}
	err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, nodeImageSet)
	Expect(err).NotTo(HaveOccurred())

	err = k8sClient.Delete(ctx, nodeImageSet)
	Expect(err).NotTo(HaveOccurred())

	Eventually(func(g Gomega) {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, nodeImageSet)
		g.Expect(err).To(HaveOccurred())
	}).Should(Succeed())
}

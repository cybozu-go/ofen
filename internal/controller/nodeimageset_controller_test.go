package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/containerd/errdefs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
	"github.com/cybozu-go/ofen/internal/imgmanager"
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

func deleteNode(ctx context.Context, name string) error {
	node := &corev1.Node{}
	err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, node)
	if apierrors.IsNotFound(err) {
		return nil // Node already deleted
	} else if err != nil {
		return err
	}

	return k8sClient.Delete(ctx, node)
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
			ch := make(chan event.TypedGenericEvent[*ofenv1.NodeImageSet])

			imagePuller := imgmanager.NewImagePuller(ctrl.Log.WithName("test-imagepuller"), fakeContainerdClient)
			queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[imgmanager.Task]())

			reconciler := &NodeImageSetReconciler{
				Client:           mgr.GetClient(),
				Scheme:           scheme.Scheme,
				NodeName:         nodeName,
				ImagePuller:      imagePuller,
				ContainerdClient: fakeContainerdClient,
				Recorder:         mgr.GetEventRecorderFor("nodeimageset-controller"),
				Queue:            queue,
			}
			err = reconciler.SetupWithManager(mgr, ch)
			Expect(err).NotTo(HaveOccurred())

			// Create and start runner
			runner := NewRunner(imagePuller, ctrl.Log.WithName("test-runner"), queue, mgr.GetEventRecorderFor("test-runner"))
			err = mgr.Add(runner)
			Expect(err).NotTo(HaveOccurred())

			eventWatcher := NewContainerdEventWatcher(mgr.GetClient(), fakeContainerdClient, imagePuller, ctrl.Log.WithName("test-event-watcher"), nodeName, ch)
			err = mgr.Add(eventWatcher)
			Expect(err).NotTo(HaveOccurred())

			ctx, cancel := context.WithCancel(ctx)
			stopFunc = cancel
			go func() {
				err = mgr.Start(ctx)
				if err != nil {
					panic(err)
				}
			}()

			go func() {
				<-ctx.Done()
				queue.ShutDown()
			}()
			time.Sleep(100 * time.Millisecond)
		})

		AfterEach(func() {
			stopFunc()
			time.Sleep(100 * time.Millisecond)
		})

		It("should pull images as specified in the NodeImageSet", func() {
			testName := "test-nodeimageset"
			image := fmt.Sprintf("test/%s/image:latest", testName)

			By("creating a NodeImageSet resource")
			nodeImageSet := createNodeImageSet(testName).
				WithLabels(map[string]string{
					constants.NodeName: nodeName,
				}).
				withNodeName(nodeName).
				withImages([]string{image}).
				withRegistryPolicy(ofenv1.RegistryPolicyDefault).
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

			By("cleaning up the NodeImageSet resource")
			deleteNodeImageSet(ctx, testName)
		})

		It("should reconcile the NodeImageSet when the image is deleted", func() {
			testName := "reconcile-on-image-deletion"
			image1 := fmt.Sprintf("test/%s/image1:latest", testName)
			image2 := fmt.Sprintf("test/%s/image2:latest", testName)
			fakeContainerdClient.SetPullDelay(3 * time.Second)

			By("creating a NodeImageSet resource with two images")
			nodeImageSet := createNodeImageSet(testName).
				WithLabels(map[string]string{
					constants.NodeName: nodeName,
				}).
				withNodeName(nodeName).
				withImages([]string{image1, image2}).
				withRegistryPolicy(ofenv1.RegistryPolicyDefault).
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
			}).Should(Succeed())

			By("deleting one of the images from the NodeImageSet")
			delteEvent, err := imgmanager.CreateImageDeleteEvent(image1)
			Expect(err).NotTo(HaveOccurred())
			err = fakeContainerdClient.SendTestEvent(delteEvent)
			Expect(err).NotTo(HaveOccurred())

			By("checking that the NodeImageSet status reflects the deletion")
			Eventually(func(g Gomega) {
				currentNis := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName}, currentNis)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(currentNis.Status.DesiredImages).To(Equal(2))
				g.Expect(currentNis.Status.AvailableImages).To(Equal(1))
			}).Should(Succeed())

			By("cleaning up the NodeImageSet resource")
			deleteNodeImageSet(ctx, testName)
		})

		It("should not reconcile NodeImageSets intended for other nodes", func() {
			testName := "no-reconcile-on-node-name-mismatch"
			image := fmt.Sprintf("test/%s/image:latest", testName)

			By("creating a NodeImageSet resource for a different node")
			nodeImageSet := createNodeImageSet(testName).
				WithLabels(map[string]string{
					constants.NodeName: "other-node",
				}).
				withNodeName("other-node").
				withImages([]string{image}).
				withRegistryPolicy(ofenv1.RegistryPolicyDefault).
				build()
			err := k8sClient.Create(ctx, nodeImageSet)
			Expect(err).NotTo(HaveOccurred())

			By("checking that the NodeImageSet status remains unchanged by this controller")
			Eventually(func(g Gomega) {
				currentNis := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName}, currentNis)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(currentNis.Status.ObservedGeneration).To(Equal(int64(0)))
				g.Expect(currentNis.Status.DesiredImages).To(Equal(0))
				g.Expect(currentNis.Status.AvailableImages).To(Equal(0))
			}).Should(Succeed())

			By("cleaning up the NodeImageSet resource")
			deleteNodeImageSet(ctx, testName)
		})

		It("should update NodeImageSet status based on the actual image availability on the node", func() {
			testName := "update-status-on-node-status-change"
			image := fmt.Sprintf("test/%s/image:latest", testName)
			By("creating a NodeImageSet resource with one image")
			nodeImageSet := createNodeImageSet(testName).
				WithLabels(map[string]string{
					constants.NodeName: nodeName,
				}).
				withNodeName(nodeName).
				withImages([]string{image}).
				withRegistryPolicy(ofenv1.RegistryPolicyDefault).
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
				conditionDownloadSucceeded := meta.FindStatusCondition(currentNis.Status.Conditions, ofenv1.ConditionImageDownloadSucceeded)
				g.Expect(conditionDownloadSucceeded).NotTo(BeNil())
				g.Expect(conditionDownloadSucceeded.Status).To(Equal(metav1.ConditionTrue))
			}).Should(Succeed())

			By("cleaning up the NodeImageSet resource")
			deleteNodeImageSet(ctx, testName)
		})

		It("should increment DownloadFailedImages when an image pull fails", func() {
			testName := "update-status-on-image-pull-fail"
			image := fmt.Sprintf("test/%s/image:latest", testName)
			fakeContainerdClient.RegisterImagePullError(image, errdefs.ErrUnavailable)

			By("creating a NodeImageSet resource with one image")
			nodeImageSet := createNodeImageSet(testName).
				WithLabels(map[string]string{
					constants.NodeName: nodeName,
				}).
				withNodeName(nodeName).
				withImages([]string{image}).
				withRegistryPolicy(ofenv1.RegistryPolicyDefault).
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
			image1 := fmt.Sprintf("test/%s/image1:latest", testName)
			image2 := fmt.Sprintf("test/%s/image2:not-found", testName)
			fakeContainerdClient.RegisterImagePullError(image2, errdefs.ErrNotFound)

			By("creating a NodeImageSet resource with two images")
			nodeImageSet := createNodeImageSet(testName).
				WithLabels(map[string]string{
					constants.NodeName: nodeName,
				}).
				withNodeName(nodeName).
				withImages([]string{image1, image2}).
				withRegistryPolicy(ofenv1.RegistryPolicyDefault).
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
			image1 := fmt.Sprintf("test/%s/image1:latest", testName) // Assume this image pulls successfully
			image2 := fmt.Sprintf("test/%s/image2:not-found", testName)
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

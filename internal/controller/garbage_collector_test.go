package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
)

var _ = Describe("NodeImageSet Garbage Collector", Serial, func() {
	Context("removeStaleNodeImageSets", func() {
		var stopFunc func()
		nodeName := "gc-test-node"
		images := []string{"image1", "image2"}
		ctx := context.Background()

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

			garbageCollector := NewGarbageCollector(mgr.GetClient(), ctrl.Log.WithName("test-gc"), 1*time.Second)
			err = mgr.Add(garbageCollector)
			Expect(err).NotTo(HaveOccurred())

			ctx, cancel := context.WithCancel(context.Background())
			stopFunc = cancel

			go func() {
				err = mgr.Start(ctx)
				if err != nil {
					panic(err)
				}
			}()
			time.Sleep(100 * time.Millisecond)
		})

		AfterEach(func() {
			time.Sleep(100 * time.Millisecond)
			stopFunc()
		})

		It("should remove stale NodeImageSets", func() {
			node := createNode(nodeName).Build()
			err := k8sClient.Create(ctx, node)
			Expect(err).NotTo(HaveOccurred())

			nis := createNodeImageSet("gc-test-node").
				WithLabels(map[string]string{
					constants.NodeName: nodeName,
				}).
				withNodeName(nodeName).
				withImages(images).
				withRegistryPolicy(ofenv1.RegistryPolicyDefault).
				WithFinalizers([]string{constants.NodeImageSetFinalizer}).
				build()
			err = k8sClient.Create(ctx, nis)
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				n := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: nis.Name}, n)
				g.Expect(err).NotTo(HaveOccurred())
			}).Should(Succeed())

			err = k8sClient.Delete(ctx, node)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				n := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: nis.Name}, n)
				g.Expect(err).To(HaveOccurred())
			}).Should(Succeed())
		})

		It("should not remove NodeImageSets for existing nodes", func() {
			existingNodeName := "gc-test-existing-node"
			node := createNode(existingNodeName).Build()
			err := k8sClient.Create(ctx, node)
			Expect(err).NotTo(HaveOccurred())

			nis := createNodeImageSet("gc-test-existing-node").
				WithLabels(map[string]string{
					constants.NodeName: existingNodeName,
				}).
				withNodeName(existingNodeName).
				withImages(images).
				withRegistryPolicy(ofenv1.RegistryPolicyDefault).
				WithFinalizers([]string{constants.NodeImageSetFinalizer}).
				build()
			err = k8sClient.Create(ctx, nis)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				n := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: nis.Name}, n)
				g.Expect(err).NotTo(HaveOccurred())
			}).Should(Succeed())

			// Wait for a while to ensure GC had time to run
			time.Sleep(2 * time.Second)

			Consistently(func(g Gomega) {
				n := &ofenv1.NodeImageSet{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: nis.Name}, n)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(n.Name).To(Equal(nis.Name))
			}).Should(Succeed())
		})
	})
})

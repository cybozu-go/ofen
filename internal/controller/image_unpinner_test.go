package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
	"github.com/cybozu-go/ofen/internal/imgmanager"
)

var _ = Describe("ImageUnpinner", func() {
	var (
		fakeContainerdClient *imgmanager.FakeContainerd
		unpinner             *ImageUnpinner
		nodeName             string
	)
	ctx := context.Background()

	BeforeEach(func() {
		nodeName = fmt.Sprintf("unpinner-test-node-%d", time.Now().UnixNano())
		fakeContainerdClient = imgmanager.NewFakeContainerd(k8sClient)
		log := logf.Log.WithName("unpinner_test")
		unpinner = NewImageUnpinner(k8sClient, fakeContainerdClient, log, nodeName, time.Second)
	})

	AfterEach(func() {
		nis := &ofenv1.NodeImageSet{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		err := k8sClient.Delete(ctx, nis)
		if err != nil && !apierrors.IsNotFound(err) {
			Expect(err).NotTo(HaveOccurred())
		}
	})

	createNodeImageSet := func(images []string) {
		nis := &ofenv1.NodeImageSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nodeName,
				Labels: map[string]string{constants.NodeName: nodeName},
			},
			Spec: ofenv1.NodeImageSetSpec{
				NodeName: nodeName,
				Images:   images,
			},
		}
		Expect(k8sClient.Create(ctx, nis)).To(Succeed())
	}

	It("unpins an image once it is in use by a container", func() {
		const image = "registry.example.com/in-use:latest"
		createNodeImageSet([]string{image})
		_, err := fakeContainerdClient.PullImage(ctx, image, ofenv1.RegistryPolicyDefault, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeContainerdClient.IsImagePinned(image)).To(BeTrue())

		By("reconciling while the image is not yet used: it stays pinned")
		Expect(unpinner.reconcilePins(ctx)).To(Succeed())
		Expect(fakeContainerdClient.IsImagePinned(image)).To(BeTrue())

		By("reconciling after the image is in use: it gets unpinned")
		fakeContainerdClient.SetImageInUse(image, true)
		Expect(unpinner.reconcilePins(ctx)).To(Succeed())
		Expect(fakeContainerdClient.IsImagePinned(image)).To(BeFalse())
	})

	It("unpins an image that is no longer desired by any NodeImageSet", func() {
		const image = "registry.example.com/orphaned:latest"
		// Image was pinned by a previous prefetch, but no NodeImageSet desires it now.
		_, err := fakeContainerdClient.PullImage(ctx, image, ofenv1.RegistryPolicyDefault, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeContainerdClient.IsImagePinned(image)).To(BeTrue())

		Expect(unpinner.reconcilePins(ctx)).To(Succeed())
		Expect(fakeContainerdClient.IsImagePinned(image)).To(BeFalse())
	})

	It("keeps a desired, unused image pinned", func() {
		const image = "registry.example.com/waiting:latest"
		createNodeImageSet([]string{image})
		_, err := fakeContainerdClient.PullImage(ctx, image, ofenv1.RegistryPolicyDefault, nil)
		Expect(err).NotTo(HaveOccurred())

		Expect(unpinner.reconcilePins(ctx)).To(Succeed())
		Expect(fakeContainerdClient.IsImagePinned(image)).To(BeTrue())
	})
})

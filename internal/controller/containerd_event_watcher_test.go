package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/typeurl/v2"
	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/imgmanager"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

var _ = Describe("ContainerdEventWatcher", func() {
	var (
		fakeContainerdClient *imgmanager.FakeContainerd
		eventChannel         chan string
		nodeName             string
		wg                   sync.WaitGroup
		stopFunc             func()
	)
	ctx := context.Background()

	BeforeEach(func() {
		ctx, cancel := context.WithCancel(ctx)
		stopFunc = cancel
		nodeName = fmt.Sprintf("event-watcher-test-node-%d", time.Now().UnixNano())
		fakeContainerdClient = imgmanager.NewFakeContainerd(k8sClient)
		fakeContainerdClient.SetNodeName(nodeName)
		log := logf.Log.WithName("eventwatcher_test")
		eventChannel = make(chan string, 5)

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

		nis := &ofenv1.NodeImageSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName,
			},
			Spec: ofenv1.NodeImageSetSpec{
				NodeName: nodeName,
				Images:   []string{},
			},
		}
		err = k8sClient.Create(ctx, nis)
		Expect(err).NotTo(HaveOccurred())

		eventWatcher := NewContainerdEventWatcher(fakeContainerdClient, log, eventChannel, []string{})
		wg.Add(1)
		go func() {
			defer GinkgoRecover()
			eventWatcher.Start(ctx)
			wg.Done()
		}()
		time.Sleep(200 * time.Millisecond)
	})

	AfterEach(func() {
		nis := &ofenv1.NodeImageSet{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		err := k8sClient.Delete(ctx, nis)
		if err != nil && !apierrors.IsNotFound(err) {
			Expect(err).NotTo(HaveOccurred())
		}

		err = deleteNode(ctx, nodeName)
		Expect(err).NotTo(HaveOccurred())

		for len(eventChannel) > 0 {
			<-eventChannel
		}

		stopFunc()
		wg.Wait()
	})

	Context("when an image delete event occurs for an image on the node", func() {
		It("should send a GenericEvent to the event channel for the node's NodeImageSet", func() {
			imageName := "test-image-deleted:latest"

			By("creating and sending an image delete event")
			imgDeleteEvent := &eventtypes.ImageDelete{Name: imageName}
			anyEvent, err := typeurl.MarshalAny(imgDeleteEvent)
			Expect(err).NotTo(HaveOccurred())
			containerdEvent := &events.Envelope{
				Timestamp: time.Now(),
				Namespace: "k8s.io",
				Topic:     "/images/delete",
				Event:     anyEvent,
			}
			err = fakeContainerdClient.SendTestEvent(containerdEvent)
			Expect(err).NotTo(HaveOccurred())

			By("checking for the event on the channel")
			var receivedEvent string
			Eventually(eventChannel, "5s", "100ms").Should(Receive(&receivedEvent), "Should receive an event on the channel")

			Expect(receivedEvent).NotTo(BeNil(), "Received event object should not be nil")
			Expect(receivedEvent).To(Equal(imageName), "Event should be for the NodeImageSet matching the node name")
		})

		It("should not send an event for non-image-delete topics", func() {
			By("creating and sending a non-image delete event")
			taskCreateEvent := &eventtypes.TaskCreate{ContainerID: "test-container"}
			anyEvent, err := typeurl.MarshalAny(taskCreateEvent)
			Expect(err).NotTo(HaveOccurred())
			containerdEvent := &events.Envelope{
				Timestamp: time.Now(),
				Namespace: "k8s.io",
				Topic:     "/tasks/create", // A different topic
				Event:     anyEvent,
			}

			err = fakeContainerdClient.SendTestEvent(containerdEvent)
			Expect(err).NotTo(HaveOccurred())

			By("checking that no event is sent to the channel")
			Consistently(eventChannel, "2s", "100ms").ShouldNot(Receive(), "Should not receive an event for non-image-delete topics")
		})

	})
})

package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	imagePullNodeLimit  = 1
	nodePrefix          = "worker"
	testImagePullSecret = "test-secret"
)

var rackNumber = []string{"rack0", "rack1"}
var testImagesList = []string{"foobar:123", "foobar:456"}

var _ = Describe("ImagePrefetch Controller", func() {
	Context("When reconciling a resource", func() {
		ctx := context.Background()
		var stopFunc func()

		BeforeEach(func() {
			mgr, err := ctrl.NewManager(cfg, ctrl.Options{
				Scheme:         scheme,
				LeaderElection: false,
				Metrics: metricsserver.Options{
					BindAddress: "0",
				},
				Controller: config.Controller{
					SkipNameValidation: ptr.To(true),
				},
			})
			Expect(err).NotTo(HaveOccurred())

			reconciler := &ImagePrefetchReconciler{
				Client:             mgr.GetClient(),
				Scheme:             mgr.GetScheme(),
				ImagePullNodeLimit: imagePullNodeLimit,
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
		})

		AfterEach(func() {
			stopFunc()

			time.Sleep(100 * time.Millisecond)
		})

		It("should create NodeImageSets according to the number specified in .spec.replicas.", func() {
			By("creating new nodes")
			// create new node
			for i := 0; i < 4; i++ {
				createNewNode(fmt.Sprintf("%s-%d", nodePrefix, i), rackNumber[0])
			}

			By("creating a new ImagePrefetch with replicas")
			testName := "replica-node-image-set"
			replicas := 1
			createNamespace(testName)
			imagePrefetch := createNewImagePrefetch(testName, ofenv1.ImagePrefetchSpec{
				Images:   testImagesList,
				Replicas: replicas,
				ImagePullSecrets: []corev1.LocalObjectReference{
					{
						Name: testImagePullSecret,
					},
				},
			})

			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))

				actualImages := []string{}
				for _, nodeImageSet := range nodeImageSets.Items {
					g.Expect(nodeImageSet.Spec.ImagePullSecrets).To(Equal([]corev1.LocalObjectReference{{
						Name: testImagePullSecret}}))
					for _, imageSet := range nodeImageSet.Spec.ImageSet {
						actualImages = append(actualImages, imageSet.Image)
					}
				}
				slices.Sort(actualImages)
				g.Expect(slices.Compact(actualImages)).To(ConsistOf(testImagesList))
				defaultPolicy, mirrorOnly := countRegistryPolicy(nodeImageSets)
				g.Expect(defaultPolicy).To(Equal(2)) // 1 node * 2 image
				g.Expect(mirrorOnly).To(Equal(0))    // 0 nodes * 2 image
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(imagePrefetch)
		})

		It("Should match node names in NodeImageSets with those in ImagePrefetch Status SelectedNodes", func() {
			By("creating a new ImagePrefetch with replicas")
			testName := "image-prefetch-status"
			replicas := 4
			createNamespace(testName)
			imagePrefetch := createNewImagePrefetch(testName, ofenv1.ImagePrefetchSpec{
				Images:   testImagesList,
				Replicas: replicas,
				ImagePullSecrets: []corev1.LocalObjectReference{
					{
						Name: testImagePullSecret,
					},
				},
			})

			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))
				var scheduleNodeName []string
				for _, nodeImageSet := range nodeImageSets.Items {
					scheduleNodeName = append(scheduleNodeName, nodeImageSet.Spec.NodeName)
				}

				imagePrefetch := &ofenv1.ImagePrefetch{}
				err = k8sClient.Get(ctx, client.ObjectKey{Name: testName, Namespace: testName}, imagePrefetch)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(imagePrefetch.Status.SelectedNodes).To(HaveLen(replicas))
				g.Expect(imagePrefetch.Status.SelectedNodes).To(ConsistOf(scheduleNodeName))
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(imagePrefetch)
		})

		It("should create NodeImageSet on the node with the fewest images", func() {
			By("increasing the images included in the nodes")
			node0 := fmt.Sprintf("%s-0", nodePrefix)
			node1 := fmt.Sprintf("%s-1", nodePrefix)
			node2 := fmt.Sprintf("%s-2", nodePrefix)
			for i := 0; i < 10; i++ {
				updateNodeImage(node0, []string{fmt.Sprintf("dummy/%d", i)})
				updateNodeImage(node1, []string{fmt.Sprintf("dummy/%d", i)})
				updateNodeImage(node2, []string{fmt.Sprintf("dummy/%d", i)})
			}

			By("creating imagePrefetch with replicas")
			testName := "fewest-images"
			createNamespace(testName)
			replicas := 1
			imagePrefetch := createNewImagePrefetch(testName, ofenv1.ImagePrefetchSpec{
				Images:   testImagesList,
				Replicas: replicas,
				ImagePullSecrets: []corev1.LocalObjectReference{
					{
						Name: testImagePullSecret,
					},
				},
			})

			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))
				g.Expect(nodeImageSets.Items[0].Spec.NodeName).To(Equal("worker-3"))
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(imagePrefetch)
		})

		It("should update ImagePrefetch status according to nodeImageSet state", func() {
			By("creating imagePrefetch with replicas")
			testName := "update-image-prefetch-status"
			createNamespace(testName)
			replicas := 1
			createNewImagePrefetch(testName, ofenv1.ImagePrefetchSpec{
				Images:   testImagesList,
				Replicas: replicas,
				ImagePullSecrets: []corev1.LocalObjectReference{
					{
						Name: testImagePullSecret,
					},
				},
			})

			By("checking imagePrefetch status to be progressing")
			Eventually(func(g Gomega) {
				imagePrefetch := &ofenv1.ImagePrefetch{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName, Namespace: testName}, imagePrefetch)
				g.Expect(err).NotTo(HaveOccurred())

				conditionImagePrefetchReady := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionReady)
				g.Expect(conditionImagePrefetchReady).NotTo(BeNil())
				g.Expect(conditionImagePrefetchReady.Status).To(Equal(metav1.ConditionFalse))
				conditionImagePrefetchProcessing := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionProgressing)
				g.Expect(conditionImagePrefetchProcessing).NotTo(BeNil())
				g.Expect(conditionImagePrefetchProcessing.Status).To(Equal(metav1.ConditionTrue))
				conditionImagePrefetchFailed := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionImagePullFailed)
				g.Expect(conditionImagePrefetchFailed).NotTo(BeNil())
				g.Expect(conditionImagePrefetchFailed.Status).To(Equal(metav1.ConditionFalse))
			}).Should(Succeed())

			By("updating nodeImageSet's status to image pull failed")
			failedCondition := metav1.Condition{
				Type:               ofenv1.ConditionImageDownloadFailed,
				Reason:             "test",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
			}
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())

				for _, nodeImageSet := range nodeImageSets.Items {
					nodeImageSet.Status.Conditions = []metav1.Condition{failedCondition}
					err = k8sClient.Status().Update(ctx, &nodeImageSet)
					g.Expect(err).NotTo(HaveOccurred())
				}
			}).Should(Succeed())

			By("checking imagePrefetch status to be failed")
			Eventually(func(g Gomega) {
				imagePrefetch := &ofenv1.ImagePrefetch{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName, Namespace: testName}, imagePrefetch)
				g.Expect(err).NotTo(HaveOccurred())

				conditionImagePrefetchReady := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionReady)
				g.Expect(conditionImagePrefetchReady).NotTo(BeNil())
				g.Expect(conditionImagePrefetchReady.Status).To(Equal(metav1.ConditionFalse))
				conditionImagePrefetchProcessing := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionProgressing)
				g.Expect(conditionImagePrefetchProcessing).NotTo(BeNil())
				g.Expect(conditionImagePrefetchProcessing.Status).To(Equal(metav1.ConditionTrue))
				conditionImagePrefetchFailed := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionImagePullFailed)
				g.Expect(conditionImagePrefetchFailed).NotTo(BeNil())
				g.Expect(conditionImagePrefetchFailed.Status).To(Equal(metav1.ConditionTrue))
			}).Should(Succeed())

			By("updating nodeImageSet's status to image available")
			failedCondition.Status = metav1.ConditionFalse
			imageAvailableCondition := metav1.Condition{
				Type:               ofenv1.ConditionImageAvailable,
				Reason:             "test",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
			}
			imageDownloadCompleteCondition := metav1.Condition{
				Type:               ofenv1.ConditionImageDownloadComplete,
				Reason:             "test",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
			}

			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())

				for _, nodeImageSet := range nodeImageSets.Items {
					nodeImageSet.Status.Conditions = []metav1.Condition{
						failedCondition, imageAvailableCondition, imageDownloadCompleteCondition}
					err = k8sClient.Status().Update(ctx, &nodeImageSet)
					Expect(err).NotTo(HaveOccurred())
				}
			}).Should(Succeed())

			By("checking imagePrefetch status to be ready")
			Eventually(func(g Gomega) {
				imagePrefetch := &ofenv1.ImagePrefetch{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: testName, Namespace: testName}, imagePrefetch)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(imagePrefetch).NotTo(BeNil())

				conditionImagePrefetchReady := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionReady)
				g.Expect(conditionImagePrefetchReady).NotTo(BeNil())
				g.Expect(conditionImagePrefetchReady.Status).To(Equal(metav1.ConditionTrue))
				conditionImagePrefetchProcessing := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionProgressing)
				g.Expect(conditionImagePrefetchProcessing).NotTo(BeNil())
				g.Expect(conditionImagePrefetchProcessing.Status).To(Equal(metav1.ConditionTrue))
				conditionImagePrefetchFailed := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionImagePullFailed)
				g.Expect(conditionImagePrefetchFailed).NotTo(BeNil())
				g.Expect(conditionImagePrefetchFailed.Status).To(Equal(metav1.ConditionFalse))
			}).Should(Succeed())
		})

		It("should increase or decrease NodeImageSets when replicas are changed", func() {
			By("creating imagePrefetch with replicas")
			testName := "increase-decrease-replicas"
			createNamespace(testName)
			replicas := 1
			createNewImagePrefetch(testName, ofenv1.ImagePrefetchSpec{
				Images:   testImagesList,
				Replicas: replicas,
				ImagePullSecrets: []corev1.LocalObjectReference{
					{
						Name: testImagePullSecret,
					},
				},
			})

			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))
				defaultPolicy, mirrorOnly := countRegistryPolicy(nodeImageSets)
				g.Expect(defaultPolicy).To(Equal(2)) // 1node * 2image
				g.Expect(mirrorOnly).To(Equal(0))    // 0nodes * 2image
			}).Should(Succeed())

			By("updating the replicas of ImagePrefetch resource from 1 to 4")
			replicas = 4 // 1 -> 4
			imagePrefetch := &ofenv1.ImagePrefetch{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: testName, Namespace: testName}, imagePrefetch)
			Expect(err).NotTo(HaveOccurred())
			imagePrefetch.Spec.Replicas = replicas
			err = k8sClient.Update(ctx, imagePrefetch)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))
				defaultPolicy, mirrorOnly := countRegistryPolicy(nodeImageSets)
				g.Expect(defaultPolicy).To(Equal(2)) // 1node * 2image
				g.Expect(mirrorOnly).To(Equal(6))    // 3nodes * 2image
			}).Should(Succeed())

			By("updating the replicas of ImagePrefetch resource from 4 to 2")
			imagePrefetch = &ofenv1.ImagePrefetch{}
			err = k8sClient.Get(ctx, client.ObjectKey{Name: testName, Namespace: testName}, imagePrefetch)
			Expect(err).NotTo(HaveOccurred())
			replicas = 2 // 4 -> 2
			imagePrefetch.Spec.Replicas = replicas
			err = k8sClient.Update(ctx, imagePrefetch)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))
				actualImages := []string{}
				for _, nodeImageSet := range nodeImageSets.Items {
					for _, imageSet := range nodeImageSet.Spec.ImageSet {
						actualImages = append(actualImages, imageSet.Image)
					}
				}
				slices.Sort(actualImages)
				g.Expect(slices.Compact(actualImages)).To(ConsistOf(testImagesList))
				defaultPolicy, mirrorOnly := countRegistryPolicy(nodeImageSets)
				g.Expect(defaultPolicy).To(Equal(2)) // 1 node * 2 image
				g.Expect(mirrorOnly).To(Equal(2))    // 1 node * 2 image
			}).Should(Succeed())
		})

		It("should create NodeImageSet on another node when a node is deleted", func() {
			By("creating imagePrefetch with replicas")
			testName := "delete-node"
			createNamespace(testName)
			replicas := 4
			createNewImagePrefetch(testName, ofenv1.ImagePrefetchSpec{
				Images:   testImagesList,
				Replicas: replicas,
				ImagePullSecrets: []corev1.LocalObjectReference{
					{
						Name: testImagePullSecret,
					},
				},
			})

			nodeImageSets := &ofenv1.NodeImageSetList{}
			Eventually(func(g Gomega) {
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))
			}).Should(Succeed())

			By("Deleting one node")
			deletingNodeName := nodeImageSets.Items[0].Spec.NodeName
			deletingNode := &corev1.Node{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: deletingNodeName}, deletingNode)
			Expect(err).NotTo(HaveOccurred())
			err = k8sClient.Delete(ctx, deletingNode)
			Expect(err).NotTo(HaveOccurred())

			By("Checking NodeImageSet is created on another node")
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))
				for _, nodeImageSet := range nodeImageSets.Items {
					g.Expect(nodeImageSet.Spec.NodeName).NotTo(Equal(deletingNodeName))
				}
			})
		})

		It("should delete NodeImageSet when ImagePrefetch is deleted", func() {
			By("creating imagePrefetch with replicas")
			testName := "delete-image-prefetch"
			createNamespace(testName)
			replicas := 4
			createNewImagePrefetch(testName, ofenv1.ImagePrefetchSpec{
				Images:   testImagesList,
				Replicas: replicas,
				ImagePullSecrets: []corev1.LocalObjectReference{
					{
						Name: testImagePullSecret,
					},
				},
			})

			By("Deleting imagePrefetch")
			imagePrefetch := &ofenv1.ImagePrefetch{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: testName, Namespace: testName}, imagePrefetch)
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Delete(ctx, imagePrefetch)
			Expect(err).NotTo(HaveOccurred())

			By("Checking nodeImageSet is deleted")
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(0))
			}).Should(Succeed())
		})

		It("should create NodeImageSets according to the node selector", func() {
			By("creating new nodes")
			for i := 4; i < 7; i++ {
				createNewNode(fmt.Sprintf("%s-%d", nodePrefix, i), rackNumber[1])
			}

			By("creating ImagePrefetch with node selector")
			testName := "node-selector"
			nodeSelector := metav1.LabelSelector{
				MatchLabels: map[string]string{
					"topology.kubernetes.io/zone": rackNumber[1],
				},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "kubernetes.io/hostname",
						Operator: metav1.LabelSelectorOpIn,
						Values:   []string{"worker1", "worker-4", "worker-6"},
					},
				},
			}

			createNamespace(testName)
			createNewImagePrefetch(testName,
				ofenv1.ImagePrefetchSpec{
					Images:       testImagesList,
					NodeSelector: nodeSelector,
				},
			)

			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(nodeImageSets.Items)).To(Equal(2))
				for _, nodeImageSet := range nodeImageSets.Items {
					g.Expect(nodeImageSet.Spec.NodeName).To(SatisfyAny(Equal("worker-4"), Equal("worker-6")))
				}

				actualImages := []string{}
				for _, nodeImageSet := range nodeImageSets.Items {
					for _, imageSet := range nodeImageSet.Spec.ImageSet {
						actualImages = append(actualImages, imageSet.Image)
					}
				}
				slices.Sort(actualImages)
				g.Expect(slices.Compact(actualImages)).To(ConsistOf(testImagesList))

				defaultPolicy, mirrorOnly := countRegistryPolicy(nodeImageSets)
				g.Expect(defaultPolicy).To(Equal(2)) // 1 node * 2 image
				g.Expect(mirrorOnly).To(Equal(2))    // 1 node * 2 image
			}).Should(Succeed())
		})

		It("should increase or decrease the number of NodeImageSets when nodes are added or removed", func() {
			By("creating ImagePrefetch with node selector")
			testName := "add-remove-node"
			nodeSelector := metav1.LabelSelector{
				MatchLabels: map[string]string{
					"beta.kubernetes.io/arch": "amd64",
				},
			}
			createNamespace(testName)
			createNewImagePrefetch(testName,
				ofenv1.ImagePrefetchSpec{
					Images:       testImagesList,
					NodeSelector: nodeSelector,
				},
			)
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(nodeImageSets.Items)).To(Equal(6))
			}).Should(Succeed())

			By("adding a new node")
			newNodeName := fmt.Sprintf("%s-7", nodePrefix)
			createNewNode(newNodeName, rackNumber[1])

			By("checking the number of NodeImageSets is increased")
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(7))
			}).Should(Succeed())

			By("removing a node")
			nodeList := &corev1.NodeList{}
			err := k8sClient.List(ctx, nodeList)
			Expect(err).NotTo(HaveOccurred())
			err = k8sClient.Delete(ctx, &nodeList.Items[0])
			Expect(err).NotTo(HaveOccurred())

			By("checking the number of NodeImageSets is decreased")
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(6))
			}).Should(Succeed())
		})

		It("should create NodeImageSets according to the node selector and replicas", func() {
			By("creating ImagePrefetch with node selector and replicas")
			testName := "node-selector-replicas"
			nodeSelector := metav1.LabelSelector{
				MatchLabels: map[string]string{
					"beta.kubernetes.io/arch": "amd64",
				},
			}
			createNamespace(testName)
			createNewImagePrefetch(testName,
				ofenv1.ImagePrefetchSpec{
					Images:       testImagesList,
					NodeSelector: nodeSelector,
					Replicas:     2,
				},
			)
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(nodeImageSets.Items)).To(Equal(2))
			}).Should(Succeed())
		})

		It("should recreate NodeImageSets on another node when a node is NotReady", func() {
			By("creating ImagePrefetch with replicas")
			testName := "not-ready-node"
			createNamespace(testName)
			createNewImagePrefetch(testName,
				ofenv1.ImagePrefetchSpec{
					Images:   testImagesList,
					Replicas: 2,
				},
			)
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(nodeImageSets.Items)).To(Equal(2))
			}).Should(Succeed())

			By("updating a node to NotReady")
			nodeImageSets := &ofenv1.NodeImageSetList{}
			err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
				LabelSelector: labels.SelectorFromSet(map[string]string{
					constants.OwnerImagePrefetchNamespace: testName,
				}),
			})
			Expect(err).NotTo(HaveOccurred())
			nodeName := nodeImageSets.Items[0].Spec.NodeName
			node := &corev1.Node{}
			err = k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node)
			Expect(err).NotTo(HaveOccurred())
			node.Status.Conditions = []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionFalse,
				},
			}
			err = k8sClient.Status().Update(ctx, node)
			Expect(err).NotTo(HaveOccurred())

			By("checking NodeImageSets are recreated on another node")
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(nodeImageSets.Items)).To(Equal(2))
				for _, nodeImageSet := range nodeImageSets.Items {
					g.Expect(nodeImageSet.Spec.NodeName).NotTo(Equal(nodeName))
				}
			}).Should(Succeed())
		})
	})
})

func createNewNode(name, zoneName string) string {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"kubernetes.io/hostname":      name,
				"topology.kubernetes.io/zone": zoneName,
				"beta.kubernetes.io/arch":     "amd64",
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
	err := k8sClient.Create(ctx, node)
	Expect(err).NotTo(HaveOccurred())
	return name
}

func createNamespace(name string) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	err := k8sClient.Create(ctx, ns)
	Expect(err).NotTo(HaveOccurred())
}

func createNewImagePrefetch(testName string, spec ofenv1.ImagePrefetchSpec) *ofenv1.ImagePrefetch {
	newImagePrefetch := &ofenv1.ImagePrefetch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testName,
			Namespace: testName,
		},
		Spec: spec,
	}

	err := k8sClient.Create(ctx, newImagePrefetch)
	Expect(err).NotTo(HaveOccurred())
	return newImagePrefetch
}

func deleteImagePrefetchResource(imagePrefetch *ofenv1.ImagePrefetch) {
	err := k8sClient.Delete(ctx, imagePrefetch)
	Expect(err).NotTo(HaveOccurred())
}

func updateNodeImage(nodeName string, images []string) {
	node := &corev1.Node{}
	err := k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node)
	Expect(err).NotTo(HaveOccurred())

	node.Status.Conditions = []corev1.NodeCondition{
		{
			Type:   corev1.NodeReady,
			Status: corev1.ConditionTrue,
		},
	}
	err = k8sClient.Status().Update(ctx, node)
	Expect(err).NotTo(HaveOccurred())

	for _, image := range images {
		node.Status.Images = append(node.Status.Images, corev1.ContainerImage{
			Names: []string{image},
		})
	}
	err = k8sClient.Status().Update(ctx, node)
	Expect(err).NotTo(HaveOccurred())
}

func countRegistryPolicy(nodeImageSets *ofenv1.NodeImageSetList) (int, int) {
	defaultPolicy, mirrorOnly := 0, 0
	for _, nodeImageSet := range nodeImageSets.Items {
		for _, imageSet := range nodeImageSet.Spec.ImageSet {
			if imageSet.RegistryPolicy == ofenv1.RegistryPolicyMirrorOnly {
				mirrorOnly++
			} else if imageSet.RegistryPolicy == ofenv1.RegistryPolicyDefault {
				defaultPolicy++
			}
		}
	}
	return defaultPolicy, mirrorOnly
}

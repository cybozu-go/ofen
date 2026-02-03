package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
)

const (
	imagePullNodeLimit                 = 1
	maxConcurrentNodeImageSetCreations = 1
	nodePrefix                         = "worker"
	testImagePullSecret                = "test-secret"
)

var testImagesList = []string{"foobar:123", "foobar:456"}

func prepareObjects(ctx context.Context) error {
	// create 4 nodes with labels
	for i := range 4 {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-%d", nodePrefix, i),
				Labels: map[string]string{
					"kubernetes.io/hostname":      fmt.Sprintf("%s-%d", nodePrefix, i),
					"topology.kubernetes.io/zone": "rack0",
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

		_, err := ctrl.CreateOrUpdate(ctx, k8sClient, node, func() error {
			return nil
		})
		if err != nil {
			return err
		}
	}

	// create 3 nodes with labels
	for i := 4; i < 7; i++ {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-%d", nodePrefix, i),
				Labels: map[string]string{
					"kubernetes.io/hostname":      fmt.Sprintf("%s-%d", nodePrefix, i),
					"topology.kubernetes.io/zone": "rack1",
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
		_, err := ctrl.CreateOrUpdate(ctx, k8sClient, node, func() error {
			return nil
		})
		if err != nil {
			return err
		}

	}

	return nil
}

func deleteAllNodes(ctx context.Context) {
	nodes := &corev1.NodeList{}
	err := k8sClient.List(ctx, nodes)
	Expect(err).NotTo(HaveOccurred())
	for _, node := range nodes.Items {
		err = k8sClient.Delete(ctx, &node)
		Expect(err).NotTo(HaveOccurred())
	}
}

var _ = Describe("ImagePrefetch Controller", Serial, func() {
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

			reconciler := &ImagePrefetchReconciler{
				Client:                             mgr.GetClient(),
				Scheme:                             mgr.GetScheme(),
				ImagePullNodeLimit:                 imagePullNodeLimit,
				MaxConcurrentNodeImageSetCreations: maxConcurrentNodeImageSetCreations,
			}
			err = reconciler.SetupWithManager(mgr)
			Expect(err).NotTo(HaveOccurred())
			err = prepareObjects(ctx)
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
			time.Sleep(100 * time.Millisecond) // wait for the reconcile to finish
			stopFunc()
			time.Sleep(100 * time.Millisecond)
		})

		It("should create NodeImageSets according to the number specified in .spec.replicas", func() {
			By("creating a new ImagePrefetch with replicas")
			testName := "replica-node-image-set"
			replicas := 1
			createNamespace(ctx, testName)
			imagePrefetch := createNewImagePrefetch(ctx, testName, ofenv1.ImagePrefetchSpec{
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

				for _, nodeImageSet := range nodeImageSets.Items {
					g.Expect(nodeImageSet.Spec.ImagePullSecrets).To(Equal([]corev1.LocalObjectReference{{
						Name: testImagePullSecret}}))
					g.Expect(nodeImageSet.Spec.Images).Should(ConsistOf(testImagesList))
				}
				defaultPolicy, mirrorOnly := countRegistryPolicy(nodeImageSets)
				g.Expect(defaultPolicy).To(Equal(1)) // 1node
				g.Expect(mirrorOnly).To(Equal(0))    // 0node
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(ctx, imagePrefetch)
		})

		It("should create NodeImageSets according to the node selector and replicas", func() {
			By("creating ImagePrefetch with node selector")
			testName := "node-selector"
			nodeSelector := metav1.LabelSelector{
				MatchLabels: map[string]string{
					"topology.kubernetes.io/zone": "rack0",
				},
			}
			replicas := 2

			createNamespace(ctx, testName)
			imagePrefetch := createNewImagePrefetch(ctx, testName,
				ofenv1.ImagePrefetchSpec{
					Images:       testImagesList,
					NodeSelector: nodeSelector,
					Replicas:     replicas,
				},
			)

			updateNodeImageSetStatusToImageAvailable(ctx, testName, replicas)

			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))
				nodeList := []string{}
				for _, nodeImageSet := range nodeImageSets.Items {
					g.Expect(nodeImageSet.Spec.Images).Should(ConsistOf(testImagesList))
					nodeList = append(nodeList, nodeImageSet.Spec.NodeName)
				}
				for _, nodeName := range nodeList {
					node := &corev1.Node{}
					err := k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(node.Labels).Should(HaveKeyWithValue("topology.kubernetes.io/zone", "rack0"))
				}

				defaultPolicy, mirrorOnly := countRegistryPolicy(nodeImageSets)
				g.Expect(defaultPolicy).To(Equal(1)) // 1node
				g.Expect(mirrorOnly).To(Equal(1))    // 1node
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(ctx, imagePrefetch)
		})

		It("should create NodeImageSets according to the node selector and allNodes", func() {
			By("creating ImagePrefetch with node selector and allNodes")
			testName := "node-selector-all-nodes"
			nodeSelector := metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "topology.kubernetes.io/zone",
						Operator: metav1.LabelSelectorOpIn,
						Values:   []string{"rack1"},
					},
				},
			}
			allNodes := true
			allNodesCount := 3

			createNamespace(ctx, testName)
			imagePrefetch := createNewImagePrefetch(ctx, testName,
				ofenv1.ImagePrefetchSpec{
					Images:       testImagesList,
					NodeSelector: nodeSelector,
					AllNodes:     allNodes,
				},
			)

			updateNodeImageSetStatusToImageAvailable(ctx, testName, allNodesCount)
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(allNodesCount))
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(ctx, imagePrefetch)
		})

		It("should create NodeImageSets according to the allNodes", func() {
			By("creating ImagePrefetch with allNodes")
			testName := "all-nodes"
			allNodesCount := 7

			createNamespace(ctx, testName)
			imagePrefetch := createNewImagePrefetch(ctx, testName,
				ofenv1.ImagePrefetchSpec{
					Images:   testImagesList,
					AllNodes: true,
				},
			)

			updateNodeImageSetStatusToImageAvailable(ctx, testName, allNodesCount)

			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(allNodesCount))
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(ctx, imagePrefetch)
		})

		It("should delete NodeImageSets when the ImagePrefetch resource is deleted", func() {
			By("creating a new ImagePrefetch with replicas")

			testName := "confirm-delete-node-image-set"
			replicas := 4
			createNamespace(ctx, testName)
			imagePrefetch := createNewImagePrefetch(ctx, testName, ofenv1.ImagePrefetchSpec{
				Images:   testImagesList,
				Replicas: replicas,
			})
			updateNodeImageSetStatusToImageAvailable(ctx, testName, replicas)

			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))
			}).Should(Succeed())

			By("deleting the ImagePrefetch resource")
			deleteImagePrefetchResource(ctx, imagePrefetch)

			By("checking NodeImageSets are deleted")
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(BeEmpty())
			}).Should(Succeed())
		})

		It("should match node names in NodeImageSets with those in ImagePrefetch Status SelectedNodes", func() {
			By("creating a new ImagePrefetch with replicas")
			testName := "image-prefetch-status"
			replicas := 4
			createNamespace(ctx, testName)
			imagePrefetch := createNewImagePrefetch(ctx, testName, ofenv1.ImagePrefetchSpec{
				Images:   testImagesList,
				Replicas: replicas,
				ImagePullSecrets: []corev1.LocalObjectReference{
					{
						Name: testImagePullSecret,
					},
				},
			})

			updateNodeImageSetStatusToImageAvailable(ctx, testName, replicas)
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
			deleteImagePrefetchResource(ctx, imagePrefetch)
		})

		It("should create NodeImageSet for the each failure domains", func() {
			By("creating imagePrefetch with replicas")
			testName := "failure-domains"
			createNamespace(ctx, testName)
			replicas := 3
			imagePrefetch := createNewImagePrefetch(ctx, testName, ofenv1.ImagePrefetchSpec{
				Images:   testImagesList,
				Replicas: replicas,
			})

			updateNodeImageSetStatusToImageAvailable(ctx, testName, replicas)
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))

				zoneSet := make(map[string]struct{})
				for _, nodeImageSet := range nodeImageSets.Items {
					nodeName := nodeImageSet.Spec.NodeName
					node := corev1.Node{}
					err := k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, &node)
					g.Expect(err).NotTo(HaveOccurred())
					zone := node.Labels["topology.kubernetes.io/zone"]
					g.Expect(zone).ToNot(BeEmpty())
					zoneSet[zone] = struct{}{}
				}
				g.Expect(zoneSet).To(HaveLen(2)) // 2 failure domains
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(ctx, imagePrefetch)
		})

		It("should update ImagePrefetch status according to nodeImageSet state", func() {
			By("creating imagePrefetch with replicas")
			testName := "update-image-prefetch-status"
			createNamespace(ctx, testName)
			replicas := 1
			imagePrefetch := createNewImagePrefetch(ctx, testName, ofenv1.ImagePrefetchSpec{
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
				ConditionNodeImageSetsCreated := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionNodeImageSetsCreated)
				g.Expect(ConditionNodeImageSetsCreated).NotTo(BeNil())
				g.Expect(ConditionNodeImageSetsCreated.Status).To(Equal(metav1.ConditionTrue))
				conditionNoImagePrefetchFailed := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionNoImagePullFailed)
				g.Expect(conditionNoImagePrefetchFailed).NotTo(BeNil())
				g.Expect(conditionNoImagePrefetchFailed.Status).To(Equal(metav1.ConditionTrue))
			}).Should(Succeed())

			By("updating nodeImageSet's status to image pull failed")
			failedCondition := metav1.Condition{
				Type:               ofenv1.ConditionImageDownloadSucceeded,
				Reason:             "test",
				Status:             metav1.ConditionFalse,
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
				conditionNodeImageSetsCreated := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionNodeImageSetsCreated)
				g.Expect(conditionNodeImageSetsCreated).NotTo(BeNil())
				g.Expect(conditionNodeImageSetsCreated.Status).To(Equal(metav1.ConditionTrue))
				conditionNoImagePrefetchFailed := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionNoImagePullFailed)
				g.Expect(conditionNoImagePrefetchFailed).NotTo(BeNil())
				g.Expect(conditionNoImagePrefetchFailed.Status).To(Equal(metav1.ConditionFalse))
			}).Should(Succeed())

			By("updating nodeImageSet's status to image available")
			failedCondition.Status = metav1.ConditionTrue
			imageAvailableCondition := metav1.Condition{
				Type:               ofenv1.ConditionImageAvailable,
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
						failedCondition, imageAvailableCondition}
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
				conditionNodeImageSetsCreated := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionNodeImageSetsCreated)
				g.Expect(conditionNodeImageSetsCreated).NotTo(BeNil())
				g.Expect(conditionNodeImageSetsCreated.Status).To(Equal(metav1.ConditionTrue))
				conditionNoImagePrefetchFailed := meta.FindStatusCondition(imagePrefetch.Status.Conditions, ofenv1.ConditionNoImagePullFailed)
				g.Expect(conditionNoImagePrefetchFailed).NotTo(BeNil())
				g.Expect(conditionNoImagePrefetchFailed.Status).To(Equal(metav1.ConditionTrue))
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(ctx, imagePrefetch)
		})

		It("should increase or decrease NodeImageSets when replicas are changed", func() {
			By("creating imagePrefetch with replicas")
			testName := "increase-decrease-replicas"
			createNamespace(ctx, testName)
			replicas := 1
			createNewImagePrefetch(ctx, testName, ofenv1.ImagePrefetchSpec{
				Images:   testImagesList,
				Replicas: replicas,
				ImagePullSecrets: []corev1.LocalObjectReference{
					{
						Name: testImagePullSecret,
					},
				},
			})

			updateNodeImageSetStatusToImageAvailable(ctx, testName, replicas)
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
				g.Expect(defaultPolicy).To(Equal(1)) // 1node
				g.Expect(mirrorOnly).To(Equal(0))    // 0node
			}).Should(Succeed())

			By("updating the replicas of ImagePrefetch resource from 1 to 4")
			replicas = 4 // 1 -> 4
			imagePrefetch := &ofenv1.ImagePrefetch{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: testName, Namespace: testName}, imagePrefetch)
			Expect(err).NotTo(HaveOccurred())
			imagePrefetch.Spec.Replicas = replicas
			err = k8sClient.Update(ctx, imagePrefetch)
			Expect(err).NotTo(HaveOccurred())

			updateNodeImageSetStatusToImageAvailable(ctx, testName, replicas)
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
				g.Expect(defaultPolicy).To(Equal(1)) // 1node
				g.Expect(mirrorOnly).To(Equal(3))    // 3node
			}).Should(Succeed())

			By("updating the replicas of ImagePrefetch resource from 4 to 2")
			imagePrefetch = &ofenv1.ImagePrefetch{}
			err = k8sClient.Get(ctx, client.ObjectKey{Name: testName, Namespace: testName}, imagePrefetch)
			Expect(err).NotTo(HaveOccurred())
			replicas = 2 // 4 -> 2
			imagePrefetch.Spec.Replicas = replicas
			err = k8sClient.Update(ctx, imagePrefetch)
			Expect(err).NotTo(HaveOccurred())

			updateNodeImageSetStatusToImageAvailable(ctx, testName, replicas)
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
					g.Expect(nodeImageSet.Spec.Images).Should(ConsistOf(testImagesList))
				}

				defaultPolicy, mirrorOnly := countRegistryPolicy(nodeImageSets)
				g.Expect(defaultPolicy).To(Equal(1)) // 1node
				g.Expect(mirrorOnly).To(Equal(1))    // 1node
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(ctx, imagePrefetch)
		})

		It("should create NodeImageSet on another node when a node is deleted", func() {
			By("creating imagePrefetch with replicas")
			testName := "delete-node"
			createNamespace(ctx, testName)
			replicas := 4
			imagePrefetch := createNewImagePrefetch(ctx, testName, ofenv1.ImagePrefetchSpec{
				Images:   testImagesList,
				Replicas: replicas,
				ImagePullSecrets: []corev1.LocalObjectReference{
					{
						Name: testImagePullSecret,
					},
				},
			})

			updateNodeImageSetStatusToImageAvailable(ctx, testName, replicas)
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
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(ctx, imagePrefetch)
		})

		It("should increase or decrease the number of NodeImageSets when node are added or removed", func() {
			By("creating ImagePrefetch with node selector")
			testName := "add-remove-node"
			nodeSelector := metav1.LabelSelector{
				MatchLabels: map[string]string{
					"topology.kubernetes.io/zone": "rack1",
				},
			}
			allNodesCount := 3

			createNamespace(ctx, testName)
			imagePrefetch := createNewImagePrefetch(ctx, testName,
				ofenv1.ImagePrefetchSpec{
					Images:       testImagesList,
					NodeSelector: nodeSelector,
					AllNodes:     true,
				},
			)

			updateNodeImageSetStatusToImageAvailable(ctx, testName, allNodesCount)
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(allNodesCount))
			}).Should(Succeed())

			By("adding a new node")
			newNodeName := fmt.Sprintf("%s-7", nodePrefix)
			createNewNode(ctx, newNodeName, "rack1")
			allNodesCount++

			By("checking the number of NodeImageSets is increased")
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(4))
			}).Should(Succeed())

			By("removing a node")
			node := &corev1.Node{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: newNodeName}, node)
			Expect(err).NotTo(HaveOccurred())
			err = k8sClient.Delete(ctx, node)
			Expect(err).NotTo(HaveOccurred())
			allNodesCount--

			By("checking the number of NodeImageSets is decreased")
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(allNodesCount))
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(ctx, imagePrefetch)

			By("cleaning up the new node")
			deleteAllNodes(ctx)
		})

		It("should recreate NodeImageSets on another node when a node is NotReady", func() {
			By("creating ImagePrefetch with replicas")
			testName := "not-ready-node"
			createNamespace(ctx, testName)
			replicas := 2
			imagePrefetch := createNewImagePrefetch(ctx,
				testName,
				ofenv1.ImagePrefetchSpec{
					Images:   testImagesList,
					Replicas: replicas,
				},
			)

			updateNodeImageSetStatusToImageAvailable(ctx, testName, replicas)
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))
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
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))
				for _, nodeImageSet := range nodeImageSets.Items {
					g.Expect(nodeImageSet.Spec.NodeName).NotTo(Equal(nodeName))
				}
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(ctx, imagePrefetch)

			By("cleaning up the new node")
			deleteAllNodes(ctx)
		})

		It("should not include not ready node when one node is NotReady", func() {
			By("creating a node in NotReady state")
			notReadyNodeName := fmt.Sprintf("%s-8", nodePrefix)
			createNewNode(ctx, notReadyNodeName, "rack0")

			node := &corev1.Node{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: notReadyNodeName}, node)
			Expect(err).NotTo(HaveOccurred())
			node.Status.Conditions = []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionFalse,
				},
			}
			err = k8sClient.Status().Update(ctx, node)
			Expect(err).NotTo(HaveOccurred())

			By("creating ImagePrefetch with nodeSelector and allNodes")
			testName := "selector-all-nodes-not-ready"
			nodeSelector := metav1.LabelSelector{
				MatchLabels: map[string]string{
					"topology.kubernetes.io/zone": "rack0",
				},
			}

			createNamespace(ctx, testName)
			imagePrefetch := createNewImagePrefetch(ctx, testName,
				ofenv1.ImagePrefetchSpec{
					Images:       testImagesList,
					NodeSelector: nodeSelector,
					AllNodes:     true,
				},
			)

			By("checking that NotReady node is not included in NodeImageSets")
			updateNodeImageSetStatusToImageAvailable(ctx, testName, 4)
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(4))

				for _, nodeImageSet := range nodeImageSets.Items {
					g.Expect(nodeImageSet.Spec.NodeName).NotTo(Equal(notReadyNodeName))
				}
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(ctx, imagePrefetch)

			By("cleaning up the new node")
			deleteAllNodes(ctx)
		})

		It("should create NodeImageSets sequentially based on ImagePullNodeLimit", func() {
			By("creating a new ImagePrefetch with replicas exceeding node limit")
			testName := "sequential-creation"
			replicas := 3
			createNamespace(ctx, testName)
			imagePrefetch := createNewImagePrefetch(ctx, testName, ofenv1.ImagePrefetchSpec{
				Images:   testImagesList,
				Replicas: replicas,
			})

			By("checking that only one NodeImageSet is created initially due to ImagePullNodeLimit")
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(1)) // Only 1 due to ImagePullNodeLimit
			}).Should(Succeed())

			By("updating the first NodeImageSet status to image available")
			updateNodeImageSetStatusToImageAvailable(ctx, testName, 1)

			By("checking that additional NodeImageSets are created after the first one completes")
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(2)) // Second one should be created
			}).Should(Succeed())

			By("updating the second NodeImageSet status to image available")
			updateNodeImageSetStatusToImageAvailable(ctx, testName, 2)

			By("checking that the final NodeImageSet is created after the second one completes")
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(3)) // All 3 should be created now
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(ctx, imagePrefetch)
		})

		It("should update NodeImageSets one by one when ImagePrefetch is modified", func() {
			By("creating a new ImagePrefetch with replicas")
			testName := "sequential-update"
			replicas := 3
			newImage := "extraimage:v3"
			createNamespace(ctx, testName)
			createNewImagePrefetch(ctx, testName, ofenv1.ImagePrefetchSpec{
				Images:   testImagesList,
				Replicas: replicas,
			})

			updateNodeImageSetStatusToImageAvailable(ctx, testName, replicas)
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))
			}).Should(Succeed())

			By("updating ImagePrefetch spec to add image")
			imagePrefetch := &ofenv1.ImagePrefetch{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: testName, Namespace: testName}, imagePrefetch)
			Expect(err).NotTo(HaveOccurred())

			imagePrefetch.Spec.Images = append(imagePrefetch.Spec.Images, newImage) // to test multiple image changes
			err = k8sClient.Update(ctx, imagePrefetch)
			Expect(err).NotTo(HaveOccurred())

			By("checking that only one NodeImageSet is updated initially")
			var newGenerationNodeImageSetName string
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(nodeImageSets.Items).To(HaveLen(replicas))

				// Count how many NodeImageSets have the new generation
				newGenerationCount := 0
				oldGenerationCount := 0
				for _, nodeImageSet := range nodeImageSets.Items {
					if len(nodeImageSet.Spec.Images) == 3 {
						g.Expect(nodeImageSet.Spec.Images).To(ContainElement(newImage))
						newGenerationCount++
						newGenerationNodeImageSetName = nodeImageSet.Name
					} else {
						oldGenerationCount++
					}
				}
				g.Expect(newGenerationCount).To(Equal(1)) // Only one should be updated initially
				g.Expect(oldGenerationCount).To(Equal(2)) // Two should still have old generation
			}).Should(Succeed())

			By("marking the first NodeImageSet as image available")
			Eventually(func(g Gomega) {
				nodeImageSet := &ofenv1.NodeImageSet{}
				err = k8sClient.Get(ctx, client.ObjectKey{Name: newGenerationNodeImageSetName, Namespace: testName}, nodeImageSet)
				g.Expect(err).NotTo(HaveOccurred())

				nodeImageSet.Status.Conditions = []metav1.Condition{
					{
						Type:               ofenv1.ConditionImageAvailable,
						Reason:             "test",
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
					},
				}

				var containerImageStatuses []ofenv1.ContainerImageStatus
				for _, image := range nodeImageSet.Spec.Images {
					containerImageStatuses = append(containerImageStatuses, ofenv1.ContainerImageStatus{
						ImageRef: image,
						State:    ofenv1.ImageDownloaded,
					})
				}

				nodeImageSet.Status.ContainerImageStatuses = containerImageStatuses
				err = k8sClient.Status().Update(ctx, nodeImageSet)
				g.Expect(err).NotTo(HaveOccurred())
			}).Should(Succeed())

			By("checking that a second NodeImageSet is updated after the first one completes")
			Eventually(func(g Gomega) {
				nodeImageSets := &ofenv1.NodeImageSetList{}
				err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(map[string]string{
						constants.OwnerImagePrefetchNamespace: testName,
					}),
				})
				g.Expect(err).NotTo(HaveOccurred())

				newGenerationCount := 0
				for _, nodeImageSet := range nodeImageSets.Items {
					if len(nodeImageSet.Spec.Images) == 3 {
						g.Expect(nodeImageSet.Spec.Images).To(ContainElement(newImage))
						newGenerationCount++
					}
				}
				g.Expect(newGenerationCount).To(Equal(2)) // Second one should be updated now
			}).Should(Succeed())

			By("cleaning up the ImagePrefetch resource")
			deleteImagePrefetchResource(ctx, imagePrefetch)
		})
	})

})

func createNewNode(ctx context.Context, name, zoneName string) {
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
}

func createNamespace(ctx context.Context, name string) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	err := k8sClient.Create(ctx, ns)
	Expect(err).NotTo(HaveOccurred())
}

func createNewImagePrefetch(ctx context.Context, testName string, spec ofenv1.ImagePrefetchSpec) *ofenv1.ImagePrefetch {
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

func deleteImagePrefetchResource(ctx context.Context, imagePrefetch *ofenv1.ImagePrefetch) {
	err := k8sClient.Delete(ctx, imagePrefetch)
	Expect(err).NotTo(HaveOccurred())

	Eventually(func(g Gomega) {
		ip := &ofenv1.ImagePrefetch{}
		err := k8sClient.Get(ctx, client.ObjectKey{Name: imagePrefetch.Name, Namespace: imagePrefetch.Namespace}, ip)
		g.Expect(err).To(HaveOccurred())
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}).Should(Succeed())
}

func countRegistryPolicy(nodeImageSets *ofenv1.NodeImageSetList) (int, int) {
	defaultPolicy, mirrorOnly := 0, 0
	for _, nodeImageSet := range nodeImageSets.Items {
		switch nodeImageSet.Spec.RegistryPolicy {
		case ofenv1.RegistryPolicyMirrorOnly:
			mirrorOnly++
		case ofenv1.RegistryPolicyDefault:
			defaultPolicy++
		}
	}
	return defaultPolicy, mirrorOnly
}

func updateNodeImageSetStatusToImageAvailable(ctx context.Context, namespace string, desired int) {
	imageAvailableCondition := metav1.Condition{
		Type:               ofenv1.ConditionImageAvailable,
		Reason:             "test",
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
	}

	Eventually(func() error {
		nodeImageSets := &ofenv1.NodeImageSetList{}
		err := k8sClient.List(ctx, nodeImageSets, &client.ListOptions{
			LabelSelector: labels.SelectorFromSet(map[string]string{
				constants.OwnerImagePrefetchNamespace: namespace,
			}),
		})
		if err != nil {
			return err
		}

		count := 0
		for _, nodeImageSet := range nodeImageSets.Items {
			nodeImageSet.Status.Conditions = []metav1.Condition{imageAvailableCondition}
			var containerImageStatuses []ofenv1.ContainerImageStatus
			for _, image := range nodeImageSet.Spec.Images {
				containerImageStatuses = append(containerImageStatuses, ofenv1.ContainerImageStatus{
					ImageRef: image,
					State:    ofenv1.ImageDownloaded,
				})
			}
			nodeImageSet.Status.ContainerImageStatuses = containerImageStatuses
			err = k8sClient.Status().Update(ctx, &nodeImageSet)
			if err != nil {
				return err
			}

			count++
			if count >= desired {
				return nil
			}
		}

		return fmt.Errorf("only %d NodeImageSets updated, desired %d", count, desired)
	}).Should(Succeed())
}

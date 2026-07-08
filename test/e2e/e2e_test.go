/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const namespace = "ofen-system"

const testNamespace = "e2e-test"

const deleteTestImage = "ghcr.io/cybozu/ubuntu-debug:24.04"

const pinnedTestImage = "ghcr.io/cybozu/ubuntu:24.04"

func execWrapper(cmd string, input []byte, args ...string) ([]byte, []byte, error) {
	var stdout, stderr bytes.Buffer
	command := exec.Command(cmd, args...)
	command.Stdout = &stdout
	command.Stderr = &stderr

	if len(input) != 0 {
		command.Stdin = bytes.NewReader(input)
	}
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func kubectl(args ...string) ([]byte, []byte, error) {
	return execWrapper("kubectl", nil, args...)
}

func docker(args ...string) ([]byte, []byte, error) {
	return execWrapper("docker", nil, args...)
}

func imagePinned(node, image string) (bool, error) {
	stdout, stderr, err := docker("exec", node, "crictl", "inspecti", "-o", "json", image)
	if err != nil {
		return false, fmt.Errorf("crictl inspecti %s on %s failed: %s: %w", image, node, string(stderr), err)
	}

	var result struct {
		Status struct {
			Pinned bool `json:"pinned"`
		} `json:"status"`
	}
	if err := json.Unmarshal(stdout, &result); err != nil {
		return false, fmt.Errorf("failed to parse crictl inspecti output on %s: %w", node, err)
	}
	return result.Status.Pinned, nil
}

func GetNonEmptyLines(output string) []string {
	var res []string
	elements := strings.Split(output, "\n")
	for _, element := range elements {
		if element != "" {
			res = append(res, element)
		}
	}

	return res
}

var _ = Describe("controller", Ordered, func() {
	BeforeAll(func() {
		By("creating the test namespace")
		_, stderr, err := kubectl("create", "namespace", testNamespace)
		Expect(err).NotTo(HaveOccurred(), "stderr:%s", string(stderr))
	})

	AfterAll(func() {
		By("deleting the test namespace")
		_, _, _ = kubectl("delete", "namespace", testNamespace, "--ignore-not-found", "--interactive=false")
	})

	Context("Operator", func() {
		It("should run successfully", func() {
			var controllerPodName string

			By("validating that the imageprefetch-controller pod is running as expected")
			verifyControllerUp := func() error {
				// Get pod name
				stdout, stderr, err := kubectl("get", "-n", namespace,
					"pods", "-l", "control-plane=imageprefetch-controller",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)
				Expect(err).NotTo(HaveOccurred(), "stderr:"+string(stderr))
				podNames := GetNonEmptyLines(string(stdout))
				if len(podNames) != 1 {
					return fmt.Errorf("expect 1 controller pods running, but got %d", len(podNames))
				}
				controllerPodName = podNames[0]
				ExpectWithOffset(2, controllerPodName).Should(ContainSubstring("imageprefetch-controller"))

				// Validate pod status
				stdout, stderr, err = kubectl("get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace)
				Expect(err).NotTo(HaveOccurred(), "stderr:"+string(stderr))
				if string(stdout) != "Running" {
					return fmt.Errorf("controller pod in %s status", stdout)
				}
				return nil
			}
			EventuallyWithOffset(1, verifyControllerUp, time.Minute, time.Second).Should(Succeed())
		})
	})

	Context("ImagePrefetch pinning", func() {
		const imagePrefetchName = "e2e-pin-test"

		It("should pin the prefetched image on all target nodes", func() {
			By("applying the ImagePrefetch resource")
			stdout, stderr, err := kubectl("apply", "-f", "testdata/imageprefetch.yaml")
			Expect(err).NotTo(HaveOccurred(), "stdout:%s stderr:%s", string(stdout), string(stderr))

			By("waiting for the ImagePrefetch to become Ready")
			Eventually(func() error {
				stdout, stderr, err := kubectl("get", "imageprefetch", imagePrefetchName, "-n", testNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				if err != nil {
					return fmt.Errorf("failed to get ImagePrefetch status: %s: %w", string(stderr), err)
				}
				if string(stdout) != "True" {
					return fmt.Errorf("ImagePrefetch is not Ready yet, condition status: %q", string(stdout))
				}
				return nil
			}).WithTimeout(5 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

			By("collecting the nodes selected for prefetching")
			stdout, stderr, err = kubectl("get", "imageprefetch", imagePrefetchName, "-n", testNamespace,
				"-o", "jsonpath={.status.selectedNodes[*]}")
			Expect(err).NotTo(HaveOccurred(), "stderr:%s", string(stderr))
			nodes := strings.Fields(string(stdout))
			Expect(nodes).NotTo(BeEmpty(), "expected at least one selected node")

			By("verifying the prefetched image is pinned on every selected node")
			Eventually(func() error {
				for _, node := range nodes {
					pinned, err := imagePinned(node, pinnedTestImage)
					if err != nil {
						return err
					}
					if !pinned {
						return fmt.Errorf("image %s is not pinned on node %s", pinnedTestImage, node)
					}
				}
				return nil
			}).WithTimeout(time.Minute).WithPolling(5 * time.Second).Should(Succeed())
		})

		It("should unpin the image once a Pod uses it", func() {
			const podName = "e2e-pin-consumer"

			DeferCleanup(func() {
				By("deleting the consumer Pod")
				_, _, _ = kubectl("delete", "-f", "testdata/pod.yaml", "--ignore-not-found", "--force", "--grace-period=0", "--interactive=false")
			})

			By("creating a Pod that uses the prefetched image")
			stdout, stderr, err := kubectl("apply", "-f", "testdata/pod.yaml")
			Expect(err).NotTo(HaveOccurred(), "stdout:%s stderr:%s", string(stdout), string(stderr))

			By("waiting for the Pod to be running")
			Eventually(func() error {
				stdout, stderr, err := kubectl("get", "pod", podName, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				if err != nil {
					return fmt.Errorf("failed to get Pod status: %s: %w", string(stderr), err)
				}
				if string(stdout) != "Running" {
					return fmt.Errorf("Pod is not running yet, phase: %q", string(stdout))
				}
				return nil
			}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

			By("identifying the node the Pod is running on")
			stdout, stderr, err = kubectl("get", "pod", podName, "-n", testNamespace,
				"-o", "jsonpath={.spec.nodeName}")
			Expect(err).NotTo(HaveOccurred(), "stderr:%s", string(stderr))
			targetNode := strings.TrimSpace(string(stdout))
			Expect(targetNode).NotTo(BeEmpty(), "expected the Pod to be scheduled on a node")

			By("verifying the in-use image is unpinned on the node")
			Eventually(func() error {
				pinned, err := imagePinned(targetNode, pinnedTestImage)
				if err != nil {
					return err
				}
				if pinned {
					return fmt.Errorf("image %s is still pinned on node %s", pinnedTestImage, targetNode)
				}
				return nil
			}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
		})
	})

	Context("ImagePrefetch unpin on deletion", func() {
		const imagePrefetchName = "e2e-unpin-test"

		It("should unpin the image when the ImagePrefetch is deleted before use", func() {
			By("applying the ImagePrefetch resource")
			stdout, stderr, err := kubectl("apply", "-f", "testdata/imageprefetch-delete.yaml")
			Expect(err).NotTo(HaveOccurred(), "stdout:%s stderr:%s", string(stdout), string(stderr))

			By("waiting for the ImagePrefetch to become Ready")
			Eventually(func() error {
				stdout, stderr, err := kubectl("get", "imageprefetch", imagePrefetchName, "-n", testNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				if err != nil {
					return fmt.Errorf("failed to get ImagePrefetch status: %s: %w", string(stderr), err)
				}
				if string(stdout) != "True" {
					return fmt.Errorf("ImagePrefetch is not Ready yet, condition status: %q", string(stdout))
				}
				return nil
			}).WithTimeout(5 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

			By("collecting the nodes selected for prefetching")
			stdout, stderr, err = kubectl("get", "imageprefetch", imagePrefetchName, "-n", testNamespace,
				"-o", "jsonpath={.status.selectedNodes[*]}")
			Expect(err).NotTo(HaveOccurred(), "stderr:%s", string(stderr))
			nodes := strings.Fields(string(stdout))
			Expect(nodes).NotTo(BeEmpty(), "expected at least one selected node")

			By("verifying the prefetched image is pinned on every selected node")
			Eventually(func() error {
				for _, node := range nodes {
					pinned, err := imagePinned(node, deleteTestImage)
					if err != nil {
						return err
					}
					if !pinned {
						return fmt.Errorf("image %s is not pinned on node %s", deleteTestImage, node)
					}
				}
				return nil
			}).WithTimeout(time.Minute).WithPolling(5 * time.Second).Should(Succeed())

			By("deleting the ImagePrefetch resource before the image is used")
			stdout, stderr, err = kubectl("delete", "-f", "testdata/imageprefetch-delete.yaml", "--interactive=false")
			Expect(err).NotTo(HaveOccurred(), "stdout:%s stderr:%s", string(stdout), string(stderr))

			By("verifying the image is unpinned on every selected node")
			Eventually(func() error {
				for _, node := range nodes {
					pinned, err := imagePinned(node, deleteTestImage)
					if err != nil {
						return err
					}
					if pinned {
						return fmt.Errorf("image %s is still pinned on node %s", deleteTestImage, node)
					}
				}
				return nil
			}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
		})
	})
})

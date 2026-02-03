package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
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

func (b *nodeImageSetBuilder) WithFinalizers(finalizers []string) *nodeImageSetBuilder {
	b.object.Finalizers = finalizers
	return b
}

func (b *nodeImageSetBuilder) build() *ofenv1.NodeImageSet {
	return b.object
}

type nodeBuilder struct {
	object *corev1.Node
}

func createNode(name string) *nodeBuilder {
	return &nodeBuilder{
		object: &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

func (nodeb *nodeBuilder) Build() *corev1.Node {
	return nodeb.object
}

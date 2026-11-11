package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

const (
	metricsNamespace = "ofen"

	labelNamespace      = "namespace"
	labelImagePrefetch  = "imageprefetch"
	labelNodeImageSet   = "nodeimageset"
	labelImage          = "image"
	labelRegistryPolicy = "registry_policy"
	labelNode           = "node"
)

var (
	ReadyVec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "imageprefetch_ready",
		Help:      "1 if the ImagePrefetch resource is ready, 0 otherwise",
	}, []string{labelNamespace, labelImagePrefetch})

	ImagePulledNodesVec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "imageprefetch_image_pull_successful_nodes",
		Help:      "Number of nodes where images have been successfully prefetched for thins Imageprefetch",
	}, []string{labelNamespace, labelImagePrefetch})

	ImagePullFailedNodesVec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "imageprefetch_image_pull_failed_nodes",
		Help:      "Number of nodes where image prefetching has failed for this ImagePrefetch",
	}, []string{labelNamespace, labelImagePrefetch})

	ImageInfoVec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "nodeimageset_image_info",
		Help:      "Information about NodeImageSet image",
	}, []string{labelNodeImageSet, labelImage, labelRegistryPolicy, labelNode})

	ImageSizeBytesVec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "nodeimageset_image_size_bytes",
		Help:      "Size of images in NodeImageSets in bytes",
	}, []string{labelNodeImageSet, labelImage, labelNode})

	ImagePrefetchDurationSecondsVec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "nodeimageset_image_prefetch_duration_seconds",
		Help:      "Duration taken to prefetch images in NodeImageSets in seconds",
	}, []string{labelNodeImageSet, labelImage, labelNode})
)

func Register(registry prometheus.Registerer) {
	registry.MustRegister(ReadyVec)
	registry.MustRegister(ImagePulledNodesVec)
	registry.MustRegister(ImagePullFailedNodesVec)
	registry.MustRegister(ImageInfoVec)
	registry.MustRegister(ImageSizeBytesVec)
	registry.MustRegister(ImagePrefetchDurationSecondsVec)
}

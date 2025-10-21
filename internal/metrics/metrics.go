package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

const (
	metricsNamespace = "ofen"
)

var (
	ReadyVec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "imageprefetch_ready",
		Help:      "1 if the ImagePrefetch resource is ready, 0 otherwise",
	}, []string{"namespace", "name"})

	ImagePulledNodesVec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "imageprefetch_image_pull_successful_nodes",
		Help:      "Number of nodes where images have been successfully prefetched for thins Imageprefetch",
	}, []string{"namespace", "name"})

	ImagePullFailedNodesVec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "imageprefetch_image_pull_failed_nodes",
		Help:      "Number of nodes where image prefetching has failed for this ImagePrefetch",
	}, []string{"namespace", "name"})

	ImageInfoVec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "nodeimageset_image_info",
		Help:      "Information about NodeImageSet image",
	}, []string{"name", "image_name", "registry_policy", "node_name"})

	ImageSizeBytesVec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "nodeimageset_image_size_bytes",
		Help:      "Size of images in NodeImageSets in bytes",
	}, []string{"name", "image_name", "node_name"})

	ImagePrefetchDurationSecondsVec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "nodeimageset_image_prefetch_duration_seconds",
		Help:      "Duration taken to prefetch images in NodeImageSets in seconds",
	}, []string{"name", "image_name", "node_name"})
)

func Register(registry prometheus.Registerer) {
	registry.MustRegister(ReadyVec)
	registry.MustRegister(ImagePulledNodesVec)
	registry.MustRegister(ImagePullFailedNodesVec)
	registry.MustRegister(ImageInfoVec)
	registry.MustRegister(ImageSizeBytesVec)
	registry.MustRegister(ImagePrefetchDurationSecondsVec)
}

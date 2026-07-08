package constants

const ImagePrefetchPrefix = "image-prefetch.ofen.cybozu.io/"

const (
	ImagePrefetchFieldManager   = ImagePrefetchPrefix + "image-prefetch-controller"
	ImagePrefetchFinalizer      = ImagePrefetchPrefix + "finalizer"
	NodeName                    = ImagePrefetchPrefix + "node-name"
	OwnerImagePrefetchNamespace = ImagePrefetchPrefix + "owner-namespace"
	OwnerImagePrefetchName      = ImagePrefetchPrefix + "owner-name"
)

const NodeImageSetPrefix = "nodeimageset.ofen.cybozu.io/"

const (
	NodeImageSetNamePrefix   = "nodeimageset"
	NodeImageSetFinalizer    = NodeImageSetPrefix + "finalizer"
	NodeImageSetFieldManager = NodeImageSetPrefix + "nodeimageset-controller"
)

const (
	DockerConfigName = ".dockerconfigjson"
	DockerCfgName    = ".dockercfg"
)

const (
	// CRIPinnedLabelKey and CRIPinnedLabelValue match the label that the containerd
	// CRI plugin uses to mark an image as pinned. An image carrying this label is
	// reported to kubelet as pinned, and kubelet's image garbage collection skips it.
	CRIPinnedLabelKey   = "io.cri-containerd.pinned"
	CRIPinnedLabelValue = "pinned"

	// OfenPinnedLabelKey and OfenPinnedLabelValue mark images that ofen itself pinned.
	// ofen only ever unpins images carrying this marker so that it never touches pins
	// managed by containerd (e.g. the sandbox/pause image).
	OfenPinnedLabelKey   = ImagePrefetchPrefix + "pinned"
	OfenPinnedLabelValue = "true"
)

package constants

const ImagePrefetchPrefix = "image-prefetch.ofen.cybozu.io/"

const (
	ImagePrefetchFieldManager    = ImagePrefetchPrefix + "image-prefetch-controller"
	ImagePrefetchFinalizer       = ImagePrefetchPrefix + "finalizer"
	NodeName                     = "nodeName"
	DefaultImagePrefetchReplicas = 2
	OwnerImagePrefetchNamespace  = ImagePrefetchPrefix + "owner-namespace"
	OwnerImagePrefetchName       = ImagePrefetchPrefix + "owner-name"
)

const (
	NodeImageSetPrefix = "nodeimageset"
)

package imgmanager

import (
	"context"
	"fmt"

	containerdclient "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/diff"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/core/remotes/docker/config"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/image-spec/identity"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
)

type ContainerdClient interface {
	IsImageExists(ctx context.Context, ref string) (bool, error)
	PullImage(ctx context.Context, ref string, policy ofenv1.RegistryPolicy, secrets *[]corev1.Secret) (int64, error)
	Subscribe(ctx context.Context) (<-chan *events.Envelope, <-chan error)
	// InUseImages returns the subset of refs that currently back a container on the node.
	InUseImages(ctx context.Context, refs []string) ([]string, error)
	// UnpinImage removes the pin that ofen placed on the image. It is a no-op for
	// images that ofen did not pin.
	UnpinImage(ctx context.Context, ref string) error
	// ListPinnedImages returns the names of images that ofen has pinned.
	ListPinnedImages(ctx context.Context) ([]string, error)
}

type ContainerdConfig struct {
	SockAddr  string
	Namespace string
	HostDir   string
}

type Containerd struct {
	client           *containerdclient.Client
	containerdConfig *ContainerdConfig
}

func NewContainerd(containerdConfig *ContainerdConfig, client *containerdclient.Client) *Containerd {
	return &Containerd{
		containerdConfig: containerdConfig,
		client:           client,
	}
}

func (c *Containerd) IsImageExists(ctx context.Context, ref string) (bool, error) {
	ctx = namespaces.WithNamespace(ctx, c.containerdConfig.Namespace)
	filter := fmt.Sprintf("name==%s", ref)
	images, err := c.client.ListImages(ctx, filter)
	if err != nil {
		return false, fmt.Errorf("failed to list images: %w", err)
	}

	return len(images) != 0, nil
}

func (c *Containerd) PullImage(ctx context.Context, ref string, policy ofenv1.RegistryPolicy, secrets *[]corev1.Secret) (int64, error) {
	ctx = namespaces.WithNamespace(ctx, c.containerdConfig.Namespace)

	tokens := map[string]Credentials{}
	if secrets != nil && len(*secrets) > 0 {
		var err error
		tokens, err = convertCredentials(*secrets)
		if err != nil {
			return 0, fmt.Errorf("failed to convert credentials: %w", err)
		}
	}

	var useMirrorOnly bool
	switch policy {
	case ofenv1.RegistryPolicyDefault:
		useMirrorOnly = false
	case ofenv1.RegistryPolicyMirrorOnly:
		useMirrorOnly = true
	default:
		return 0, fmt.Errorf("unknown registry policy %q", policy)
	}

	resolver := c.setupResolver(ctx, useMirrorOnly, tokens)
	pullOptions := []containerdclient.RemoteOpt{
		containerdclient.WithPullUnpack,
		containerdclient.WithResolver(resolver),
		containerdclient.WithUnpackOpts([]containerdclient.UnpackOpt{
			containerdclient.WithUnpackApplyOpts(diff.WithSyncFs(true)), // force sync fs
		}),
		// Pin the image so kubelet's image garbage collection does not delete it
		// before a Pod uses it. The pin is removed once the image is in use
		// (see InUseImages/UnpinImage), after which kubelet manages it normally.
		containerdclient.WithPullLabels(map[string]string{
			constants.CRIPinnedLabelKey:  constants.CRIPinnedLabelValue,
			constants.OfenPinnedLabelKey: constants.OfenPinnedLabelValue,
		}),
	}

	image, err := c.client.Pull(ctx, ref, pullOptions...)
	if err != nil {
		return 0, fmt.Errorf("failed to pull image %s: %w", ref, err)
	}

	size, err := image.Size(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get image size for %s: %w", ref, err)
	}
	return size, nil
}

func (c *Containerd) setupResolver(ctx context.Context, useMirrorOnly bool, tokens map[string]Credentials) remotes.Resolver {
	hostOpt := config.HostOptions{
		HostDir:     config.HostDirFromRoot(c.containerdConfig.HostDir),
		Credentials: credentials(tokens),
	}
	resolveOpt := docker.ResolverOptions{
		Hosts: config.ConfigureHosts(ctx, hostOpt),
	}

	if useMirrorOnly {
		resolveOpt.Hosts = registryMirrorHosts(ctx, hostOpt)
	}

	return docker.NewResolver(resolveOpt)
}

func registryMirrorHosts(ctx context.Context, hostOpt config.HostOptions) docker.RegistryHosts {
	logger := log.FromContext(ctx)

	return func(host string) ([]docker.RegistryHost, error) {
		logger.Info("skipping upstream registry due to mirror-only policy", "host", host)
		hosts := config.ConfigureHosts(ctx, hostOpt)
		rhosts, err := hosts(host)
		if err != nil {
			logger.Error(err, "failed to get registry hosts", "host", host)
			return nil, err
		}

		mirrorHosts := []docker.RegistryHost{}
		for _, rhost := range rhosts {
			if rhost.Host == host {
				continue
			}
			if !rhost.Capabilities.Has(docker.HostCapabilityPull) {
				logger.Info("skipping registry host without pull capability", "host", rhost.Host)
				continue
			}
			mirrorHosts = append(mirrorHosts, rhost)
		}
		return mirrorHosts, nil
	}
}

func credentials(tokens map[string]Credentials) func(host string) (string, string, error) {
	return func(host string) (string, string, error) {
		if h, ok := tokens[host]; ok {
			return h.Username, h.Password, nil
		}

		return "", "", nil
	}
}

// InUseImages returns the subset of refs that are currently used by a container on
// the node. CRI-created containers do not populate the containerd container's Image
// field, so usage is detected by matching each container's rootfs snapshot parent
// (the image chain ID) against the chain ID of each candidate image.
func (c *Containerd) InUseImages(ctx context.Context, refs []string) ([]string, error) {
	ctx = namespaces.WithNamespace(ctx, c.containerdConfig.Namespace)

	containers, err := c.client.Containers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	usedChainIDs := map[string]struct{}{}
	for _, cont := range containers {
		info, err := cont.Info(ctx)
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to get container info", "container", cont.ID())
			continue
		}
		if info.SnapshotKey == "" || info.Snapshotter == "" {
			continue
		}
		stat, err := c.client.SnapshotService(info.Snapshotter).Stat(ctx, info.SnapshotKey)
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to stat snapshot", "snapshotKey", info.SnapshotKey)
			continue
		}
		if stat.Parent != "" {
			usedChainIDs[stat.Parent] = struct{}{}
		}
	}

	var inUse []string
	for _, ref := range refs {
		image, err := c.client.GetImage(ctx, ref)
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to get image", "image", ref)
			continue
		}
		diffIDs, err := image.RootFS(ctx)
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to get image rootfs", "image", ref)
			continue
		}
		chainID := identity.ChainID(diffIDs).String()
		if _, ok := usedChainIDs[chainID]; ok {
			inUse = append(inUse, ref)
		}
	}
	return inUse, nil
}

// UnpinImage removes both the CRI pinned label and ofen's own pin marker so that
// kubelet's image garbage collection can manage the image again. Images that ofen
// did not pin are left untouched.
func (c *Containerd) UnpinImage(ctx context.Context, ref string) error {
	ctx = namespaces.WithNamespace(ctx, c.containerdConfig.Namespace)

	is := c.client.ImageService()
	image, err := is.Get(ctx, ref)
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get image %s: %w", ref, err)
	}

	if image.Labels[constants.OfenPinnedLabelKey] != constants.OfenPinnedLabelValue {
		// Not pinned by ofen; do not touch it.
		return nil
	}

	delete(image.Labels, constants.CRIPinnedLabelKey)
	delete(image.Labels, constants.OfenPinnedLabelKey)
	if _, err := is.Update(ctx, image, "labels"); err != nil {
		return fmt.Errorf("failed to unpin image %s: %w", ref, err)
	}
	return nil
}

// ListPinnedImages returns the names of images that ofen has pinned.
func (c *Containerd) ListPinnedImages(ctx context.Context) ([]string, error) {
	ctx = namespaces.WithNamespace(ctx, c.containerdConfig.Namespace)

	filter := fmt.Sprintf("labels.%q==%s", constants.OfenPinnedLabelKey, constants.OfenPinnedLabelValue)
	images, err := c.client.ListImages(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list pinned images: %w", err)
	}

	names := make([]string, 0, len(images))
	for _, image := range images {
		names = append(names, image.Name())
	}
	return names, nil
}

func (c *Containerd) Subscribe(ctx context.Context) (<-chan *events.Envelope, <-chan error) {
	filters := generateEventFilter()
	eventsCh, errCh := c.client.EventService().Subscribe(ctx, filters...)
	return eventsCh, errCh
}

func generateEventFilter() []string {
	baseFilter := `topic~="/images/delete"`
	return []string{baseFilter}
}

package imgmanager

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/diff"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/core/remotes/docker/config"
	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (c *Containerd) IsImageExists(ctx context.Context, ref string) (bool, error) {
	ctx = namespaces.WithNamespace(ctx, c.config.Namespace)
	filter := fmt.Sprintf("name==%s", ref)
	images, err := c.client.ListImages(ctx, filter)
	if err != nil {
		return false, fmt.Errorf("failed to list images: %w", err)
	}

	return len(images) != 0, nil
}

func (c *Containerd) PullImage(ctx context.Context, ref string, policy ofenv1.RegistryPolicy) error {
	ctx = namespaces.WithNamespace(ctx, c.config.Namespace)

	var useMirrorOnly bool
	switch policy {
	case ofenv1.RegistryPolicyDefault:
		useMirrorOnly = false
	case ofenv1.RegistryPolicyMirrorOnly:
		useMirrorOnly = true
	default:
		return fmt.Errorf("unknown registry policy %q", policy)
	}

	resolver := c.setupResolver(ctx, useMirrorOnly)
	pullOptions := []client.RemoteOpt{
		client.WithPullUnpack,
		client.WithResolver(resolver),
		client.WithUnpackOpts([]client.UnpackOpt{
			client.WithUnpackApplyOpts(diff.WithSyncFs(true)), // force sync fs
		}),
	}

	_, err := c.client.Pull(ctx, ref, pullOptions...)
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", ref, err)
	}

	//status, err := img.ContentStore().Status(ctx, ref)
	//if err != nil {
	//	return fmt.Errorf("failed to get image status: %w", err)
	//}
	//if status.Expected != img.Target().Digest {
	//	return fmt.Errorf("unexpected image digest: %s", img.Target().Digest)
	//}

	return nil
}

func (c *Containerd) setupResolver(ctx context.Context, useMirrorOnly bool) remotes.Resolver {
	ctx = namespaces.WithNamespace(ctx, c.config.Namespace)
	hostOpt := config.HostOptions{
		HostDir:     config.HostDirFromRoot(c.config.HostDir),
		Credentials: c.credentials(),
	}
	resolveOpt := docker.ResolverOptions{
		Hosts: config.ConfigureHosts(ctx, hostOpt),
	}

	//if useMirrorOnly {
	//	resolveOpt.Hosts = registryMirrorHosts(ctx, hostOpt)
	//}

	return docker.NewResolver(resolveOpt)
}

func (c *Containerd) credentials() func(host string) (string, string, error) {
	return func(host string) (string, string, error) {
		if h, ok := c.tokens[host]; ok {
			return h.Username, h.Password, nil
		}

		return "", "", nil
	}
}

func registryMirrorHosts(ctx context.Context, hostOpt config.HostOptions) docker.RegistryHosts {
	logger := log.FromContext(ctx)

	return func(host string) ([]docker.RegistryHost, error) {
		logger.Info("skipping upstream registry as mirror-only policy is set", "host", host)
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
			mirrorHosts = append(mirrorHosts, rhost)
		}
		return mirrorHosts, nil
	}
}

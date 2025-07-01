package imgmanager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/containerd/containerd/namespaces"
	containerdclient "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/diff"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/core/remotes/docker/config"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/constants"
)

type ContainerdClient interface {
	IsImageExists(ctx context.Context, ref string) (bool, error)
	PullImage(ctx context.Context, ref string, policy ofenv1.RegistryPolicy, secrets *[]corev1.Secret) error
	Subscribe(ctx context.Context) (<-chan *events.Envelope, <-chan error)
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

type Credentials struct {
	Username string
	Password string
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

func (c *Containerd) PullImage(ctx context.Context, ref string, policy ofenv1.RegistryPolicy, secrets *[]corev1.Secret) error {
	ctx = namespaces.WithNamespace(ctx, c.containerdConfig.Namespace)

	tokens := map[string]Credentials{}
	if secrets != nil && len(*secrets) > 0 {
		var err error
		tokens, err = c.convertCredentials(*secrets)
		if err != nil {
			return fmt.Errorf("failed to convert credentials: %w", err)
		}
	}

	var useMirrorOnly bool
	switch policy {
	case ofenv1.RegistryPolicyDefault:
		useMirrorOnly = false
	case ofenv1.RegistryPolicyMirrorOnly:
		useMirrorOnly = true
	default:
		return fmt.Errorf("unknown registry policy %q", policy)
	}

	resolver := c.setupResolver(ctx, useMirrorOnly, tokens)
	pullOptions := []containerdclient.RemoteOpt{
		containerdclient.WithPullUnpack,
		containerdclient.WithResolver(resolver),
		containerdclient.WithUnpackOpts([]containerdclient.UnpackOpt{
			containerdclient.WithUnpackApplyOpts(diff.WithSyncFs(true)), // force sync fs
		}),
	}

	_, err := c.client.Pull(ctx, ref, pullOptions...)
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", ref, err)
	}

	return nil
}

func (c *Containerd) setupResolver(ctx context.Context, useMirrorOnly bool, tokens map[string]Credentials) remotes.Resolver {
	ctx = namespaces.WithNamespace(ctx, c.containerdConfig.Namespace)
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

type DockerConfig struct {
	Auths map[string]struct {
		Auth string `json:"auth"`
	} `json:"auths"`
}

func (c *Containerd) convertCredentials(secrets []corev1.Secret) (map[string]Credentials, error) {
	tokens := map[string]Credentials{}
	for _, secret := range secrets {
		var dockerConfig DockerConfig

		data := secret.Data[constants.DockerConfigName]
		if err := json.Unmarshal(data, &dockerConfig); err != nil {
			return tokens, fmt.Errorf("failed to unmarshal data %w", err)
		}

		for registry, auth := range dockerConfig.Auths {
			data, err := base64.StdEncoding.DecodeString(auth.Auth)
			if err != nil {
				return tokens, fmt.Errorf("failed to decode auth %s: %w", auth.Auth, err)
			}

			username, password, ok := strings.Cut(string(data), ":")
			if !ok {
				return tokens, fmt.Errorf("failed to found username and password in auth %s", auth.Auth)
			}
			tokens[registry] = Credentials{
				Username: username,
				Password: password,
			}
		}
	}

	return tokens, nil
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

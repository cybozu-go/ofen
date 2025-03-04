package imgmanager

import (
	"context"

	containerdclient "github.com/containerd/containerd/v2/client"
)

type ContainerdConfig struct {
	SockAddr  string
	Namespace string
	HostDir   string
}

type Containerd struct {
	ctx    context.Context
	client *containerdclient.Client
	config *ContainerdConfig
	tokens map[string]Credentials
}

type Credentials struct {
	Username string
	Password string
}

func NewContainerd(ctx context.Context, config *ContainerdConfig, client *containerdclient.Client) *Containerd {
	return &Containerd{
		ctx:    ctx,
		config: config,
		client: client,
	}
}

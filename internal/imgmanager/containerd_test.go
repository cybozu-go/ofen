package imgmanager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/remotes/docker/config"
	"github.com/stretchr/testify/require"
)

func TestGenerateEventFilter(t *testing.T) {
	t.Parallel()

	expected := []string{`topic~="/images/delete"`}
	filter := generateEventFilter()
	require.Equal(t, expected, filter)
}

func TestCredentials(t *testing.T) {
	t.Parallel()

	tokens := map[string]Credentials{
		"registry.example.com": {
			Username: "user",
			Password: "pass",
		},
	}

	credFunc := credentials(tokens)

	user, pass, err := credFunc("registry.example.com")
	require.NoError(t, err)
	require.Equal(t, "user", user)
	require.Equal(t, "pass", pass)

	user, pass, err = credFunc("unknown.registry.com")
	require.NoError(t, err)
	require.Equal(t, "", user)
	require.Equal(t, "", pass)
}

func TestRegistryMirrorHosts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tests := []struct {
		name           string
		registryName   string
		registryConfig string
		expectedHosts  []string
	}{
		{
			name:         "valid registry with pull capability",
			registryName: "test.registry.example",
			registryConfig: `
server = "http://test.registry.example"
[host."http://localhost:5000"]
  capabilities = ["pull"]
`,
			expectedHosts: []string{"localhost:5000"},
		},
		{
			name:         "valid registry with multiple hosts",
			registryName: "test.registry.example",
			registryConfig: `
server = "http://test.registry.example"
[host."http://localhost:5000"]
  capabilities = ["pull"]
[host."http://localhost:6000"]
  capabilities = ["pull"]
`,
			expectedHosts: []string{"localhost:5000", "localhost:6000"},
		},
		{
			name:         "valid registry with pull and resolve capabilities",
			registryName: "test.registry.example",
			registryConfig: `
server = "http://test.registry.example"
[host."http://localhost:5000"]
  capabilities = ["pull", "resolve"]
`,
			expectedHosts: []string{"localhost:5000"},
		},
		{
			name:         "valid registry with no pull capability",
			registryName: "test.registry.example",
			registryConfig: `
server = "http://test.registry.example"
[host."http://localhost:5000"]
  capabilities = ["push"]
`,
			expectedHosts: []string{},
		},
		{
			name:         "no mirror hosts",
			registryName: "test.registry.example",
			registryConfig: `
server = "http://test.registry.example"
`,
			expectedHosts: []string{},
		},
		{
			name:           "no registry config",
			registryName:   "test.registry.example",
			registryConfig: "",
			expectedHosts:  []string{},
		},
		{
			name:         "invalid registry config",
			registryName: "test.registry.example",
			registryConfig: `
server = "http://test.registry.example"
[host."http://localhost:5000"]
  capabilities = ["invalid"]
`,
			// containerd does not return an error when parsing hosts.toml fails, it only outputs to the log.
			// https://github.com/containerd/containerd/blob/v2.1.1/core/remotes/docker/config/hosts.go#L327-L332
			expectedHosts: []string{},
		},
		{
			name:         "invalid toml syntax",
			registryName: "test.registry.example",
			registryConfig: `
server = "http://test.registry.example"
[host."http://localhost:5000"]
  capabilities = ["pull"
`,
			// containerd does not return an error when parsing hosts.toml fails, it only outputs to the log.
			// https://github.com/containerd/containerd/blob/v2.1.1/core/remotes/docker/config/hosts.go#L327-L332
			expectedHosts: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmpDir, err := createRegistryMirrorHostsFile(tt.registryName, tt.registryConfig)
			require.NoError(t, err)
			defer os.RemoveAll(tmpDir)

			hosts := registryMirrorHosts(ctx, config.HostOptions{
				HostDir: config.HostDirFromRoot(tmpDir),
			})
			require.NotNil(t, hosts)
			mirrorHosts, err := hosts(tt.registryName)
			require.NoError(t, err)
			require.Len(t, mirrorHosts, len(tt.expectedHosts))
			for i, host := range mirrorHosts {
				require.Equal(t, tt.expectedHosts[i], host.Host)
			}
		})
	}

}

func createRegistryMirrorHostsFile(registryName string, registryConfig string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "containerd_test")
	if err != nil {
		return "", err
	}

	registryDir := filepath.Join(tmpDir, registryName)
	err = os.MkdirAll(registryDir, 0755)
	if err != nil {
		return "", err
	}

	tmpFile, err := os.Create(filepath.Join(registryDir, "hosts.toml"))
	if err != nil {
		return "", err
	}
	defer tmpFile.Close()

	_, err = tmpFile.WriteString(registryConfig)
	if err != nil {
		return "", err
	}

	return tmpDir, nil
}

func TestNewContainerd(t *testing.T) {
	t.Parallel()

	cfg := &ContainerdConfig{
		SockAddr:  "/run/containerd/containerd.sock",
		Namespace: "test-namespace",
		HostDir:   "/etc/containerd/certs.d",
	}

	c := NewContainerd(cfg, nil)

	require.NotNil(t, c)
	require.Equal(t, cfg, c.containerdConfig)
	require.Nil(t, c.client)
}

func TestSetupResolver(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := &ContainerdConfig{
		Namespace: "test",
		HostDir:   "/tmp",
	}
	c := NewContainerd(cfg, nil)

	tokens := map[string]Credentials{
		"registry.example.com": {
			Username: "user",
			Password: "pass",
		},
	}

	resolver := c.setupResolver(ctx, false, tokens)
	require.NotNil(t, resolver)

	resolverMirrorOnly := c.setupResolver(ctx, true, tokens)
	require.NotNil(t, resolverMirrorOnly)
}

package imgmanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/events"
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

// requireSubscriptionClosed asserts that both relayed channels get closed and
// returns the error reported on the error channel, if any.
func requireSubscriptionClosed(t *testing.T, eventsCh <-chan *events.Envelope, errCh <-chan error) error {
	t.Helper()

	timeout := time.After(5 * time.Second)

	select {
	case event, ok := <-eventsCh:
		require.False(t, ok, "unexpected event: %v", event)
	case <-timeout:
		require.FailNow(t, "event channel was not closed")
	}

	var reported error
	for {
		select {
		case err, ok := <-errCh:
			if !ok {
				return reported
			}
			require.NoError(t, reported, "more than one error was reported")
			reported = err
		case <-timeout:
			require.FailNow(t, "error channel was not closed")
		}
	}
}

func TestRelaySubscriptionForwardsEvents(t *testing.T) {
	t.Parallel()

	srcEventsCh := make(chan *events.Envelope)
	srcErrCh := make(chan error)
	eventsCh, _ := relaySubscription(t.Context(), srcEventsCh, srcErrCh)

	for _, topic := range []string{"/images/delete", "/images/delete/again"} {
		sent := &events.Envelope{Topic: topic}
		select {
		case srcEventsCh <- sent:
		case <-time.After(5 * time.Second):
			require.FailNow(t, "event was not consumed")
		}

		select {
		case received, ok := <-eventsCh:
			require.True(t, ok)
			require.Same(t, sent, received)
		case <-time.After(5 * time.Second):
			require.FailNow(t, "event was not forwarded")
		}
	}
}

// The containerd client leaves its event channel open when the subscription
// ends, so srcEventsCh is never closed in the following tests.

func TestRelaySubscriptionForwardsErrorAndCloses(t *testing.T) {
	t.Parallel()

	srcEventsCh := make(chan *events.Envelope)
	srcErrCh := make(chan error, 1)
	eventsCh, errCh := relaySubscription(t.Context(), srcEventsCh, srcErrCh)

	srcErr := errors.New("failed to dial containerd socket")
	srcErrCh <- srcErr
	close(srcErrCh)

	require.ErrorIs(t, requireSubscriptionClosed(t, eventsCh, errCh), srcErr)
}

func TestRelaySubscriptionClosesWhenErrorChannelCloses(t *testing.T) {
	t.Parallel()

	srcEventsCh := make(chan *events.Envelope)
	srcErrCh := make(chan error)
	eventsCh, errCh := relaySubscription(t.Context(), srcEventsCh, srcErrCh)

	close(srcErrCh)

	require.NoError(t, requireSubscriptionClosed(t, eventsCh, errCh))
}

func TestRelaySubscriptionClosesOnContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	srcEventsCh := make(chan *events.Envelope)
	srcErrCh := make(chan error)
	eventsCh, errCh := relaySubscription(ctx, srcEventsCh, srcErrCh)

	cancel()

	require.NoError(t, requireSubscriptionClosed(t, eventsCh, errCh))
}

func TestRelaySubscriptionClosesOnContextCancellationWithPendingEvent(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	srcEventsCh := make(chan *events.Envelope)
	srcErrCh := make(chan error)
	eventsCh, errCh := relaySubscription(ctx, srcEventsCh, srcErrCh)

	// Nobody reads eventsCh, so the relay is blocked forwarding this event.
	select {
	case srcEventsCh <- &events.Envelope{Topic: "/images/delete"}:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "event was not consumed")
	}

	cancel()

	// The pending event may or may not be delivered before the channel closes.
	select {
	case _, ok := <-eventsCh:
		if !ok {
			eventsCh = nil
		}
	case <-time.After(5 * time.Second):
		require.FailNow(t, "event channel was not closed")
	}
	if eventsCh != nil {
		require.NoError(t, requireSubscriptionClosed(t, eventsCh, errCh))
		return
	}
	select {
	case err, ok := <-errCh:
		require.False(t, ok, "unexpected error: %v", err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "error channel was not closed")
	}
}

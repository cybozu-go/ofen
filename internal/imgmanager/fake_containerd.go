package imgmanager

import (
	"context"
	"fmt"
	"sync"
	"time"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/typeurl/v2"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime/pkg/client"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
)

type FakeContainerd struct {
	mu                 sync.Mutex
	pulledImages       map[string]bool
	pullErrorOverrides map[string]error
	k8sClient          ctrl.Client
	pullDelay          time.Duration // Simulate delay for pulling images
	testEventsCh       chan *events.Envelope
	testErrCh          chan error
}

func NewFakeContainerd(k8sClient ctrl.Client) *FakeContainerd {
	return &FakeContainerd{
		pulledImages:       make(map[string]bool),
		pullErrorOverrides: make(map[string]error),
		k8sClient:          k8sClient,
		testEventsCh:       make(chan *events.Envelope, 10),
		testErrCh:          make(chan error, 10),
	}
}

func (f *FakeContainerd) IsImageExists(ctx context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	exists, ok := f.pulledImages[ref]
	if !ok {
		return false, nil
	}

	return exists, nil
}

func (f *FakeContainerd) Subscribe(ctx context.Context) (<-chan *events.Envelope, <-chan error) {
	eventsCh := make(chan *events.Envelope)
	errCh := make(chan error)

	go func() {
		defer close(eventsCh)
		defer close(errCh)
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-f.testEventsCh:
				select {
				case eventsCh <- event:
				case <-ctx.Done():
					return
				}
			case err := <-f.testErrCh:
				select {
				case errCh <- err:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return eventsCh, errCh
}

func (f *FakeContainerd) SendTestEvent(event *events.Envelope) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	v, err := typeurl.UnmarshalAny(event.Event)
	if err != nil {
		return fmt.Errorf("failed to unmarshal event: %w", err)
	}

	if f.testEventsCh != nil {
		if e, ok := v.(*eventtypes.ImageDelete); ok {
			f.pulledImages[e.GetName()] = false
		}
		f.testEventsCh <- event
	}
	return nil
}

// RegisterImagePullError allows registering a specific error to be returned
// when PullImage is called for a given image reference.
func (f *FakeContainerd) RegisterImagePullError(ref string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pullErrorOverrides[ref] = err
}

func (f *FakeContainerd) PullImage(ctx context.Context, ref string, policy ofenv1.RegistryPolicy, secrets *[]corev1.Secret) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Check for registered error overrides first
	if err, ok := f.pullErrorOverrides[ref]; ok {
		return 0, err
	}

	// Simulate a delay for pulling the image
	if f.pullDelay > 0 {
		time.Sleep(f.pullDelay)
	}
	f.pulledImages[ref] = true

	return 100, nil
}

func (f *FakeContainerd) SetPullDelay(delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pullDelay = delay
}

// SetImageExists allows setting whether an image exists in the fake registry
func (f *FakeContainerd) SetImageExists(ref string, exists bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulledImages[ref] = exists
}

// SendDeleteEvent sends a delete event for testing purposes
func (f *FakeContainerd) SendDeleteEvent(ref string) error {
	event, err := CreateImageDeleteEvent(ref)
	if err != nil {
		return err
	}
	return f.SendTestEvent(event)
}

func CreateImageDeleteEvent(ref string) (*events.Envelope, error) {
	imageDeleteEvent := &eventtypes.ImageDelete{Name: ref}
	anyEvent, err := typeurl.MarshalAny(imageDeleteEvent)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal image delete event: %w", err)
	}
	return &events.Envelope{
		Timestamp: time.Now(),
		Namespace: "k8s.io",
		Topic:     "/images/delete",
		Event:     anyEvent,
	}, nil
}

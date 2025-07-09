package imgmanager

import (
	"context"
	"fmt"
	"slices"
	"sync"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/typeurl/v2"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
)

type NodeImageSetStatus struct {
	Images sync.Map
}

type ImagePullStatus struct {
	ImagePulling bool
	Error        error
	mutex        sync.Mutex
}

func NewImagePullStatus() *ImagePullStatus {
	return &ImagePullStatus{
		ImagePulling: false,
		Error:        nil,
		mutex:        sync.Mutex{},
	}
}

func (s *ImagePullStatus) SetError(err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.Error = err
}

func (s *ImagePullStatus) GetError() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.Error
}

func (s *ImagePullStatus) SetImagePulling(pulling bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.ImagePulling = pulling
}

func (s *ImagePullStatus) IsImagePulling() bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.ImagePulling
}

func (s *ImagePullStatus) ClearError() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.Error = nil
}

func (s *ImagePullStatus) StartPulling() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.ImagePulling = true
	s.Error = nil
}

func (s *ImagePullStatus) StopPulling() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.ImagePulling = false
}

func (s *ImagePullStatus) TryStartPulling() bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.ImagePulling {
		return false
	}

	s.ImagePulling = true
	s.Error = nil
	return true
}

func (n *NodeImageSetStatus) GetOrCreateImageStatus(ref string) *ImagePullStatus {
	newStatus := NewImagePullStatus()
	if value, loaded := n.Images.LoadOrStore(ref, newStatus); loaded {
		return value.(*ImagePullStatus)
	}
	return newStatus
}

func (n *NodeImageSetStatus) GetImageStatus(ref string) (*ImagePullStatus, bool) {
	value, ok := n.Images.Load(ref)
	if !ok {
		return nil, false
	}
	return value.(*ImagePullStatus), true
}

type ImagePuller struct {
	logger           logr.Logger
	containerdClient ContainerdClient
	status           sync.Map
}

func NewImagePuller(logger logr.Logger, containerdClient ContainerdClient) *ImagePuller {
	return &ImagePuller{
		logger:           logger,
		containerdClient: containerdClient,
		status:           sync.Map{},
	}
}

func (p *ImagePuller) NewNodeImageSetStatus(nodeImageSetName string) {
	p.status.Store(nodeImageSetName, &NodeImageSetStatus{
		Images: sync.Map{},
	})
}

func (p *ImagePuller) IsExistsNodeImageSetStatus(nodeImageSetName string) bool {
	_, ok := p.status.Load(nodeImageSetName)
	return ok
}

func (p *ImagePuller) UpdateNodeImageSetStatus(nodeImageSetName string, images []string) {
	value, ok := p.status.Load(nodeImageSetName)
	if !ok {
		return
	}

	nodeStatus := value.(*NodeImageSetStatus)
	nodeStatus.Images.Range(func(key, value any) bool {
		if slices.Contains(images, key.(string)) {
			return true
		}
		nodeStatus.Images.Delete(key)
		return true
	})

	for _, ref := range images {
		if _, ok := nodeStatus.Images.Load(ref); !ok {
			nodeStatus.GetOrCreateImageStatus(ref)
		}
	}
}

func (p *ImagePuller) DeleteNodeImageSetStatus(nodeImageSetName string) {
	p.status.LoadAndDelete(nodeImageSetName)
}

func (p *ImagePuller) IsImageExists(ctx context.Context, ref string) bool {
	exists, err := p.containerdClient.IsImageExists(ctx, ref)
	if err != nil {
		p.logger.Error(err, "failed to check image existence", "image", ref)
		return false
	}

	return exists
}

func (p *ImagePuller) PullImage(ctx context.Context, nodeImageSetName, ref string, registryPolicy ofenv1.RegistryPolicy, secrets *[]corev1.Secret) error {
	value, ok := p.status.Load(nodeImageSetName)
	if !ok {
		return nil
	}

	exists, err := p.containerdClient.IsImageExists(ctx, ref)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	nodeStatus := value.(*NodeImageSetStatus)
	imageStatus := nodeStatus.GetOrCreateImageStatus(ref)

	if !imageStatus.TryStartPulling() {
		return nil // Another process is already pulling
	}
	exists, err = p.containerdClient.IsImageExists(ctx, ref)
	if err != nil {
		imageStatus.StopPulling()
		return err
	}
	if exists {
		imageStatus.StopPulling()
		return nil
	}

	err = p.containerdClient.PullImage(ctx, ref, registryPolicy, secrets)

	imageStatus.StopPulling()
	if err != nil {
		if errdefs.IsNotFound(err) && registryPolicy == ofenv1.RegistryPolicyMirrorOnly {
			// If the image is not found and the policy is MirrorOnly, we treat it as a non-fatal error.
			p.logger.Info("image not found in mirror registry, skipping pull", "image", ref)
		} else {
			imageStatus.SetError(err)
		}
	}

	return err
}

func (p *ImagePuller) GetImageStatus(ctx context.Context, nodeImageSetName, imageName string) (string, string, error) {
	value, ok := p.status.Load(nodeImageSetName)
	if !ok {
		return "", "", nil
	}

	exists, err := p.containerdClient.IsImageExists(ctx, imageName)
	if err != nil {
		return "", "", err
	}
	if exists {
		return ofenv1.ImageDownloaded, "", nil
	}

	nodeStatus := value.(*NodeImageSetStatus)
	imageStatus, ok := nodeStatus.GetImageStatus(imageName)
	if !ok {
		return ofenv1.WaitingForImageDownload, "", nil
	}

	pulling := imageStatus.IsImagePulling()
	if pulling {
		return ofenv1.ImageDownloadInProgress, "", nil
	}
	imageErr := imageStatus.GetError()
	if imageErr != nil {
		return ofenv1.ImageDownloadFailed, imageErr.Error(), nil
	}

	return ofenv1.WaitingForImageDownload, "", nil
}

func (p *ImagePuller) SubscribeDeleteEvent(ctx context.Context) (<-chan string, error) {
	eventsCh, errorCh := p.containerdClient.Subscribe(ctx)
	imageDeletionCh := make(chan string)

	go func() {
		defer close(imageDeletionCh)
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-errorCh:
				if err != nil {
					p.logger.Error(err, "failed to receive events from containerd")
				}
			case e := <-eventsCh:
				deleteImageName, err := handleDeleteEvent(e)
				if err != nil {
					p.logger.Error(err, "failed to process containerd delete event", "event", e)
					continue
				}
				if deleteImageName != "" {
					p.logger.Info("processed image deletion event", "imageName", deleteImageName)
					p.status.Range(func(key, value interface{}) bool {
						nodeStatus := value.(*NodeImageSetStatus)
						if value, ok := nodeStatus.Images.Load(deleteImageName); ok {
							imageStatus := value.(*ImagePullStatus)
							imageStatus.SetImagePulling(false)
							imageStatus.SetError(nil)
						}
						return true
					})

					select {
					case imageDeletionCh <- deleteImageName:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	return imageDeletionCh, nil
}

func handleDeleteEvent(e *events.Envelope) (string, error) {
	if e == nil || e.Event == nil {
		return "", fmt.Errorf("event is nil or empty")
	}
	v, err := typeurl.UnmarshalAny(e.Event)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal event: %w", err)
	}

	switch event := v.(type) {
	case *eventtypes.ImageDelete:
		return event.GetName(), nil
	default:
		return "", fmt.Errorf("unsupported event type: %T", event)
	}
}

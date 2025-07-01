package imgmanager

import (
	"context"
	"fmt"
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
	Images map[string]*ImagePullStatus
	mutex  sync.RWMutex
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
	n.mutex.Lock()
	defer n.mutex.Unlock()

	if n.Images == nil {
		n.Images = make(map[string]*ImagePullStatus)
	}

	if _, ok := n.Images[ref]; !ok {
		n.Images[ref] = NewImagePullStatus()
	}

	return n.Images[ref]
}

func (n *NodeImageSetStatus) GetImageStatus(ref string) (*ImagePullStatus, bool) {
	n.mutex.RLock()
	defer n.mutex.RUnlock()

	status, ok := n.Images[ref]
	return status, ok
}

type ImagePuller struct {
	logger           logr.Logger
	containerdClient ContainerdClient
	status           map[string]*NodeImageSetStatus
}

func NewImagePuller(logger logr.Logger, containerdClient ContainerdClient) *ImagePuller {
	return &ImagePuller{
		logger:           logger,
		containerdClient: containerdClient,
		status:           make(map[string]*NodeImageSetStatus),
	}
}

func (p *ImagePuller) NewNodeImageSetStatus(nodeImageSetName string) {
	p.status[nodeImageSetName] = &NodeImageSetStatus{
		Images: make(map[string]*ImagePullStatus),
		mutex:  sync.RWMutex{},
	}
}

func (p *ImagePuller) IsExistsNodeImageSetStatus(nodeImageSetName string) bool {
	_, ok := p.status[nodeImageSetName]
	return ok
}

func (p *ImagePuller) IsImageExists(ctx context.Context, ref string) bool {
	exists, err := p.containerdClient.IsImageExists(ctx, ref)
	if err != nil {
		p.logger.Error(err, "Failed to check if image exists", "image", ref)
		return false
	}

	return exists
}

func (p *ImagePuller) PullImage(ctx context.Context, nodeImageSetName, ref string, registryPolicy ofenv1.RegistryPolicy, secrets *[]corev1.Secret) error {
	if _, ok := p.status[nodeImageSetName]; !ok {
		return nil
	}

	exists, err := p.containerdClient.IsImageExists(ctx, ref)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	nodeStatus := p.status[nodeImageSetName]
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
			p.logger.Info("Image not found in mirror registry, continuing", "image", ref)
		} else {
			imageStatus.SetError(err)
		}
	}

	return err
}

func (p *ImagePuller) GetImageStatus(ctx context.Context, nodeImageSetName, imageName string) (string, string, error) {
	if _, ok := p.status[nodeImageSetName]; !ok {
		return "", "", nil
	}

	exists, err := p.containerdClient.IsImageExists(ctx, imageName)
	if err != nil {
		return "", "", err
	}
	if exists {
		return ofenv1.ImageDownloaded, "", nil
	}

	nodeStatus := p.status[nodeImageSetName]
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
					p.logger.Error(err, "error receiving events from containerd")
				}
			case e := <-eventsCh:
				deleteImageName, err := handleDeleteEvent(e)
				if err != nil {
					p.logger.Error(err, "failed to handle delete event", "event", e)
					continue
				}
				if deleteImageName != "" {
					p.logger.Info("image deletion event received", "deleteImageName", deleteImageName)
					for _, nodeStatus := range p.status {
						if imageStatus, ok := nodeStatus.Images[deleteImageName]; ok {
							imageStatus.SetImagePulling(false)
							imageStatus.SetError(nil)
						}
					}

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

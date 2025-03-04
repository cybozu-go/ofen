package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/errdefs"
	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

const (
	pullRetryInterval      = 30 * time.Second
	existenceCheckInterval = 5 * time.Minute
)

type ContainerdClient interface {
	IsImageExists(ctx context.Context, ref string) (bool, error)
	PullImage(ctx context.Context, ref string, policy ofenv1.RegistryPolicy) error
	Subscribe(ctx context.Context, images []string) (<-chan *events.Envelope, <-chan error)
	SetCredentials(ctx context.Context, secrets []corev1.Secret) error
}

type imagePuller struct {
	log                logr.Logger
	containerdClient   ContainerdClient
	k8sClient          ctrl.Client
	processes          map[string]*pullProcess
	reconcileRequestCh chan<- event.TypedGenericEvent[*ofenv1.NodeImageSet]
	mu                 sync.Mutex
}

func NewImagePuller(log logr.Logger, containerdClient ContainerdClient, k8sClient ctrl.Client, reconcileRequestCh chan<- event.TypedGenericEvent[*ofenv1.NodeImageSet]) *imagePuller {
	return &imagePuller{
		log:                log,
		containerdClient:   containerdClient,
		k8sClient:          k8sClient,
		reconcileRequestCh: reconcileRequestCh,
		processes:          make(map[string]*pullProcess),
	}
}

func (i *imagePuller) start(ctx context.Context, nis *ofenv1.NodeImageSet, secrets []corev1.Secret, recorder record.EventRecorder, requireImagePullerRefresh bool) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if _, ok := i.processes[nis.Name]; !ok {
		i.log.Info("starting image pull process", "nodeimageset", nis.Name)
		process := newPullProcess(i.log.WithValues("nodeimageset", nis.Name), i.containerdClient, i.k8sClient, i.reconcileRequestCh, nis, secrets, recorder)
		process.wg.Add(1)
		go func() {
			process.start(ctx)
			process.wg.Done()
		}()
		i.processes[nis.Name] = process
		return nil
	}

	if requireImagePullerRefresh {
		i.processes[nis.Name].update(nis, secrets)
	}

	return nil
}

func (i *imagePuller) stop(nis *ofenv1.NodeImageSet) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if process, ok := i.processes[nis.Name]; ok {
		process.stop()
		process.wg.Wait()
		delete(i.processes, nis.Name)
	}
}

func (i *imagePuller) StopAll() {
	i.mu.Lock()
	defer i.mu.Unlock()

	for _, process := range i.processes {
		process.stop()
		process.wg.Wait()
	}
	i.processes = nil
}

func (i *imagePuller) GetImageStatuses(nisName string) []ofenv1.ContainerImageStatus {
	i.mu.Lock()
	defer i.mu.Unlock()

	if process, ok := i.processes[nisName]; ok {
		return process.getImageStatuses()
	}
	return nil
}

type pullProcess struct {
	log                logr.Logger
	containerdClient   ContainerdClient
	k8sClient          ctrl.Client
	nodeImageSet       *ofenv1.NodeImageSet
	namespace          string
	cancel             context.CancelFunc
	secrets            []corev1.Secret
	updateSignalCh     chan string
	recorder           record.EventRecorder
	once               sync.Once
	reconcileRequestCh chan<- event.TypedGenericEvent[*ofenv1.NodeImageSet]
	imageDeleteEventCh chan string
	imageStatus        map[string]*ImageStatus
	eventWatcher       *ContainerdEventWatcher
	wg                 sync.WaitGroup
}
type ImageStatus struct {
	mutex sync.Mutex
	err   error
	State string
}

func (imageStatus *ImageStatus) SetError(err error) {
	imageStatus.mutex.Lock()
	imageStatus.err = err
	imageStatus.mutex.Unlock()
}

func (imageStatus *ImageStatus) GetError() error {
	imageStatus.mutex.Lock()
	defer imageStatus.mutex.Unlock()
	return imageStatus.err
}

func (imageStatus *ImageStatus) GetState() string {
	imageStatus.mutex.Lock()
	defer imageStatus.mutex.Unlock()
	return imageStatus.State
}

func (imageStatus *ImageStatus) SetState(state string) {
	imageStatus.mutex.Lock()
	imageStatus.State = state
	imageStatus.mutex.Unlock()
}

func (p *pullProcess) getImageStatuses() []ofenv1.ContainerImageStatus {
	statuses := make([]ofenv1.ContainerImageStatus, 0, len(p.imageStatus))

	for ref, status := range p.imageStatus {
		state := status.GetState()
		imgStatus := ofenv1.ContainerImageStatus{
			ImageRef: ref,
			State:    state,
		}

		err := status.GetError()
		if err != nil {
			imgStatus.Error = err.Error()
		}
		statuses = append(statuses, imgStatus)
	}

	return statuses
}

func newPullProcess(log logr.Logger,
	containerdClient ContainerdClient,
	k8sClient ctrl.Client,
	reconcileRequestCh chan<- event.TypedGenericEvent[*ofenv1.NodeImageSet],
	nis *ofenv1.NodeImageSet,
	secrets []corev1.Secret,
	recorder record.EventRecorder,
) *pullProcess {
	return &pullProcess{
		log:                log,
		containerdClient:   containerdClient,
		k8sClient:          k8sClient,
		reconcileRequestCh: reconcileRequestCh,
		nodeImageSet:       nis,
		namespace:          nis.Namespace,
		secrets:            secrets,
		recorder:           recorder,
		updateSignalCh:     make(chan string, 10),
		imageDeleteEventCh: make(chan string, 10),
		imageStatus:        make(map[string]*ImageStatus),
	}
}

func (p *pullProcess) start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.log.Info("starting image pull process")
	p.recorder.Event(p.nodeImageSet, corev1.EventTypeNormal, "ImagePullStarted", fmt.Sprintf("Image pull process started on %s", p.nodeImageSet.Spec.NodeName))
	waitTime := time.Second

	p.startOrUpdateEventWatcher(ctx)
	err := p.containerdClient.SetCredentials(ctx, p.secrets)
	if err != nil {
		p.log.Error(err, "failed to set credentials for containerd client")
		return
	}

	for {
		select {
		case <-ctx.Done():
			p.log.Info("received stop signal, stopping image pull process")
			p.recorder.Event(p.nodeImageSet, corev1.EventTypeNormal, "ImagePullStopped", "Image pull process stopped")
			return
		case deleteImageEvent := <-p.imageDeleteEventCh:
			if deleteImageEvent == "" {
				continue
			}
			p.log.Info("received image delete event", "image", deleteImageEvent)
			if _, exists := p.imageStatus[deleteImageEvent]; !exists {
				p.log.Info("image not found in status map, ignoring delete event", "image", deleteImageEvent)
				continue
			}
			p.imageStatus[deleteImageEvent].SetState(ofenv1.ImageDownloadInProgress)
			p.notifyController(deleteImageEvent)
		case update := <-p.updateSignalCh:
			if update == "" {
				continue
			}
			p.startOrUpdateEventWatcher(ctx)
			err := p.containerdClient.SetCredentials(ctx, p.secrets)
			if err != nil {
				p.log.Error(err, "failed to update credentials for containerd client during update")
				return
			}
		case <-time.After(waitTime):
		}

		errs := p.pullImages(ctx)
		if len(errs) != 0 {
			p.log.Error(fmt.Errorf("failed to pull images: %v", errs), "error pulling images")
			waitTime = pullRetryInterval
			continue
		}

		waitTime = existenceCheckInterval
	}
}

func (p *pullProcess) startOrUpdateEventWatcher(ctx context.Context) {
	if p.eventWatcher != nil {
		p.log.Info("stopping existing event watcher")
		p.eventWatcher.Stop()
	}

	p.log.Info("starting event watcher")
	eventWatcher := NewContainerdEventWatcher(
		p.containerdClient,
		p.log,
		p.imageDeleteEventCh,
		p.nodeImageSet.Spec.Images,
	)
	p.eventWatcher = eventWatcher
	go func() {
		eventWatcher.Start(ctx)
	}()
}

func (p *pullProcess) notifyController(imageName string) {
	p.log.Info("notifying controller about image deletion", "imageName", imageName)
	p.reconcileRequestCh <- event.TypedGenericEvent[*ofenv1.NodeImageSet]{
		Object: p.nodeImageSet.DeepCopy(),
	}
}

func (p *pullProcess) pullImages(ctx context.Context) []error {
	var errs []error

	for _, ref := range p.nodeImageSet.Spec.Images {
		imgStatus, exists := p.imageStatus[ref]
		if !exists {
			imgStatus = &ImageStatus{}
			p.imageStatus[ref] = imgStatus
		}

		exists, err := p.containerdClient.IsImageExists(ctx, ref)
		if err != nil {
			p.log.Error(err, "failed to check image existence", "ref", ref)
			errs = append(errs, err)
			continue
		}
		if exists {
			imgStatus.SetState(ofenv1.ImageDownloaded)
			imgStatus.SetError(nil)
			continue
		}

		p.log.Info("pulling image", "ref", ref)
		p.recorder.Event(p.nodeImageSet, corev1.EventTypeNormal, "ImagePulling", fmt.Sprintf("Pulling image %s on %s", ref, p.nodeImageSet.Spec.NodeName))
		imgStatus.SetState(ofenv1.ImageDownloadInProgress)

		err = p.containerdClient.PullImage(ctx, ref, p.nodeImageSet.Spec.RegistryPolicy)
		if err != nil {
			// If using MirrorOnly policy and the image is not found (i.e., not yet cached in the mirror),
			// record the error and attempt to pull the next image.
			if errdefs.IsNotFound(err) && p.nodeImageSet.Spec.RegistryPolicy == ofenv1.RegistryPolicyMirrorOnly {
				errs = append(errs, err)
				continue
			}

			p.log.Error(err, "failed to pull image", "ref", ref)
			p.recorder.Eventf(p.nodeImageSet, corev1.EventTypeWarning, "ImagePullFailed", "Failed to pull image %s on node %s: %v", ref, p.nodeImageSet.Spec.NodeName, err)
			imgStatus.SetState(ofenv1.ImageDownloadFailed)
			imgStatus.SetError(err)

			errs = append(errs, err)
			continue
		}
		p.recorder.Event(p.nodeImageSet, corev1.EventTypeNormal, "ImagePulled", fmt.Sprintf("Finished pulling image %s on %s", ref, p.nodeImageSet.Spec.NodeName))
		imgStatus.SetState(ofenv1.ImageDownloaded)
		imgStatus.SetError(nil)
	}

	return errs
}

func (p *pullProcess) update(nis *ofenv1.NodeImageSet, secrets []corev1.Secret) {
	p.log.Info("updating image pull process with new NodeImageSet", "nodeimageset", nis.Name)
	p.nodeImageSet = nis
	p.secrets = secrets
	p.imageStatus = make(map[string]*ImageStatus)
	p.updateSignalCh <- "update"
}

func (p *pullProcess) stop() {
	p.log.Info("stopping image pull process")
	p.cancel()
	p.once.Do(func() {
		close(p.updateSignalCh)
		close(p.imageDeleteEventCh)
		p.eventWatcher.Stop()
		p.eventWatcher = nil
	})
}

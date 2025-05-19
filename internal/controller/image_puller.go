package controller

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/containerd/containerd/errdefs"
	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
)

type ContainerdClient interface {
	IsImageExists(ctx context.Context, ref string) (bool, error)
	PullImage(ctx context.Context, ref string, policy ofenv1.RegistryPolicy) error
}

type imagePuller struct {
	log              logr.Logger
	containerdClient ContainerdClient
	processes        map[string]*pullProcess
	wg               sync.WaitGroup
	mu               sync.Mutex
}

func NewImagePuller(log logr.Logger, containerdClient ContainerdClient) *imagePuller {
	return &imagePuller{
		log:              log,
		containerdClient: containerdClient,
		processes:        make(map[string]*pullProcess),
	}
}

func (i *imagePuller) start(ctx context.Context, nis *ofenv1.NodeImageSet, secrets []corev1.Secret, recorder record.EventRecorder, requireImagePullerRefresh bool) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if _, ok := i.processes[nis.Name]; !ok {
		i.log.Info("starting image pull process", "nodeimageset", nis.Name)
		process := newPullProcess(i.log.WithValues("nodeimageset", nis.Name), i.containerdClient, nis, secrets, recorder)
		i.wg.Add(1)
		go func() {
			process.start(ctx)
			i.wg.Done()
		}()
		i.processes[nis.Name] = process
		return nil
	}

	if requireImagePullerRefresh {
		i.processes[nis.Name].update(nis)
	}

	return nil
}

func (i *imagePuller) stop(nis *ofenv1.NodeImageSet) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if process, ok := i.processes[nis.Name]; ok {
		process.stop()
		delete(i.processes, nis.Name)
	}
}

func (i *imagePuller) StopAll() {
	i.mu.Lock()
	defer i.mu.Unlock()

	for _, process := range i.processes {
		process.stop()
	}
	i.processes = nil
	i.wg.Wait()
}

func (i *imagePuller) CountFailedImages(nis *ofenv1.NodeImageSet) int {
	i.mu.Lock()
	defer i.mu.Unlock()

	if process, ok := i.processes[nis.Name]; ok {
		failedImageCount := 0
		process.imageStatus.Range(func(key, value interface{}) bool {
			if status, ok := value.(*ImageStatus); ok {
				if atomic.LoadInt32(&status.failedCount) > 0 {
					failedImageCount++
				}
			}
			return true
		})
		return failedImageCount
	}

	return 0
}

type pullProcess struct {
	log              logr.Logger
	containerdClient ContainerdClient
	nodeImageSet     *ofenv1.NodeImageSet
	namespace        string
	cancel           context.CancelFunc
	secrets          []corev1.Secret
	ch               chan string
	recorder         record.EventRecorder
	once             sync.Once
	imageStatus      *sync.Map
}
type ImageStatus struct {
	failedCount int32
	err         error
}

func newPullProcess(log logr.Logger, containerdClient ContainerdClient, nis *ofenv1.NodeImageSet, secrets []corev1.Secret, recorder record.EventRecorder) *pullProcess {
	return &pullProcess{
		log:              log,
		containerdClient: containerdClient,
		nodeImageSet:     nis,
		namespace:        nis.Namespace,
		secrets:          secrets,
		recorder:         recorder,
		ch:               make(chan string, 1),
		imageStatus:      new(sync.Map),
	}
}

const (
	pullRetryInterval      = 1 * time.Minute
	existenceCheckInterval = 5 * time.Minute
)

func (p *pullProcess) start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.log.Info("starting image pull process")
	p.recorder.Event(p.nodeImageSet, corev1.EventTypeNormal, "ImagePullStarted", fmt.Sprintf("Image pull process started on %s", p.nodeImageSet.Spec.NodeName))
	waitTime := time.Second

	for {
		select {
		case <-ctx.Done():
			p.log.Info("image pull process stopped")
			p.recorder.Event(p.nodeImageSet, corev1.EventTypeNormal, "ImagePullStopped", "Image pull process stopped")
			p.stop()
			return
		case <-time.After(waitTime):
		case <-p.ch:
		}

		errs := p.pullImages(ctx)
		if len(errs) != 0 {
			p.log.Error(fmt.Errorf("image pull failed"), "retrying in 1 minute", "errors", errs)
			waitTime = pullRetryInterval
			continue
		}

		waitTime = existenceCheckInterval
	}
}

func (p *pullProcess) pullImages(ctx context.Context) []error {
	var errs []error

	for _, ref := range p.nodeImageSet.Spec.Images {
		s, _ := p.imageStatus.LoadOrStore(ref, &ImageStatus{})
		imgStatus := s.(*ImageStatus)

		exists, err := p.containerdClient.IsImageExists(ctx, ref)
		if err != nil {
			p.log.Error(err, "failed to check image existence", "ref", ref)
			errs = append(errs, err)
			continue
		}
		if exists {
			atomic.StoreInt32(&imgStatus.failedCount, 0)
			imgStatus.err = nil
			continue
		}

		p.log.Info("pulling image", "ref", ref)
		p.recorder.Event(p.nodeImageSet, corev1.EventTypeNormal, "ImagePulling", fmt.Sprintf("Pulling image %s on %s", ref, p.nodeImageSet.Spec.NodeName))
		err = p.containerdClient.PullImage(ctx, ref, p.nodeImageSet.Spec.RegistryPolicy)
		if err != nil {
			if errdefs.IsNotFound(err) &&
				p.nodeImageSet.Spec.RegistryPolicy == ofenv1.RegistryPolicyMirrorOnly {
				errs = append(errs, err)
				continue
			}

			p.log.Error(err, "failed to pull image", "ref", ref)
			atomic.AddInt32(&imgStatus.failedCount, 1)
			imgStatus.err = err
			errs = append(errs, err)
			continue
		}

		p.recorder.Event(p.nodeImageSet, corev1.EventTypeNormal, "ImagePulled", fmt.Sprintf("Finished pulling image %s on %s", ref, p.nodeImageSet.Spec.NodeName))
		atomic.StoreInt32(&imgStatus.failedCount, 0)
		imgStatus.err = nil
	}

	return errs
}

func (p *pullProcess) update(nis *ofenv1.NodeImageSet) {
	p.nodeImageSet = nis

	newImageRefs := make(map[string]struct{})
	for _, ref := range nis.Spec.Images {
		newImageRefs[ref] = struct{}{}
	}

	p.imageStatus.Range(func(key, value interface{}) bool {
		ref := key.(string)
		if _, ok := newImageRefs[ref]; !ok {
			// Remove status for images that are no longer in NodeImageSet
			p.imageStatus.Delete(ref)
			return true
		}

		if status, ok := value.(*ImageStatus); ok {
			atomic.StoreInt32(&status.failedCount, 0)
			status.err = nil
		}
		return true
	})
	p.ch <- "update"
}

func (p *pullProcess) stop() {
	p.cancel()
	p.once.Do(func() {
		close(p.ch)
	})
}

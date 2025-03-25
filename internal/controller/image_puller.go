package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/imgmanager"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime/pkg/client"
)

type imagePuller struct {
	log              logr.Logger
	k8sClient        ctrl.Client
	containerdClient *imgmanager.Containerd
	processes        map[string]*pullProcess
	wg               sync.WaitGroup
	mu               sync.Mutex
}

func NewImagePuller(log logr.Logger, k8sClient ctrl.Client, containerdClient *imgmanager.Containerd) *imagePuller {
	return &imagePuller{
		log:              log,
		k8sClient:        k8sClient,
		containerdClient: containerdClient,
		processes:        make(map[string]*pullProcess),
	}
}

func (i *imagePuller) start(ctx context.Context, nis *ofenv1.NodeImageSet, secrets []corev1.Secret, recorder record.EventRecorder) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if p, ok := i.processes[nis.Name]; ok {
		p.update(nis)
		return nil
	}

	i.log.Info("starting image pull process", "nodeimageset", nis.Name)
	process := newPullProcess(i.log.WithValues("nodeimageset", nis.Name), i.k8sClient, i.containerdClient, nis, secrets, recorder)
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		process.start(ctx)
	}()
	i.processes[nis.Name] = process
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

type pullProcess struct {
	log              logr.Logger
	k8sClient        ctrl.Client
	containerdClient *imgmanager.Containerd
	nodeImageSet     *ofenv1.NodeImageSet
	namespace        string
	cancel           context.CancelFunc
	secrets          []corev1.Secret
	ch               chan string
	recorder         record.EventRecorder
}

func newPullProcess(log logr.Logger, k8sClient ctrl.Client, containerdClient *imgmanager.Containerd, nis *ofenv1.NodeImageSet, secrets []corev1.Secret, recorder record.EventRecorder) *pullProcess {
	return &pullProcess{
		log:              log,
		k8sClient:        k8sClient,
		containerdClient: containerdClient,
		nodeImageSet:     nis,
		namespace:        nis.Namespace,
		secrets:          secrets,
		recorder:         recorder,
	}
}

func (p *pullProcess) start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.log.Info("starting image pull process")
	p.recorder.Event(p.nodeImageSet, corev1.EventTypeNormal, "ImagePullStarted", "Image pull process started")
	waitTime := 10 * time.Second

	for {
		select {
		case <-ctx.Done():
			p.log.Info("image pull process stopped")
			p.recorder.Event(p.nodeImageSet, corev1.EventTypeNormal, "ImagePullStopped", "Image pull process stopped")
			p.stop()
			return nil
		case <-time.After(waitTime):
		case <-p.ch:
		}

		err := p.pullImages(ctx)
		if err != nil {
			p.log.Error(err, "failed to pull images")
			p.recorder.Event(p.nodeImageSet, corev1.EventTypeWarning, "ImagePullFailed", "Failed to pull images")
			waitTime = 1 * time.Minute
			continue
		}
		waitTime = 5 * time.Minute
	}
}

func (p *pullProcess) pullImages(ctx context.Context) error {
	for _, img := range p.nodeImageSet.Spec.ImageSet {
		ref := img.Image
		policy := img.RegistryPolicy
		exists, err := p.containerdClient.IsImageExists(ctx, ref)
		if err != nil {
			return err
		}
		if exists {
			continue
		}

		p.log.Info("pulling image", "ref", ref)
		p.recorder.Event(p.nodeImageSet, corev1.EventTypeNormal, "ImagePulling", fmt.Sprintf("Pulling image %s", ref))
		err = p.containerdClient.PullImage(ctx, ref, policy)
		if err != nil {
			p.log.Error(err, "failed to pull image", "ref", ref)
			return err
		}
		p.recorder.Event(p.nodeImageSet, corev1.EventTypeNormal, "ImagePulled", fmt.Sprintf("Image %s pulled", ref))
	}

	return nil
}

func (p *pullProcess) update(nis *ofenv1.NodeImageSet) {
	p.nodeImageSet = nis
	p.ch <- "update"
}

func (p *pullProcess) stop() {
	p.cancel()
}

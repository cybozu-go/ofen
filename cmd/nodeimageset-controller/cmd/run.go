package cmd

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"

	containerdclient "github.com/containerd/containerd/v2/client"
	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/controller"
	"github.com/cybozu-go/ofen/internal/imgmanager"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(ofenv1.AddToScheme(scheme))
}

func subMain() error {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&config.zapOpts)))

	host, portStr, err := net.SplitHostPort(config.webhookAddr)
	if err != nil {
		return fmt.Errorf("invalid webhook address: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("invalid webhook address: %w", err)
	}

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		return fmt.Errorf("NODE_NAME is not set")
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:           scheme,
		LeaderElection:   false,
		LeaderElectionID: "08b3b3b1.ofen.cybozu.io",
		Metrics: metricsserver.Options{
			BindAddress: config.metricsAddr,
		},
		WebhookServer: webhook.NewServer(webhook.Options{
			Host: host,
			Port: port,
		}),
		HealthProbeBindAddress: config.probeAddr,
	})
	if err != nil {
		return err
	}

	containerdConfig := &imgmanager.ContainerdConfig{
		SockAddr:  config.containerdAddress,
		Namespace: config.namespace,
		HostDir:   config.hostDir,
	}
	ctx := context.Background()
	client, err := containerdclient.New(containerdConfig.SockAddr)
	if err != nil {
		return err
	}
	containerdClient := imgmanager.NewContainerd(ctx, containerdConfig, client)
	log := ctrl.Log.WithName("image-puller")
	imagePuller := controller.NewImagePuller(log, mgr.GetClient(), containerdClient)
	defer imagePuller.StopAll()

	if err = (&controller.NodeImageSetReconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Recorder:    mgr.GetEventRecorderFor("nodeimageset-controller"),
		NodeName:    nodeName,
		ImagePuller: imagePuller,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}

	setupLog.Info("starting nodeimageset controller", "node", nodeName)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running nodeimageset controller")
		return err
	}

	return nil
}

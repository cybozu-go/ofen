package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"os"

	containerd "github.com/containerd/containerd/v2/client"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
	"github.com/cybozu-go/ofen/internal/controller"
	"github.com/cybozu-go/ofen/internal/imgmanager"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(ofenv1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	var containerdConfig imgmanager.ContainerdConfig
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&containerdConfig.SockAddr, "containerd-socket",
		"/run/containerd/containerd.sock", "Containerd socket address")
	flag.StringVar(&containerdConfig.Namespace, "containerd-namespace", "k8s.io", "Containerd namespace")
	flag.StringVar(&containerdConfig.HostDir, "containerd-host-dir",
		"/etc/containerd/certs.d", "Containerd host directory")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: tlsOpts,
	})

	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		setupLog.Error(fmt.Errorf("NODE_NAME is required"), "failed to get node name")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "08b3b3b1.ofen.cybozu.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	client, err := containerd.New(containerdConfig.SockAddr)
	if err != nil {
		setupLog.Error(err, "unable to connect to containerd")
		os.Exit(1)
	}
	defer func() {
		if err := client.Close(); err != nil {
			setupLog.Error(err, "failed to close containerd client")
		}
	}()

	containerdClient := imgmanager.NewContainerd(&containerdConfig, client)
	imagePuller := imgmanager.NewImagePuller(ctrl.Log.WithName("imagePuller"), containerdClient)
	ch := make(chan event.TypedGenericEvent[*ofenv1.NodeImageSet])
	rateLimiter := workqueue.DefaultTypedControllerRateLimiter[controller.Task]()
	queue := workqueue.NewTypedRateLimitingQueue(rateLimiter)

	runner := controller.NewRunner(
		mgr.GetClient(),
		imagePuller,
		ctrl.Log.WithName("runner").WithValues("nodeName", nodeName),
		queue,
		mgr.GetEventRecorderFor("image-pull-runner"),
	)
	err = mgr.Add(runner)
	if err != nil {
		setupLog.Error(err, "unable to add runner")
		os.Exit(1)
	}

	// Set up the containerd event watcher
	containerdEventWatcher := controller.NewContainerdEventWatcher(
		mgr.GetClient(),
		containerdClient,
		imagePuller,
		ctrl.Log.WithName("containerd-event-watcher"),
		nodeName,
		ch,
	)
	err = mgr.Add(containerdEventWatcher)
	if err != nil {
		setupLog.Error(err, "unable to add containerd event watcher")
		os.Exit(1)
	}

	if err = (&controller.NodeImageSetReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		NodeName:         nodeName,
		Recorder:         mgr.GetEventRecorderFor("nodeimageset-controller"),
		ContainerdClient: containerdClient,
		ImagePuller:      imagePuller,
		Queue:            queue,
	}).SetupWithManager(mgr, ch); err != nil {
		setupLog.Error(err, "unable to start controller", "controller", "nodeimageset")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting nodeimageset controller", "nodeName", nodeName)
	ctx := ctrl.SetupSignalHandler()

	go func() {
		<-ctx.Done()
		setupLog.Info("shutting down queue")
		queue.ShutDown()
	}()

	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running nodeimageset controller")
		os.Exit(1)
	}
}

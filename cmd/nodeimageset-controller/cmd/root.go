package cmd

import (
	"flag"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var config struct {
	webhookAddr       string
	metricsAddr       string
	probeAddr         string
	containerdAddress string
	namespace         string
	hostDir           string
	zapOpts           zap.Options
}

var rootCmd = &cobra.Command{
	Use:   "nodeimageset-controller",
	Short: "controller for NodeImageSet",
	Long:  `nodeimageset-controller is a Kubernetes controller for NodeImageSet.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return subMain()
	},
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&config.metricsAddr, "metrics-bind-address", ":8443", "The address the metric endpoint binds to.")
	pf.StringVar(&config.probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	pf.StringVar(&config.webhookAddr, "webhook-addr", ":9443", "The address the webhook server binds to.")
	pf.StringVar(
		&config.containerdAddress,
		"containerd-address",
		"/run/containerd/containerd.sock",
		"The address of containerd.",
	)
	pf.StringVar(&config.namespace, "namespace", "k8s.io", "The namespace of containerd.")
	pf.StringVar(&config.hostDir, "host-dir", "/var/lib/containerd", "The directory on the host that containerd uses.")

	goflags := flag.NewFlagSet("klog", flag.ExitOnError)
	klog.InitFlags(goflags)
	config.zapOpts.BindFlags(goflags)
	pf.AddGoFlagSet(goflags)
}

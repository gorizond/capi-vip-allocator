package main

import (
	"flag"
	"os"

	"github.com/go-logr/logr"
	"github.com/gorizond/capi-vip-allocator/pkg/controller"
	runtimeext "github.com/gorizond/capi-vip-allocator/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	scheme   = runtime.NewScheme()
	setupLog logr.Logger
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		enableLeaderElection bool
		probeAddr            string
		defaultPort          int
		runtimeExtPort       int
		enableRuntimeExt     bool
		runtimeExtName       string
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the health probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.IntVar(&defaultPort, "default-port", 6443, "Default control plane port to set when absent.")
	flag.IntVar(&runtimeExtPort, "runtime-extension-port", 9443, "The port for the runtime extension server.")
	flag.BoolVar(&enableRuntimeExt, "enable-runtime-extension", false, "Enable CAPI Runtime Extension server for BeforeClusterCreate hook.")
	flag.StringVar(&runtimeExtName, "runtime-extension-name", "vip-allocator", "The name of the runtime extension handler (must not contain dots).")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog = ctrl.Log.WithName("setup")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "capi-vip-allocator.gorizond.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	reconciler := &controller.ClusterReconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Logger:      ctrl.Log.WithName("controllers").WithName("Cluster"),
		DefaultPort: int32(defaultPort),
	}

	if err := reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Cluster")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// Start Runtime Extension server if enabled
	if enableRuntimeExt {
		setupLog.Info("runtime extension enabled", "port", runtimeExtPort, "name", runtimeExtName)
		certDir := "/tmp/runtime-extension/serving-certs"
		extServer := runtimeext.NewServer(mgr.GetClient(), ctrl.Log.WithName("runtime-extension"), runtimeExtPort, certDir, runtimeExtName)

		if err := mgr.Add(extServer); err != nil {
			setupLog.Error(err, "unable to add runtime extension server to manager")
			os.Exit(1)
		}
	} else {
		setupLog.Info("runtime extension disabled - using reconciler-only mode")
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

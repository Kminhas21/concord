// Command concord-operator reconciles TeamCoordinator resources.
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1alpha1 "github.com/Kminhas21/concord/api/v1alpha1"
	"github.com/Kminhas21/concord/internal/operator"
)

// version is set by ldflags (-X main.version=...); it picks the default concord image.
var version = "dev"

func main() {
	var cfg operator.Config
	var metricsAddr, probeAddr string
	var leaderElect bool
	flag.StringVar(&cfg.ConcordImage, "concord-image", "ghcr.io/kminhas21/concord:"+version, "default concord image (spec.image overrides)")
	flag.StringVar(&cfg.DragonflyImage, "dragonfly-image", "docker.dragonflydb.io/dragonflydb/dragonfly:latest", "default Dragonfly image (spec.dataStore.image overrides)")
	flag.BoolVar(&leaderElect, "leader-elect", false, "enable leader election")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8443", "metrics endpoint address (0 disables)")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "health probe address")
	flag.Parse()

	ctrl.SetLogger(zap.New())

	scheme := runtime.NewScheme()
	must(clientgoscheme.AddToScheme(scheme))
	must(v1alpha1.AddToScheme(scheme))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Cache:                  operator.CacheOptions(),
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "concord-operator.concord.kminhas21.github.io",
	})
	must(err)
	must(mgr.AddHealthzCheck("healthz", healthz.Ping))
	must(mgr.AddReadyzCheck("readyz", healthz.Ping))
	must((&operator.Reconciler{Client: mgr.GetClient(), Config: cfg}).SetupWithManager(mgr))
	must(mgr.Start(ctrl.SetupSignalHandler()))
}

func must(err error) {
	if err != nil {
		ctrl.Log.Error(err, "operator failed")
		os.Exit(1)
	}
}

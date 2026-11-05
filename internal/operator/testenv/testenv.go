//go:build !windows

// controller-runtime v0.25 envtest does not compile on Windows (upstream
// signalProcess redeclared); the Linux gate runs these.

// Package testenv is the shared envtest harness: a real kube-apiserver + etcd
// with the TeamCoordinator CRD installed. Tests skip when no assets are present.
package testenv

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/Kminhas21/concord/api/v1alpha1"
)

// Start boots envtest and returns its REST config and a client that knows the
// TeamCoordinator scheme. It skips the test when KUBEBUILDER_ASSETS is unset or
// CONCORD_TEST_SKIP_ENVTEST is set, and stops the environment on cleanup.
func Start(t *testing.T) (*rest.Config, client.Client) {
	t.Helper()
	if os.Getenv("CONCORD_TEST_SKIP_ENVTEST") != "" || os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("envtest skipped: KUBEBUILDER_ASSETS unset or CONCORD_TEST_SKIP_ENVTEST set")
	}
	_, self, _, _ := goruntime.Caller(0)
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(filepath.Dir(self), "..", "..", "..", "deploy", "helm", "concord-operator", "crds")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return cfg, c
}

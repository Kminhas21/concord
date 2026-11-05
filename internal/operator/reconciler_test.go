//go:build !windows

// controller-runtime v0.25 envtest does not compile on Windows (upstream
// signalProcess redeclared); the Linux gate runs these.

package operator_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/Kminhas21/concord/api/v1alpha1"
	"github.com/Kminhas21/concord/internal/operator"
	"github.com/Kminhas21/concord/internal/operator/testenv"
)

const (
	testConcordImage   = "example.com/concord:test"
	testDragonflyImage = "example.com/dragonfly:test"
	tokenHashAnno      = "concord.kminhas21.github.io/token-hash"
)

// startManager runs the reconciler against envtest for the life of the test.
func startManager(t *testing.T, cfg *rest.Config) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Cache:   operator.CacheOptions(),
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &operator.Reconciler{Client: mgr.GetClient(), Config: operator.Config{
		ConcordImage: testConcordImage, DragonflyImage: testDragonflyImage}}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

// eventually polls until get returns true or the deadline passes.
func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func TestReconciler(t *testing.T) {
	cfg, c := testenv.Start(t)
	startManager(t, cfg)
	ctx := context.Background()

	nsN := 0
	newNS := func(t *testing.T) string {
		t.Helper()
		nsN++
		ns := fmt.Sprintf("rec-%d", nsN)
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
			t.Fatal(err)
		}
		return ns
	}
	create := func(t *testing.T, ns, name string, mut func(*v1alpha1.TeamCoordinator)) *v1alpha1.TeamCoordinator {
		t.Helper()
		tc := newTC(name)
		tc.Namespace = ns
		if mut != nil {
			mut(tc)
		}
		if err := c.Create(ctx, tc); err != nil {
			t.Fatal(err)
		}
		return tc
	}
	key := func(ns, name string) client.ObjectKey { return client.ObjectKey{Namespace: ns, Name: name} }
	waitGet := func(t *testing.T, obj client.Object, ns, name string) {
		t.Helper()
		eventually(t, fmt.Sprintf("%T %s/%s", obj, ns, name), func() bool {
			return c.Get(ctx, key(ns, name), obj) == nil
		})
	}
	// ownedBy asserts a controller ownerReference to the CR.
	ownedBy := func(t *testing.T, o client.Object, tc *v1alpha1.TeamCoordinator) {
		t.Helper()
		ref := metav1.GetControllerOf(o)
		if ref == nil || ref.Kind != "TeamCoordinator" || ref.Name != tc.Name || ref.UID != tc.UID {
			t.Errorf("%T %s: controller ownerRef = %+v, want TeamCoordinator %s/%s", o, o.GetName(), ref, tc.Name, tc.UID)
		}
	}
	tokenOf := func(t *testing.T, ns, name string) string {
		t.Helper()
		var s corev1.Secret
		waitGet(t, &s, ns, name+"-token")
		return string(s.Data["token"])
	}
	podHash := func(t *testing.T, ns, name string) string {
		t.Helper()
		var d appsv1.Deployment
		waitGet(t, &d, ns, name+"-concord")
		return d.Spec.Template.Annotations[tokenHashAnno]
	}

	t.Run("CR produces the full object set", func(t *testing.T) {
		ns := newNS(t)
		tc := create(t, ns, "team", func(tc *v1alpha1.TeamCoordinator) { tc.Spec.Replicas = 2 })

		var dep appsv1.Deployment
		var sts appsv1.StatefulSet
		var csvc, dsvc corev1.Service
		var np networkingv1.NetworkPolicy
		var sec corev1.Secret
		waitGet(t, &dep, ns, "team-concord")
		waitGet(t, &sts, ns, "team-dragonfly")
		waitGet(t, &csvc, ns, "team-concord")
		waitGet(t, &dsvc, ns, "team-dragonfly")
		waitGet(t, &np, ns, "team-dragonfly")
		waitGet(t, &sec, ns, "team-token")
		for _, o := range []client.Object{&dep, &sts, &csvc, &dsvc, &np, &sec} {
			ownedBy(t, o, tc)
		}

		if *dep.Spec.Replicas != 2 {
			t.Errorf("replicas = %d, want 2", *dep.Spec.Replicas)
		}
		ct := dep.Spec.Template.Spec.Containers[0]
		if ct.Image != testConcordImage {
			t.Errorf("image = %s", ct.Image)
		}
		if p := ct.LivenessProbe.HTTPGet; p == nil || p.Path != "/healthz" {
			t.Errorf("liveness = %+v", ct.LivenessProbe)
		}
		if p := ct.ReadinessProbe; p.HTTPGet == nil || p.HTTPGet.Path != "/readyz" || p.TimeoutSeconds != 2 {
			t.Errorf("readiness = %+v", p)
		}
		env := map[string]corev1.EnvVar{}
		for _, e := range ct.Env {
			env[e.Name] = e
		}
		if env["CONCORD_ADDR"].Value != ":8080" || env["CONCORD_DRAGONFLY_ADDR"].Value != "team-dragonfly:6379" ||
			env["CONCORD_METRICS_ADDR"].Value != ":9464" || env["CONCORD_INTENT_TTL"].Value == "" {
			t.Errorf("env = %+v", ct.Env)
		}
		if ref := env["CONCORD_TOKEN"].ValueFrom; ref == nil || ref.SecretKeyRef.Name != "team-token" || ref.SecretKeyRef.Key != "token" {
			t.Errorf("CONCORD_TOKEN = %+v", env["CONCORD_TOKEN"])
		}
		tok := string(sec.Data["token"])
		if len(tok) != 64 {
			t.Errorf("token length = %d, want 64 hex chars", len(tok))
		}
		if sec.Labels["app.kubernetes.io/managed-by"] != "concord-operator" {
			t.Errorf("token Secret labels = %v", sec.Labels)
		}
		if got := dep.Spec.Template.Annotations[tokenHashAnno]; got != operator.TokenHash([]byte(tok)) {
			t.Errorf("token-hash = %s", got)
		}
		if dep.Spec.Selector.MatchLabels["app.kubernetes.io/instance"] != "team" ||
			dep.Spec.Selector.MatchLabels["app.kubernetes.io/component"] != "coordination" || len(dep.Spec.Selector.MatchLabels) != 3 {
			t.Errorf("selector = %v", dep.Spec.Selector.MatchLabels)
		}

		if sts.Spec.Template.Spec.Containers[0].Image != testDragonflyImage || *sts.Spec.Replicas != 1 || len(sts.Spec.VolumeClaimTemplates) != 0 {
			t.Errorf("statefulset = %+v", sts.Spec)
		}
		portsOf := func(s corev1.Service) (out []int32) {
			for _, p := range s.Spec.Ports {
				out = append(out, p.Port)
			}
			return
		}
		if fmt.Sprint(portsOf(csvc)) != "[8080 9464]" || fmt.Sprint(portsOf(dsvc)) != "[6379]" || dsvc.Spec.ClusterIP != "None" {
			t.Errorf("services: concord %v dragonfly %v clusterIP %s", portsOf(csvc), portsOf(dsvc), dsvc.Spec.ClusterIP)
		}
		if np.Spec.Ingress[0].From[0].PodSelector.MatchLabels["app.kubernetes.io/component"] != "coordination" {
			t.Errorf("networkpolicy = %+v", np.Spec)
		}
	})

	t.Run("spec images override the flags", func(t *testing.T) {
		ns := newNS(t)
		create(t, ns, "pinned", func(tc *v1alpha1.TeamCoordinator) {
			tc.Spec.Image = "example.com/concord:pinned"
			tc.Spec.DataStore.Image = "example.com/dragonfly:pinned"
		})
		var dep appsv1.Deployment
		var sts appsv1.StatefulSet
		waitGet(t, &dep, ns, "pinned-concord")
		waitGet(t, &sts, ns, "pinned-dragonfly")
		if dep.Spec.Template.Spec.Containers[0].Image != "example.com/concord:pinned" ||
			sts.Spec.Template.Spec.Containers[0].Image != "example.com/dragonfly:pinned" {
			t.Errorf("images = %s / %s", dep.Spec.Template.Spec.Containers[0].Image, sts.Spec.Template.Spec.Containers[0].Image)
		}
	})

	t.Run("two CRs in one namespace do not collide", func(t *testing.T) {
		ns := newNS(t)
		a := create(t, ns, "alpha", nil)
		b := create(t, ns, "beta", nil)
		for _, tc := range []*v1alpha1.TeamCoordinator{a, b} {
			var dep appsv1.Deployment
			var sec corev1.Secret
			waitGet(t, &dep, ns, tc.Name+"-concord")
			waitGet(t, &sec, ns, tc.Name+"-token")
			ownedBy(t, &dep, tc)
			ownedBy(t, &sec, tc)
			if dep.Spec.Selector.MatchLabels["app.kubernetes.io/instance"] != tc.Name {
				t.Errorf("%s selector = %v", tc.Name, dep.Spec.Selector.MatchLabels)
			}
		}
		if tokenOf(t, ns, "alpha") == tokenOf(t, ns, "beta") {
			t.Error("two CRs share a token")
		}
	})

	t.Run("token stable across reconciles", func(t *testing.T) {
		ns := newNS(t)
		tc := create(t, ns, "stable", nil)
		first, hash := tokenOf(t, ns, "stable"), podHash(t, ns, "stable")
		// Bump the generation: a spec change triggers a fresh reconcile.
		eventually(t, "replicas bump", func() bool {
			if err := c.Get(ctx, key(ns, tc.Name), tc); err != nil {
				return false
			}
			tc.Spec.Replicas = 3
			return c.Update(ctx, tc) == nil
		})
		eventually(t, "deployment scaled to 3", func() bool {
			var d appsv1.Deployment
			return c.Get(ctx, key(ns, "stable-concord"), &d) == nil && *d.Spec.Replicas == 3
		})
		if got := tokenOf(t, ns, "stable"); got != first {
			t.Errorf("token changed across reconcile")
		}
		if got := podHash(t, ns, "stable"); got != hash {
			t.Errorf("token-hash changed across reconcile")
		}
	})

	t.Run("deleted token is regenerated and rolls the pods", func(t *testing.T) {
		ns := newNS(t)
		create(t, ns, "rotate", nil)
		old, oldHash := tokenOf(t, ns, "rotate"), podHash(t, ns, "rotate")
		if err := c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "rotate-token"}}); err != nil {
			t.Fatal(err)
		}
		eventually(t, "new token", func() bool {
			var s corev1.Secret
			return c.Get(ctx, key(ns, "rotate-token"), &s) == nil && string(s.Data["token"]) != old
		})
		eventually(t, "new token-hash", func() bool {
			var d appsv1.Deployment
			return c.Get(ctx, key(ns, "rotate-concord"), &d) == nil && d.Spec.Template.Annotations[tokenHashAnno] != oldHash
		})
	})

	t.Run("drift on replicas is reconciled back", func(t *testing.T) {
		ns := newNS(t)
		create(t, ns, "drift", func(tc *v1alpha1.TeamCoordinator) { tc.Spec.Replicas = 2 })
		var dep appsv1.Deployment
		waitGet(t, &dep, ns, "drift-concord")
		patch := client.MergeFrom(dep.DeepCopy())
		five := int32(5)
		dep.Spec.Replicas = &five
		if err := c.Patch(ctx, &dep, patch); err != nil {
			t.Fatal(err)
		}
		eventually(t, "replicas back to 2", func() bool {
			var d appsv1.Deployment
			return c.Get(ctx, key(ns, "drift-concord"), &d) == nil && *d.Spec.Replicas == 2
		})
	})

	t.Run("persistence adds a volumeClaimTemplate", func(t *testing.T) {
		ns := newNS(t)
		create(t, ns, "pvc", func(tc *v1alpha1.TeamCoordinator) { tc.Spec.DataStore.Persistence = true })
		var sts appsv1.StatefulSet
		waitGet(t, &sts, ns, "pvc-dragonfly")
		if len(sts.Spec.VolumeClaimTemplates) != 1 {
			t.Fatalf("volumeClaimTemplates = %d", len(sts.Spec.VolumeClaimTemplates))
		}
		q := sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]
		if !strings.HasPrefix(q.String(), "1Gi") {
			t.Errorf("storage = %s, want 1Gi", q.String())
		}
	})
}

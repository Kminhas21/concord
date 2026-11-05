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
	eventsv1 "k8s.io/api/events/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	r := &operator.Reconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(),
		Recorder: mgr.GetEventRecorder("concord-operator"), Config: operator.Config{
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

	// cond returns the named status condition of the CR, or nil.
	cond := func(ns, name, typ string) *metav1.Condition {
		var tc v1alpha1.TeamCoordinator
		if c.Get(ctx, key(ns, name), &tc) != nil {
			return nil
		}
		return meta.FindStatusCondition(tc.Status.Conditions, typ)
	}
	waitCond := func(t *testing.T, ns, name, typ string, status metav1.ConditionStatus, reason string) {
		t.Helper()
		eventually(t, fmt.Sprintf("%s/%s %s=%s (%s)", ns, name, typ, status, reason), func() bool {
			cd := cond(ns, name, typ)
			return cd != nil && cd.Status == status && (reason == "" || cd.Reason == reason)
		})
	}
	// warningNote waits for a Warning Event with reason on the CR and returns its note.
	warningNote := func(t *testing.T, ns, name, reason string) string {
		t.Helper()
		var note string
		eventually(t, "Warning event "+reason, func() bool {
			var l eventsv1.EventList
			if c.List(ctx, &l, client.InNamespace(ns)) != nil {
				return false
			}
			for _, e := range l.Items {
				if e.Regarding.Name == name && e.Type == corev1.EventTypeWarning && e.Reason == reason {
					note = e.Note
					return true
				}
			}
			return false
		})
		return note
	}
	absent := func(t *testing.T, obj client.Object, ns, name string) {
		t.Helper()
		if err := c.Get(ctx, key(ns, name), obj); !apierrors.IsNotFound(err) {
			t.Errorf("%T %s/%s: want NotFound, got %v", obj, ns, name, err)
		}
	}

	t.Run("fresh CR reports Progressing, endpoint and observedGeneration", func(t *testing.T) {
		ns := newNS(t)
		tc := create(t, ns, "fresh", nil)
		waitCond(t, ns, "fresh", "Progressing", metav1.ConditionTrue, "Reconciling")
		waitCond(t, ns, "fresh", "Ready", metav1.ConditionFalse, "Progressing")
		var got v1alpha1.TeamCoordinator
		if err := c.Get(ctx, key(ns, "fresh"), &got); err != nil {
			t.Fatal(err)
		}
		if want := "fresh-concord." + ns + ".svc.cluster.local:8080"; got.Status.Endpoint != want {
			t.Errorf("endpoint = %q, want %q", got.Status.Endpoint, want)
		}
		if got.Status.ObservedGeneration != tc.Generation || tc.Generation == 0 {
			t.Errorf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, tc.Generation)
		}
		if r := cond(ns, "fresh", "Ready"); r.ObservedGeneration != tc.Generation {
			t.Errorf("condition observedGeneration = %d", r.ObservedGeneration)
		}
	})

	t.Run("Ready follows the Deployment and StatefulSet status", func(t *testing.T) {
		ns := newNS(t)
		create(t, ns, "up", func(tc *v1alpha1.TeamCoordinator) { tc.Spec.Replicas = 2 })
		var dep appsv1.Deployment
		var sts appsv1.StatefulSet
		waitGet(t, &dep, ns, "up-concord")
		waitGet(t, &sts, ns, "up-dragonfly")
		waitCond(t, ns, "up", "Progressing", metav1.ConditionTrue, "Reconciling")
		dep.Status = appsv1.DeploymentStatus{ObservedGeneration: dep.Generation, Replicas: 2, UpdatedReplicas: 2, ReadyReplicas: 2, AvailableReplicas: 2}
		if err := c.Status().Update(ctx, &dep); err != nil {
			t.Fatal(err)
		}
		// Deployment alone is not enough: Dragonfly must be ready too.
		time.Sleep(time.Second)
		if cd := cond(ns, "up", "Ready"); cd == nil || cd.Status != metav1.ConditionFalse {
			t.Errorf("Ready before Dragonfly is ready = %+v", cd)
		}
		sts.Status = appsv1.StatefulSetStatus{ObservedGeneration: sts.Generation, Replicas: 1, ReadyReplicas: 1, CurrentRevision: "r1", UpdateRevision: "r1"}
		if err := c.Status().Update(ctx, &sts); err != nil {
			t.Fatal(err)
		}
		waitCond(t, ns, "up", "Ready", metav1.ConditionTrue, "Available")
		waitCond(t, ns, "up", "Progressing", metav1.ConditionFalse, "")
		var got v1alpha1.TeamCoordinator
		if err := c.Get(ctx, key(ns, "up"), &got); err != nil {
			t.Fatal(err)
		}
		if got.Status.ReadyReplicas != 2 {
			t.Errorf("readyReplicas = %d, want 2", got.Status.ReadyReplicas)
		}
	})

	t.Run("a rollout in flight is Progressing, not Ready", func(t *testing.T) {
		ns := newNS(t)
		create(t, ns, "roll", nil)
		var dep appsv1.Deployment
		var sts appsv1.StatefulSet
		waitGet(t, &dep, ns, "roll-concord")
		waitGet(t, &sts, ns, "roll-dragonfly")
		dep.Status = appsv1.DeploymentStatus{ObservedGeneration: dep.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
		sts.Status = appsv1.StatefulSetStatus{ObservedGeneration: sts.Generation, Replicas: 1, ReadyReplicas: 1, CurrentRevision: "r1", UpdateRevision: "r1"}
		if err := c.Status().Update(ctx, &dep); err != nil {
			t.Fatal(err)
		}
		if err := c.Status().Update(ctx, &sts); err != nil {
			t.Fatal(err)
		}
		waitCond(t, ns, "roll", "Ready", metav1.ConditionTrue, "Available")
		oldGen := dep.Generation

		// Spec change bumps the Deployment generation; the old pod stays available.
		var tc v1alpha1.TeamCoordinator
		if err := c.Get(ctx, key(ns, "roll"), &tc); err != nil {
			t.Fatal(err)
		}
		tc.Spec.Image = "ghcr.io/kminhas21/concord:next"
		if err := c.Update(ctx, &tc); err != nil {
			t.Fatal(err)
		}
		eventually(t, "Deployment generation bump", func() bool {
			return c.Get(ctx, key(ns, "roll-concord"), &dep) == nil && dep.Generation > oldGen
		})
		waitCond(t, ns, "roll", "Ready", metav1.ConditionFalse, "Progressing")
		waitCond(t, ns, "roll", "Progressing", metav1.ConditionTrue, "Reconciling")

		dep.Status = appsv1.DeploymentStatus{ObservedGeneration: dep.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
		if err := c.Status().Update(ctx, &dep); err != nil {
			t.Fatal(err)
		}
		waitCond(t, ns, "roll", "Ready", metav1.ConditionTrue, "Available")
		waitCond(t, ns, "roll", "Progressing", metav1.ConditionFalse, "")
	})

	t.Run("status is not rewritten when nothing changed", func(t *testing.T) {
		ns := newNS(t)
		create(t, ns, "quiet", nil)
		waitCond(t, ns, "quiet", "Progressing", metav1.ConditionTrue, "Reconciling")
		time.Sleep(time.Second)
		var before v1alpha1.TeamCoordinator
		if err := c.Get(ctx, key(ns, "quiet"), &before); err != nil {
			t.Fatal(err)
		}
		// Touch an owned child: the watch re-runs the reconcile, which must not rewrite status.
		var dep appsv1.Deployment
		waitGet(t, &dep, ns, "quiet-concord")
		if err := c.Patch(ctx, &dep, client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"annotations":{"x":"y"}}}`))); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		var after v1alpha1.TeamCoordinator
		if err := c.Get(ctx, key(ns, "quiet"), &after); err != nil {
			t.Fatal(err)
		}
		if before.ResourceVersion != after.ResourceVersion {
			t.Errorf("resourceVersion %s -> %s: status rewritten on a no-op reconcile", before.ResourceVersion, after.ResourceVersion)
		}
	})

	t.Run("secretRef to an existing Secret is used, none generated", func(t *testing.T) {
		ns := newNS(t)
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "mine"},
			Data: map[string][]byte{"tok": []byte("s3cret-value")}}
		if err := c.Create(ctx, sec); err != nil {
			t.Fatal(err)
		}
		create(t, ns, "byo", func(tc *v1alpha1.TeamCoordinator) {
			tc.Spec.Auth.SecretRef = &v1alpha1.SecretRef{Name: "mine", Key: "tok"}
		})
		var dep appsv1.Deployment
		waitGet(t, &dep, ns, "byo-concord")
		var ref *corev1.SecretKeySelector
		for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
			if e.Name == "CONCORD_TOKEN" && e.ValueFrom != nil {
				ref = e.ValueFrom.SecretKeyRef
			}
		}
		if ref == nil || ref.Name != "mine" || ref.Key != "tok" {
			t.Errorf("CONCORD_TOKEN = %+v", ref)
		}
		if got := dep.Spec.Template.Annotations[tokenHashAnno]; got != operator.TokenHash([]byte("s3cret-value")) {
			t.Errorf("token-hash = %s", got)
		}
		absent(t, &corev1.Secret{}, ns, "byo-token")
	})

	t.Run("secretRef missing blocks the Deployment until the Secret appears", func(t *testing.T) {
		ns := newNS(t)
		create(t, ns, "wait", func(tc *v1alpha1.TeamCoordinator) {
			tc.Spec.Auth.SecretRef = &v1alpha1.SecretRef{Name: "later", Key: "token"}
		})
		waitCond(t, ns, "wait", "Ready", metav1.ConditionFalse, "SecretNotFound")
		waitCond(t, ns, "wait", "Progressing", metav1.ConditionFalse, "SecretNotFound")
		warningNote(t, ns, "wait", "SecretNotFound")
		var sts appsv1.StatefulSet
		waitGet(t, &sts, ns, "wait-dragonfly") // other children still apply
		absent(t, &appsv1.Deployment{}, ns, "wait-concord")
		absent(t, &corev1.Secret{}, ns, "wait-token")

		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "later"},
			Data: map[string][]byte{"token": []byte("abc")}}
		if err := c.Create(ctx, sec); err != nil {
			t.Fatal(err)
		}
		var dep appsv1.Deployment
		waitGet(t, &dep, ns, "wait-concord") // via backoff retry
		waitCond(t, ns, "wait", "Ready", metav1.ConditionFalse, "Progressing")
	})

	t.Run("secretRef with a missing key is SecretNotFound and leaks no value", func(t *testing.T) {
		ns := newNS(t)
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "wrongkey"},
			Data: map[string][]byte{"other": []byte("do-not-leak")}}
		if err := c.Create(ctx, sec); err != nil {
			t.Fatal(err)
		}
		create(t, ns, "nokey", func(tc *v1alpha1.TeamCoordinator) {
			tc.Spec.Auth.SecretRef = &v1alpha1.SecretRef{Name: "wrongkey", Key: "token"}
		})
		waitCond(t, ns, "nokey", "Ready", metav1.ConditionFalse, "SecretNotFound")
		note := warningNote(t, ns, "nokey", "SecretNotFound")
		if !strings.Contains(note, `"token"`) || strings.Contains(note, "do-not-leak") {
			t.Errorf("event note = %q", note)
		}
		absent(t, &appsv1.Deployment{}, ns, "nokey-concord")
	})

	t.Run("pre-existing unlabelled token Secret is reported, never adopted", func(t *testing.T) {
		ns := newNS(t)
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "unm-token"},
			Data: map[string][]byte{"token": []byte("users-own")}}
		if err := c.Create(ctx, sec); err != nil {
			t.Fatal(err)
		}
		create(t, ns, "unm", nil)
		waitCond(t, ns, "unm", "Ready", metav1.ConditionFalse, "TokenSecretUnmanaged")
		warningNote(t, ns, "unm", "TokenSecretUnmanaged")
		absent(t, &appsv1.Deployment{}, ns, "unm-concord")
		var got corev1.Secret
		if err := c.Get(ctx, key(ns, "unm-token"), &got); err != nil {
			t.Fatal(err)
		}
		if got.ResourceVersion != sec.ResourceVersion || len(got.Labels) != 0 || len(got.OwnerReferences) != 0 ||
			string(got.Data["token"]) != "users-own" {
			t.Errorf("secret was touched: %+v", got.ObjectMeta)
		}
	})
}

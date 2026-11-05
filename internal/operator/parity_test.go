package operator_test

import (
	"bytes"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/Kminhas21/concord/api/v1alpha1"
	"github.com/Kminhas21/concord/internal/operator"
)

// TestChartParity locks the two install paths together: the operator's builders
// must agree with deploy/helm/concord (the shipped reference) on the fields a
// client or the cluster relies on. Accepted deltas are not asserted: labels
// beyond selectors and the three app.kubernetes.io ones named below, NATS env
// (the operator never sets it), and the TTL string form (compared as a
// time.Duration). The Dragonfly objects are named <cr>-dragonfly by the spec
// (the chart says <release>-concord-dragonfly); df() maps the chart's name onto
// the spec's so the rest of each object is still compared. CONCORD_TOKEN is always set by the operator, so the chart is
// rendered with auth.existingSecret set.
func TestChartParity(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH; the CI Linux gate installs it")
	}
	const name = "cu"
	out, err := exec.Command("helm", "template", name, "../../deploy/helm/concord",
		"--set", "concord.auth.existingSecret="+name+"-token",
		"--set", "concord.replicaCount=3",
		"--set", "concord.intentTTL=10m",
		"--set", "image.tag=dev").CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	var (
		dep  appsv1.Deployment
		sts  appsv1.StatefulSet
		np   networkingv1.NetworkPolicy
		svcs = map[string]corev1.Service{}
	)
	for _, doc := range bytes.Split(out, []byte("\n---\n")) {
		var tm metav1.TypeMeta
		if err := yaml.Unmarshal(doc, &tm); err != nil || tm.Kind == "" {
			continue
		}
		switch tm.Kind {
		case "Deployment":
			must(t, yaml.Unmarshal(doc, &dep))
		case "StatefulSet":
			must(t, yaml.Unmarshal(doc, &sts))
		case "NetworkPolicy":
			must(t, yaml.Unmarshal(doc, &np))
		case "Service":
			var s corev1.Service
			must(t, yaml.Unmarshal(doc, &s))
			svcs[s.Name] = s
		}
	}

	ttl := metav1.Duration{Duration: 10 * time.Minute}
	tc := &v1alpha1.TeamCoordinator{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1alpha1.TeamCoordinatorSpec{Repo: "r", Replicas: 3, IntentTTL: &ttl},
	}
	cfg := operator.Config{
		ConcordImage:   operator.DefaultConcordRepository + ":dev",
		DragonflyImage: operator.DefaultDragonflyImage,
	}
	ref := operator.TokenRef{Name: operator.TokenName(tc), Key: operator.TokenKey}
	opDep := operator.BuildConcordDeployment(tc, cfg, ref, "h")
	opSts := operator.BuildDragonflyStatefulSet(tc, cfg)
	opNP := operator.BuildDragonflyNetworkPolicy(tc)

	t.Run("concord deployment", func(t *testing.T) {
		eq(t, "name", opDep.Name, dep.Name)
		eq(t, "replicas", *opDep.Spec.Replicas, *dep.Spec.Replicas)
		eq(t, "selector", opDep.Spec.Selector.MatchLabels, dep.Spec.Selector.MatchLabels)
		eq(t, "pod labels", opDep.Spec.Template.Labels, dep.Spec.Template.Labels)
		eq(t, "pod securityContext", opDep.Spec.Template.Spec.SecurityContext, dep.Spec.Template.Spec.SecurityContext)
		oc, cc := opDep.Spec.Template.Spec.Containers[0], dep.Spec.Template.Spec.Containers[0]
		eq(t, "image", oc.Image, cc.Image)
		eq(t, "ports", oc.Ports, cc.Ports)
		eq(t, "liveness", oc.LivenessProbe, cc.LivenessProbe)
		eq(t, "readiness", oc.ReadinessProbe, cc.ReadinessProbe)
		eq(t, "container securityContext", oc.SecurityContext, cc.SecurityContext)
		oe, ce := envMap(oc.Env), envMap(cc.Env)
		ce["CONCORD_DRAGONFLY_ADDR"] = df(ce["CONCORD_DRAGONFLY_ADDR"])
		eqTTL(t, oe["CONCORD_INTENT_TTL"], ce["CONCORD_INTENT_TTL"])
		delete(oe, "CONCORD_INTENT_TTL")
		delete(ce, "CONCORD_INTENT_TTL")
		eq(t, "env", oe, ce)
	})

	t.Run("dragonfly statefulset", func(t *testing.T) {
		eq(t, "name", opSts.Name, df(sts.Name))
		eq(t, "serviceName", opSts.Spec.ServiceName, df(sts.Spec.ServiceName))
		eq(t, "selector", opSts.Spec.Selector.MatchLabels, sts.Spec.Selector.MatchLabels)
		oc, cc := opSts.Spec.Template.Spec.Containers[0], sts.Spec.Template.Spec.Containers[0]
		eq(t, "image", oc.Image, cc.Image)
		eq(t, "ports", oc.Ports, cc.Ports)
		eq(t, "liveness", oc.LivenessProbe, cc.LivenessProbe)
		eq(t, "readiness", oc.ReadinessProbe, cc.ReadinessProbe)
	})

	t.Run("services", func(t *testing.T) {
		for chartName, opSvc := range map[string]*corev1.Service{
			name + "-concord":           operator.BuildConcordService(tc),
			name + "-concord-dragonfly": operator.BuildDragonflyService(tc),
		} {
			cs, ok := svcs[chartName]
			if !ok {
				t.Fatalf("chart renders no Service %q", chartName)
			}
			eq(t, chartName+" name", opSvc.Name, df(cs.Name))
			eq(t, chartName+" type", opSvc.Spec.Type, cs.Spec.Type)
			eq(t, chartName+" clusterIP", opSvc.Spec.ClusterIP, cs.Spec.ClusterIP)
			eq(t, chartName+" selector", opSvc.Spec.Selector, cs.Spec.Selector)
			eq(t, chartName+" ports", opSvc.Spec.Ports, cs.Spec.Ports)
		}
	})

	t.Run("networkpolicy", func(t *testing.T) {
		eq(t, "name", opNP.Name, df(np.Name))
		eq(t, "podSelector", opNP.Spec.PodSelector, np.Spec.PodSelector)
		eq(t, "policyTypes", opNP.Spec.PolicyTypes, np.Spec.PolicyTypes)
		eq(t, "ingress", opNP.Spec.Ingress, np.Spec.Ingress)
	})

	t.Run("shared app.kubernetes.io labels", func(t *testing.T) {
		for _, p := range []struct {
			what      string
			op, chart map[string]string
		}{{"deployment", opDep.Labels, dep.Labels}, {"statefulset", opSts.Labels, sts.Labels}} {
			for _, k := range []string{"name", "instance", "component"} {
				k = "app.kubernetes.io/" + k
				eq(t, p.what+" label "+k, p.op[k], p.chart[k])
			}
		}
	})
}

// envMap flattens env vars; a secretKeyRef becomes "secret:<name>/<key>".
func envMap(env []corev1.EnvVar) map[string]string {
	m := map[string]string{}
	for _, e := range env {
		if r := e.ValueFrom; r != nil && r.SecretKeyRef != nil {
			m[e.Name] = "secret:" + r.SecretKeyRef.Name + "/" + r.SecretKeyRef.Key
		} else {
			m[e.Name] = e.Value
		}
	}
	return m
}

// df maps the chart's Dragonfly object name onto the spec's <cr>-dragonfly.
func df(s string) string { return strings.ReplaceAll(s, "-concord-dragonfly", "-dragonfly") }

func eqTTL(t *testing.T, op, chart string) {
	t.Helper()
	o, err1 := time.ParseDuration(op)
	c, err2 := time.ParseDuration(chart)
	if err1 != nil || err2 != nil || o != c {
		t.Errorf("CONCORD_INTENT_TTL: operator %q, chart %q", op, chart)
	}
}

func eq[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n operator %+v\n chart    %+v", what, got, want)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

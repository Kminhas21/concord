// Package operator reconciles TeamCoordinator resources into a concord
// coordination unit. The builders here are pure (TeamCoordinator -> typed
// object) and mirror deploy/helm/concord.
package operator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	v1alpha1 "github.com/Kminhas21/concord/api/v1alpha1"
)

const (
	// FieldManager is the server-side-apply field manager.
	FieldManager = "concord-operator"
	// ManagedBy is the managed-by label value; the Secret cache filters on it.
	ManagedBy = "concord-operator"
	// TokenHashAnnotation on the pod template rolls pods when the token changes.
	TokenHashAnnotation = "concord.kminhas21.github.io/token-hash"
	// TokenKey is the key of the generated token Secret.
	TokenKey = "token"

	rpcPort     = 8080
	metricsPort = 9464
	redisPort   = 6379
)

// Config carries the operator-wide image defaults (flags).
type Config struct {
	ConcordImage   string
	DragonflyImage string
}

// TokenRef names the Secret key that holds the team token.
type TokenRef struct{ Name, Key string }

func concordName(tc *v1alpha1.TeamCoordinator) string   { return tc.Name + "-concord" }
func dragonflyName(tc *v1alpha1.TeamCoordinator) string { return tc.Name + "-dragonfly" }

// TokenName is the generated token Secret's name.
func TokenName(tc *v1alpha1.TeamCoordinator) string { return tc.Name + "-token" }

// TokenHash is the SHA-256 hex of the token value.
func TokenHash(token []byte) string {
	h := sha256.Sum256(token)
	return hex.EncodeToString(h[:])
}

func selector(tc *v1alpha1.TeamCoordinator, component string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      "concord",
		"app.kubernetes.io/instance":  tc.Name,
		"app.kubernetes.io/component": component,
	}
}

func objLabels(tc *v1alpha1.TeamCoordinator, component string) map[string]string {
	l := selector(tc, component)
	l["app.kubernetes.io/managed-by"] = ManagedBy
	return l
}

func meta(tc *v1alpha1.TeamCoordinator, name, component string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: tc.Namespace,
		Labels:    objLabels(tc, component),
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion:         v1alpha1.GroupVersion.String(),
			Kind:               "TeamCoordinator",
			Name:               tc.Name,
			UID:                tc.UID,
			Controller:         ptr.To(true),
			BlockOwnerDeletion: ptr.To(true),
		}},
	}
}

// BuildTokenSecret returns the generated token Secret holding value.
func BuildTokenSecret(tc *v1alpha1.TeamCoordinator, value []byte) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: meta(tc, TokenName(tc), "coordination"),
		Data:       map[string][]byte{TokenKey: value},
	}
}

// BuildConcordDeployment mirrors the chart's concord Deployment.
func BuildConcordDeployment(tc *v1alpha1.TeamCoordinator, cfg Config, ref TokenRef, tokenHash string) *appsv1.Deployment {
	image := cfg.ConcordImage
	if tc.Spec.Image != "" {
		image = tc.Spec.Image
	}
	ttl := 600 * time.Second
	if tc.Spec.IntentTTL != nil {
		ttl = tc.Spec.IntentTTL.Duration
	}
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: meta(tc, concordName(tc), "coordination"),
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(tc.Spec.Replicas),
			Selector: &metav1.LabelSelector{MatchLabels: selector(tc, "coordination")},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      selector(tc, "coordination"),
					Annotations: map[string]string{TokenHashAnnotation: tokenHash},
				},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr.To(true),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:            "concord",
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Env: []corev1.EnvVar{
							{Name: "CONCORD_ADDR", Value: fmt.Sprintf(":%d", rpcPort)},
							{Name: "CONCORD_DRAGONFLY_ADDR", Value: fmt.Sprintf("%s:%d", dragonflyName(tc), redisPort)},
							{Name: "CONCORD_INTENT_TTL", Value: ttl.String()},
							{Name: "CONCORD_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name}, Key: ref.Key}}},
							{Name: "CONCORD_METRICS_ADDR", Value: fmt.Sprintf(":%d", metricsPort)},
						},
						Ports: []corev1.ContainerPort{
							{Name: "rpc", ContainerPort: rpcPort},
							{Name: "metrics", ContainerPort: metricsPort},
						},
						LivenessProbe: &corev1.Probe{
							ProbeHandler:        httpGet("/healthz"),
							InitialDelaySeconds: 2, PeriodSeconds: 10,
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler:        httpGet("/readyz"),
							InitialDelaySeconds: 2, PeriodSeconds: 10, TimeoutSeconds: 2,
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							ReadOnlyRootFilesystem:   ptr.To(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						Resources: tc.Spec.Resources,
					}},
				},
			},
		},
	}
}

func httpGet(path string) corev1.ProbeHandler {
	return corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromString("rpc")}}
}

// BuildConcordService mirrors the chart's concord Service.
func BuildConcordService(tc *v1alpha1.TeamCoordinator) *corev1.Service {
	return &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: meta(tc, concordName(tc), "coordination"),
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selector(tc, "coordination"),
			Ports: []corev1.ServicePort{
				{Name: "rpc", Port: rpcPort, TargetPort: intstr.FromString("rpc")},
				{Name: "metrics", Port: metricsPort, TargetPort: intstr.FromString("metrics")},
			},
		},
	}
}

// BuildDragonflyStatefulSet mirrors the chart's Dragonfly StatefulSet.
func BuildDragonflyStatefulSet(tc *v1alpha1.TeamCoordinator, cfg Config) *appsv1.StatefulSet {
	image := cfg.DragonflyImage
	if tc.Spec.DataStore.Image != "" {
		image = tc.Spec.DataStore.Image
	}
	redis := func() corev1.ProbeHandler {
		return corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromString("redis")}}
	}
	sts := &appsv1.StatefulSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: meta(tc, dragonflyName(tc), "datastore"),
		Spec: appsv1.StatefulSetSpec{
			ServiceName: dragonflyName(tc),
			Replicas:    ptr.To(int32(1)),
			Selector:    &metav1.LabelSelector{MatchLabels: selector(tc, "datastore")},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: selector(tc, "datastore")},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  "dragonfly",
					Image: image,
					Ports: []corev1.ContainerPort{{Name: "redis", ContainerPort: redisPort}},
					ReadinessProbe: &corev1.Probe{ProbeHandler: redis(),
						InitialDelaySeconds: 2, PeriodSeconds: 5},
					LivenessProbe: &corev1.Probe{ProbeHandler: redis(),
						InitialDelaySeconds: 10, PeriodSeconds: 15},
					VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
				}}},
			},
		},
	}
	if !tc.Spec.DataStore.Persistence {
		sts.Spec.Template.Spec.Volumes = []corev1.Volume{{Name: "data",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
		return sts
	}
	size := resource.MustParse("1Gi")
	if tc.Spec.DataStore.Storage != nil {
		size = *tc.Spec.DataStore.Storage
	}
	sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{Name: "data"},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: size}},
		},
	}}
	return sts
}

// BuildDragonflyService mirrors the chart's headless Dragonfly Service.
func BuildDragonflyService(tc *v1alpha1.TeamCoordinator) *corev1.Service {
	return &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: meta(tc, dragonflyName(tc), "datastore"),
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  selector(tc, "datastore"),
			Ports:     []corev1.ServicePort{{Name: "redis", Port: redisPort, TargetPort: intstr.FromString("redis")}},
		},
	}
}

// BuildDragonflyNetworkPolicy admits Dragonfly ingress only from this unit's concord pods.
func BuildDragonflyNetworkPolicy(tc *v1alpha1.TeamCoordinator) *networkingv1.NetworkPolicy {
	tcp := corev1.ProtocolTCP
	port := intstr.FromInt32(redisPort)
	return &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: meta(tc, dragonflyName(tc), "datastore"),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: selector(tc, "datastore")},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From:  []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: selector(tc, "coordination")}}},
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &port}},
			}},
		},
	}
}

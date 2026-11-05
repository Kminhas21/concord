package operator

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Kminhas21/concord/api/v1alpha1"
)

// +kubebuilder:rbac:groups=concord.kminhas21.github.io,resources=teamcoordinators,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=concord.kminhas21.github.io,resources=teamcoordinators/finalizers,verbs=update
// +kubebuilder:rbac:groups=concord.kminhas21.github.io,resources=teamcoordinators/status,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services;secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler turns each TeamCoordinator into its coordination unit.
type Reconciler struct {
	client.Client
	// APIReader reads uncached: secretRef Secrets and unlabelled token Secrets
	// are invisible to the label-filtered cache.
	APIReader client.Reader
	Recorder  events.EventRecorder
	Config    Config
}

// CacheOptions label-filters the Secret cache to Secrets this operator generated.
func CacheOptions() cache.Options {
	return cache.Options{ByObject: map[client.Object]cache.ByObject{
		&corev1.Secret{}: {Label: labels.SelectorFromSet(labels.Set{"app.kubernetes.io/managed-by": ManagedBy})},
	}}
}

// SetupWithManager registers the controller; no timed requeue, only watches.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.TeamCoordinator{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}

// block is a known reason the Deployment cannot be applied.
type block struct{ Reason, Message string }

// Reconcile applies the object set for one TeamCoordinator and reports its status.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var tc v1alpha1.TeamCoordinator
	if err := r.Get(ctx, req.NamespacedName, &tc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !tc.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	b, err := r.converge(ctx, &tc)
	if err != nil {
		b = &block{"ReconcileError", err.Error()}
	}
	if serr := r.updateStatus(ctx, &tc, b); serr != nil && err == nil && b == nil {
		return ctrl.Result{}, serr
	}
	if b != nil {
		r.Recorder.Eventf(&tc, nil, corev1.EventTypeWarning, b.Reason, "Reconcile", "%s", b.Message)
		if err == nil {
			err = errors.New(b.Message) // backoff retry: the Secret may appear later
		}
	}
	return ctrl.Result{}, err
}

// converge applies the children. A non-nil block means the concord Deployment
// was deliberately not applied; the other children still are.
func (r *Reconciler) converge(ctx context.Context, tc *v1alpha1.TeamCoordinator) (*block, error) {
	ref, token, b, err := r.resolveToken(ctx, tc)
	if err != nil {
		return nil, err
	}
	objs := []client.Object{
		BuildDragonflyService(tc),
		BuildDragonflyStatefulSet(tc, r.Config),
		BuildDragonflyNetworkPolicy(tc),
		BuildConcordService(tc),
	}
	if b == nil {
		objs = append(objs, BuildConcordDeployment(tc, r.Config, ref, TokenHash(token)))
	}
	for _, o := range objs {
		u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			return nil, err
		}
		if err := r.Apply(ctx, client.ApplyConfigurationFromUnstructured(&unstructured.Unstructured{Object: u}),
			client.FieldOwner(FieldManager), client.ForceOwnership); err != nil {
			return nil, fmt.Errorf("apply %T %s: %w", o, o.GetName(), err)
		}
	}
	return b, nil
}

// updateStatus patches /status only when it differs from what is stored.
func (r *Reconciler) updateStatus(ctx context.Context, tc *v1alpha1.TeamCoordinator, b *block) error {
	var dep appsv1.Deployment
	var sts appsv1.StatefulSet
	// Not found just means not created yet (zero status).
	if err := r.Get(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: concordName(tc)}, &dep); client.IgnoreNotFound(err) != nil {
		return err
	}
	if err := r.Get(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: dragonflyName(tc)}, &sts); client.IgnoreNotFound(err) != nil {
		return err
	}
	ready := b == nil && dep.Status.AvailableReplicas == tc.Spec.Replicas && sts.Status.ReadyReplicas == 1

	orig := tc.DeepCopy()
	tc.Status.ObservedGeneration = tc.Generation
	tc.Status.Endpoint = fmt.Sprintf("%s.%s.svc.cluster.local:%d", concordName(tc), tc.Namespace, rpcPort)
	tc.Status.ReadyReplicas = dep.Status.ReadyReplicas
	readyC := metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Progressing", ObservedGeneration: tc.Generation}
	progC := metav1.Condition{Type: "Progressing", Status: metav1.ConditionFalse, ObservedGeneration: tc.Generation}
	switch {
	case b != nil:
		readyC.Reason, readyC.Message = b.Reason, b.Message
		progC.Reason, progC.Message = b.Reason, b.Message
	case ready:
		readyC.Status, readyC.Reason = metav1.ConditionTrue, "Available"
		progC.Reason = "Available"
	default:
		progC.Status, progC.Reason = metav1.ConditionTrue, "Reconciling"
	}
	apimeta.SetStatusCondition(&tc.Status.Conditions, readyC)
	apimeta.SetStatusCondition(&tc.Status.Conditions, progC)
	if equality.Semantic.DeepEqual(orig.Status, tc.Status) {
		return nil
	}
	return r.Status().Patch(ctx, tc, client.MergeFrom(orig))
}

// resolveToken returns the token Secret reference and value. A nil block with
// a nil error means the token is usable.
func (r *Reconciler) resolveToken(ctx context.Context, tc *v1alpha1.TeamCoordinator) (TokenRef, []byte, *block, error) {
	if sr := tc.Spec.Auth.SecretRef; sr != nil {
		key := sr.Key
		if key == "" {
			key = TokenKey
		}
		var s corev1.Secret
		err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: sr.Name}, &s)
		switch {
		case apierrors.IsNotFound(err):
			return TokenRef{}, nil, &block{"SecretNotFound", fmt.Sprintf("secret %q not found", sr.Name)}, nil
		case err != nil:
			return TokenRef{}, nil, nil, err
		case len(s.Data[key]) == 0:
			return TokenRef{}, nil, &block{"SecretNotFound", fmt.Sprintf("secret %q has no key %q", sr.Name, key)}, nil
		}
		return TokenRef{Name: sr.Name, Key: key}, s.Data[key], nil, nil
	}
	value, b, err := r.ensureToken(ctx, tc)
	return TokenRef{Name: TokenName(tc), Key: TokenKey}, value, b, err
}

// ensureToken returns the generated token, creating the Secret only when absent
// (never overwriting an existing value). A cache miss is double-checked with an
// uncached read: a Secret without our managed-by label is the user's and is
// reported, never adopted; one with it is just cache lag.
func (r *Reconciler) ensureToken(ctx context.Context, tc *v1alpha1.TeamCoordinator) ([]byte, *block, error) {
	var s corev1.Secret
	nn := types.NamespacedName{Namespace: tc.Namespace, Name: TokenName(tc)}
	err := r.Get(ctx, nn, &s)
	if apierrors.IsNotFound(err) {
		err = r.APIReader.Get(ctx, nn, &s)
		if err == nil && s.Labels["app.kubernetes.io/managed-by"] != ManagedBy {
			return nil, &block{"TokenSecretUnmanaged", fmt.Sprintf(
				"secret %q exists but is not managed by concord-operator; delete it or set spec.auth.secretRef", s.Name)}, nil
		}
	}
	switch {
	case err == nil:
		if len(s.Data[TokenKey]) == 0 {
			return nil, nil, fmt.Errorf("secret %s has no %q key", s.Name, TokenKey)
		}
		return s.Data[TokenKey], nil, nil
	case !apierrors.IsNotFound(err):
		return nil, nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, nil, err
	}
	value := fmt.Appendf(nil, "%x", raw)
	if err := r.Create(ctx, BuildTokenSecret(tc, value)); err != nil {
		return nil, nil, err
	}
	return value, nil, nil
}

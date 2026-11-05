package operator

import (
	"context"
	"crypto/rand"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Kminhas21/concord/api/v1alpha1"
)

// +kubebuilder:rbac:groups=concord.kminhas21.github.io,resources=teamcoordinators,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=concord.kminhas21.github.io,resources=teamcoordinators/status,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services;secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconciler turns each TeamCoordinator into its coordination unit.
type Reconciler struct {
	client.Client
	Config Config
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

// Reconcile applies the full object set for one TeamCoordinator.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var tc v1alpha1.TeamCoordinator
	if err := r.Get(ctx, req.NamespacedName, &tc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !tc.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	token, err := r.ensureToken(ctx, &tc)
	if err != nil {
		return ctrl.Result{}, err
	}
	ref := TokenRef{Name: TokenName(&tc), Key: TokenKey}
	for _, o := range []client.Object{
		BuildDragonflyService(&tc),
		BuildDragonflyStatefulSet(&tc, r.Config),
		BuildDragonflyNetworkPolicy(&tc),
		BuildConcordService(&tc),
		BuildConcordDeployment(&tc, r.Config, ref, TokenHash(token)),
	} {
		u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Apply(ctx, client.ApplyConfigurationFromUnstructured(&unstructured.Unstructured{Object: u}),
			client.FieldOwner(FieldManager), client.ForceOwnership); err != nil {
			return ctrl.Result{}, fmt.Errorf("apply %T %s: %w", o, o.GetName(), err)
		}
	}
	return ctrl.Result{}, nil
}

// ensureToken returns the generated token, creating the Secret only when absent
// (never overwriting an existing value). A create race surfaces as an error and
// is retried with backoff once the cache has caught up.
func (r *Reconciler) ensureToken(ctx context.Context, tc *v1alpha1.TeamCoordinator) ([]byte, error) {
	var s corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: TokenName(tc)}, &s)
	switch {
	case err == nil:
		if len(s.Data[TokenKey]) == 0 {
			return nil, fmt.Errorf("secret %s has no %q key", s.Name, TokenKey)
		}
		return s.Data[TokenKey], nil
	case !apierrors.IsNotFound(err):
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	value := fmt.Appendf(nil, "%x", raw)
	if err := r.Create(ctx, BuildTokenSecret(tc, value)); err != nil {
		return nil, err
	}
	return value, nil
}

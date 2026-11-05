//go:build !windows

// controller-runtime v0.25 envtest does not compile on Windows (upstream
// signalProcess redeclared); the Linux gate runs these.

package operator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Kminhas21/concord/api/v1alpha1"
	"github.com/Kminhas21/concord/internal/operator/testenv"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newTC(name string) *v1alpha1.TeamCoordinator {
	return &v1alpha1.TeamCoordinator{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1alpha1.TeamCoordinatorSpec{Repo: "github.com/acme/app"},
	}
}

func TestTeamCoordinatorAPI(t *testing.T) {
	_, c := testenv.Start(t)
	ctx := context.Background()

	t.Run("minimal CR accepted with defaults", func(t *testing.T) {
		tc := newTC("minimal")
		if err := c.Create(ctx, tc); err != nil {
			t.Fatal(err)
		}
		var got v1alpha1.TeamCoordinator
		if err := c.Get(ctx, client.ObjectKeyFromObject(tc), &got); err != nil {
			t.Fatal(err)
		}
		s := got.Spec
		if s.Replicas != 1 || s.IntentTTL.Duration != 600*time.Second || s.Auth.Mode != "token" ||
			s.DataStore.Persistence || s.DataStore.Storage.String() != "1Gi" {
			t.Fatalf("defaults not filled: %+v", s)
		}
	})

	t.Run("replicas 0 rejected", func(t *testing.T) {
		// replicas is omitempty+default, so a typed 0 would be defaulted; send it raw.
		raw := map[string]any{"apiVersion": "concord.kminhas21.github.io/v1alpha1", "kind": "TeamCoordinator",
			"metadata": map[string]any{"name": "zero", "namespace": "default"},
			"spec":     map[string]any{"repo": "r", "replicas": 0}}
		if err := c.Create(ctx, unstructuredOf(raw)); !apierrors.IsInvalid(err) {
			t.Fatalf("replicas: 0: want Invalid, got %v", err)
		}
	})

	t.Run("auth.mode mtls rejected", func(t *testing.T) {
		tc := newTC("mtls")
		tc.Spec.Auth.Mode = "mtls"
		if err := c.Create(ctx, tc); !apierrors.IsInvalid(err) {
			t.Fatalf("auth.mode mtls: want Invalid, got %v", err)
		}
	})

	t.Run("persistence immutable, replicas mutable", func(t *testing.T) {
		tc := newTC("immut")
		if err := c.Create(ctx, tc); err != nil {
			t.Fatal(err)
		}
		tc.Spec.Replicas = 3
		if err := c.Update(ctx, tc); err != nil {
			t.Fatalf("replicas change rejected: %v", err)
		}
		tc.Spec.DataStore.Persistence = true
		err := c.Update(ctx, tc)
		if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "dataStore.persistence/storage are immutable") {
			t.Fatalf("want immutability rejection, got %v", err)
		}
	})
}

func unstructuredOf(m map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: m}
}

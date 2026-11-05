// Package v1alpha1 contains the TeamCoordinator API.
// +kubebuilder:object:generate=true
// +groupName=concord.kminhas21.github.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group/version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "concord.kminhas21.github.io", Version: "v1alpha1"}

	// SchemeBuilder registers the types with a runtime.Scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group/version to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &TeamCoordinator{}, &TeamCoordinatorList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TeamCoordinatorSpec declares one team's concord coordination unit.
type TeamCoordinatorSpec struct {
	// Repo is an informational label for the team's repository.
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`

	// Replicas is the number of concord pods.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`

	// IntentTTL is how long an intent record lives.
	// +kubebuilder:default="600s"
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s')",message="intentTTL must be a positive duration"
	IntentTTL *metav1.Duration `json:"intentTTL,omitempty"`

	// Image overrides the concord image (default: the operator's version).
	// +optional
	Image string `json:"image,omitempty"`

	// Resources for the concord container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// +kubebuilder:default={}
	DataStore DataStoreSpec `json:"dataStore,omitempty"`

	// +kubebuilder:default={}
	Auth AuthSpec `json:"auth,omitempty"`
}

// DataStoreSpec configures the Dragonfly backing store.
type DataStoreSpec struct {
	// Image overrides the Dragonfly image.
	// +optional
	Image string `json:"image,omitempty"`

	// +kubebuilder:default=false
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="dataStore.persistence/storage are immutable; delete and recreate the TeamCoordinator"
	Persistence bool `json:"persistence,omitempty"`

	// +kubebuilder:default="1Gi"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="dataStore.persistence/storage are immutable; delete and recreate the TeamCoordinator"
	Storage *resource.Quantity `json:"storage,omitempty"`
}

// AuthSpec configures client authentication.
type AuthSpec struct {
	// +kubebuilder:validation:Enum=token
	// +kubebuilder:default=token
	Mode string `json:"mode,omitempty"`

	// SecretRef names a Secret holding the team token; omitted means the
	// operator generates one.
	// +optional
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// SecretRef points at a key in a Secret in the same namespace.
type SecretRef struct {
	Name string `json:"name"`
	// +kubebuilder:default=token
	Key string `json:"key,omitempty"`
}

// TeamCoordinatorStatus is the observed state.
type TeamCoordinatorStatus struct {
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Endpoint           string             `json:"endpoint,omitempty"`
	ReadyReplicas      int32              `json:"readyReplicas,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=tc
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TeamCoordinator is one team's concord coordination unit.
type TeamCoordinator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TeamCoordinatorSpec   `json:"spec"`
	Status TeamCoordinatorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TeamCoordinatorList is a list of TeamCoordinators.
type TeamCoordinatorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TeamCoordinator `json:"items"`
}

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "storage.curvine.io", Version: "v1alpha1"}

var SchemeBuilder = runtime.NewSchemeBuilder(func(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &CurvineCluster{}, &CurvineClusterList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
})

var AddToScheme = SchemeBuilder.AddToScheme

// CurvineCluster describes intent. The manager observes status and can opt in
// to workload reconciliation through an explicit runtime contract.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cvcluster
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Observed",type=integer,JSONPath=".status.observedGeneration"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 30 && self.metadata.name.matches('^[a-z]([-a-z0-9]*[a-z0-9])?$')",message="name must be a lowercase DNS label starting with a letter, at most 30 characters"
type CurvineCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              Spec `json:"spec"`
	// +optional
	Status CurvineClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CurvineClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CurvineCluster `json:"items"`
}

type CurvineClusterStatus struct {
	// ObservedGeneration is processed intent, not proof of successful execution.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Observation contains only the current validated application sample.
	// It is cleared on unsupported, failed, stale or mismatched observations.
	// +optional
	Observation *ObservationStatus `json:"observation,omitempty"`
	// +optional
	LastSuccessfulObservationTime *metav1.Time `json:"lastSuccessfulObservationTime,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type ObservationStatus struct {
	SampleTime metav1.Time `json:"sampleTime"`
	// +kubebuilder:validation:MaxLength=128
	// Version is populated only when an adapter can verify the running version.
	// +optional
	Version string `json:"version,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	RegisteredWorkers *int32 `json:"registeredWorkers,omitempty"`
	// +optional
	Application *ApplicationObservation `json:"application,omitempty"`
}

// Intent isolates Kubernetes metadata and status from the existing S0 model.
// The returned spec does not share mutable slices with the resource.
func (c *CurvineCluster) Intent() Cluster {
	return Cluster{
		APIVersion: APIVersion, Kind: "CurvineCluster",
		Metadata: Metadata{Name: c.Name, Namespace: c.Namespace},
		Spec:     *c.Spec.DeepCopy(),
	}
}

// ApplicationObservation reports bounded release-specific evidence. Contract
// and SourceCommit describe the adapter, not the binaries currently running.
type ApplicationObservation struct {
	// +kubebuilder:validation:MaxLength=128
	Contract string `json:"contract"`
	// +kubebuilder:validation:MaxLength=40
	SourceCommit string `json:"sourceCommit"`
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason"`
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=8
	Capabilities []CapabilityStatus `json:"capabilities"`
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=5
	Masters []MasterObservation `json:"masters"`
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	WorkerPools []WorkerPoolObservation `json:"workerPools,omitempty"`
}
type CapabilityStatus struct {
	// +kubebuilder:validation:MaxLength=64
	Name      string `json:"name"`
	Supported bool   `json:"supported"`
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason"`
}
type MasterObservation struct {
	// +kubebuilder:validation:MaxLength=63
	Name      string `json:"name"`
	Reachable bool   `json:"reachable"`
	// +kubebuilder:validation:Enum=Unknown;Active;Standby;SafeMode;Exit
	Role string `json:"role"`
}
type WorkerPoolObservation struct {
	// +kubebuilder:validation:MaxLength=16
	Name string `json:"name"`
	// +kubebuilder:validation:Minimum=0
	Desired int32 `json:"desired"`
	// Registered counts matching Live endpoints, not desired replicas.
	// +kubebuilder:validation:Minimum=0
	Registered int32 `json:"registered"`
	// Fresh counts Live endpoints with a recent Master-recorded heartbeat.
	// +kubebuilder:validation:Minimum=0
	Fresh int32 `json:"fresh"`
	// VersionVerified counts structured reports matching the pinned source.
	// +kubebuilder:validation:Minimum=0
	VersionVerified int32 `json:"versionVerified"`
}

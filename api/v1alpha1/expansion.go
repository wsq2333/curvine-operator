package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// WorkerPoolProgress describes current allocation/registration, not data placement.
// Progress survives manager restarts through the status subresource.
type WorkerPoolProgress struct {
	// +kubebuilder:validation:MaxLength=16
	Name               string `json:"name"`
	ObservedGeneration int64  `json:"observedGeneration"`
	Desired            int32  `json:"desired"`
	Created            int32  `json:"created"`
	Scheduled          int32  `json:"scheduled"`
	Ready              int32  `json:"ready"`
	VolumesBound       int32  `json:"volumesBound"`
	// Nil means unavailable, never zero registered workers.
	// +optional
	Registered *int32 `json:"registered,omitempty"`
	// +kubebuilder:validation:Enum=Unknown;Blocked;WaitingForResources;WaitingForVolumes;WaitingForScheduling;WaitingForPods;WaitingForRegistration;Registered
	Phase string `json:"phase"`
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason"`
	// First affected resource; raw Kubernetes events/errors are never copied.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Resource         string      `json:"resource,omitempty"`
	LastProgressTime metav1.Time `json:"lastProgressTime"`
	Stalled          bool        `json:"stalled"`
}

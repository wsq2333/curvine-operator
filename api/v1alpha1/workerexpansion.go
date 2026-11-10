package v1alpha1

import (
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"regexp"
	"strings"
)

// CurvineWorkerExpansion adds one separately managed Worker to an existing,
// explicitly declared read-cache cluster. It never adopts the source workloads.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cvexpand
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 40 && self.metadata.name.matches('^[a-z]([-a-z0-9]*[a-z0-9])?$')",message="name must be a lowercase DNS label starting with a letter, at most 40 characters"
type CurvineWorkerExpansion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              WorkerExpansionSpec `json:"spec"`
	// +optional
	Status WorkerExpansionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CurvineWorkerExpansionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CurvineWorkerExpansion `json:"items"`
}

type PinnedObject struct {
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=128
	UID string `json:"uid"`
	// Hash of canonical JSON spec (workloads) or data/binaryData (ConfigMaps).
	// +kubebuilder:validation:Pattern="^[a-f0-9]{64}$"
	SHA256 string `json:"sha256"`
}

// +kubebuilder:validation:XValidation:rule="self.binding == oldSelf.binding",message="binding is immutable; migration requires a separate workflow"
// +kubebuilder:validation:XValidation:rule="self.replicas >= oldSelf.replicas",message="scale-in is not supported"
type WorkerExpansionSpec struct {
	Binding WorkerExpansionBinding `json:"binding"`
	// Total workers, including the existing single Worker. One is preflight only.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2
	Replicas int32 `json:"replicas"`
}
type WorkerExpansionBinding struct {
	// +kubebuilder:validation:Enum=ReadCache
	Purpose string `json:"purpose"`
	// +kubebuilder:validation:MaxLength=63
	ClusterID string       `json:"clusterID"`
	Master    PinnedObject `json:"master"`
	Worker    PinnedObject `json:"worker"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=name
	ConfigMaps []PinnedObject `json:"configMaps"`
	// +kubebuilder:validation:MaxLength=253
	TargetNode string `json:"targetNode"`
	// +kubebuilder:validation:MaxLength=128
	TargetNodeUID string `json:"targetNodeUID"`
	// Exact runtime image digest approved for this imported deployment, not a
	// claim that it was built from the upstream v0.5.4 release.
	// +kubebuilder:validation:MaxLength=327
	Image string `json:"image"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=set
	DataVolumes []string `json:"dataVolumes"`
}
type WorkerExpansionStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// +optional
	WorkerName string `json:"workerName,omitempty"`
	// +optional
	Registered int32 `json:"registered,omitempty"`
	// +optional
	LastProgressTime *metav1.Time `json:"lastProgressTime,omitempty"`
	// +optional
	SampleTime *metav1.Time `json:"sampleTime,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

func (s WorkerExpansionSpec) Validate() error {
	b := s.Binding
	name := regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	hash := regexp.MustCompile(`^[a-f0-9]{64}$`)
	validPin := func(p PinnedObject) bool {
		return name.MatchString(p.Name) && len(p.Name) <= 253 && p.UID != "" && len(p.UID) <= 128 && hash.MatchString(p.SHA256)
	}
	if s.Replicas < 1 || s.Replicas > 2 || b.Purpose != "ReadCache" || !dnsLabel.MatchString(b.ClusterID) || len(b.ClusterID) > 63 || !validPin(b.Master) || !validPin(b.Worker) || b.Master.Name == b.Worker.Name || !name.MatchString(b.TargetNode) || len(b.TargetNode) > 253 || b.TargetNodeUID == "" || len(b.TargetNodeUID) > 128 || !validPinnedImage(b.Image) {
		return fmt.Errorf("invalid expansion binding")
	}
	if len(b.ConfigMaps) < 1 || len(b.ConfigMaps) > 16 || len(b.DataVolumes) < 1 || len(b.DataVolumes) > 16 {
		return fmt.Errorf("invalid pinned configuration or data volumes")
	}
	seen := map[string]bool{}
	for _, p := range b.ConfigMaps {
		if !validPin(p) || seen[p.Name] {
			return fmt.Errorf("invalid or duplicate ConfigMap pin")
		}
		seen[p.Name] = true
	}
	seen = map[string]bool{}
	for _, n := range b.DataVolumes {
		if !dnsLabel.MatchString(n) || len(n) > 63 || seen[n] {
			return fmt.Errorf("invalid or duplicate data volume")
		}
		seen[n] = true
	}
	return nil
}

func validPinnedImage(image string) bool {
	repo, hash, ok := strings.Cut(image, "@sha256:")
	return ok && validRepository(repo) && digest.MatchString(hash)
}

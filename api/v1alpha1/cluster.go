package v1alpha1

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const APIVersion = "storage.curvine.io/v1alpha1"

type Cluster struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
}

type Metadata struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// +kubebuilder:validation:XValidation:rule="self.profile != 'production' || self.master.replicas >= 3",message="production requires 3 or 5 masters"
type Spec struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^[a-z]([-a-z0-9]*[a-z0-9])?$"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="clusterID is immutable"
	ClusterID string `json:"clusterID"`
	// +optional
	// +kubebuilder:default="cluster.local"
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?([.][a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)*$"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="clusterDomain is immutable"
	ClusterDomain string `json:"clusterDomain"`
	// +optional
	// +kubebuilder:default="development"
	// +kubebuilder:validation:MaxLength=11
	// +kubebuilder:validation:Enum=development;production
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="profile migration is not supported"
	Profile string `json:"profile"`
	// +kubebuilder:validation:MaxLength=327
	// +kubebuilder:validation:Pattern="^[a-z0-9][a-z0-9./:_-]*@sha256:[a-f0-9]{64}$"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="image upgrades require a dedicated workflow"
	Image string `json:"image"`
	// +optional
	// +kubebuilder:default="Retain"
	// +kubebuilder:validation:Enum=Retain
	DeletionPolicy string `json:"deletionPolicy"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="master membership and storage changes are not supported"
	Master MasterSpec `json:"master"`
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:XValidation:rule="oldSelf.all(p, self.exists(n, n.name == p.name && n.replicas >= p.replicas && n.storage == p.storage))",message="worker removal, scale-in and storage changes require a dedicated workflow"
	WorkerPools []WorkerPool `json:"workerPools"`
}

type MasterSpec struct {
	// +kubebuilder:validation:Enum=1;3;5
	Replicas int     `json:"replicas"`
	Storage  Storage `json:"storage"`
}

type Storage struct {
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?([.][a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)*$"
	StorageClassName string `json:"storageClassName"`
	// +kubebuilder:validation:Minimum=1
	SizeGiB int `json:"sizeGiB"`
}

type WorkerPool struct {
	// +kubebuilder:validation:MaxLength=16
	// +kubebuilder:validation:Pattern="^[a-z]([-a-z0-9]*[a-z0-9])?$"
	Name string `json:"name"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10000
	Replicas int     `json:"replicas"`
	Storage  Storage `json:"storage"`
}

var dnsLabel = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)
var dnsPart = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var repositoryPart = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)

// S0 supports lowercase repository names with an optional DNS registry and
// port, followed by a SHA-256 digest. Tags and IPv6 registry literals are not
// part of this deliberately narrow input contract.
func validRepository(s string) bool {
	if len(s) == 0 || len(s) > 255 {
		return false
	}
	parts := strings.Split(s, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		host, port, hasPort := strings.Cut(parts[0], ":")
		if !validDomain(host) {
			return false
		}
		if hasPort {
			if len(port) == 0 || len(port) > 5 {
				return false
			}
			for _, ch := range port {
				if ch < '0' || ch > '9' {
					return false
				}
			}
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return false
			}
		}
		parts = parts[1:]
	}
	for _, part := range parts {
		if !repositoryPart.MatchString(part) {
			return false
		}
	}
	return true
}

func validLabel(s string, max int) bool { return len(s) <= max && dnsLabel.MatchString(s) }

func validDomain(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, p := range strings.Split(s, ".") {
		if len(p) > 63 || !dnsPart.MatchString(p) {
			return false
		}
	}
	return true
}

func validateStorage(s Storage) error {
	if !validDomain(s.StorageClassName) || s.SizeGiB < 1 {
		return fmt.Errorf("storageClassName must be a DNS subdomain and sizeGiB must be positive")
	}
	return nil
}

// Validate checks the deliberately narrow S0 API, without contacting a cluster.
// An explicit storage class and image digest make intent reproducible; their
// existence and runtime suitability still need online preflight checks.
func (c Cluster) Validate() error {
	if c.APIVersion != APIVersion || c.Kind != "CurvineCluster" {
		return fmt.Errorf("expected %s CurvineCluster", APIVersion)
	}
	if !validLabel(c.Metadata.Name, 30) || !validLabel(c.Metadata.Namespace, 63) {
		return fmt.Errorf("metadata.name (max 30) and namespace (max 63) must be lowercase DNS labels beginning with a letter")
	}
	if !validLabel(c.Spec.ClusterID, 63) || !validDomain(c.Spec.ClusterDomain) {
		return fmt.Errorf("clusterID must be a DNS label; clusterDomain must be a DNS subdomain")
	}
	if c.Spec.Profile != "development" && c.Spec.Profile != "production" {
		return fmt.Errorf("profile must be development or production")
	}
	n := c.Spec.Master.Replicas
	if n != 1 && n != 3 && n != 5 {
		return fmt.Errorf("master.replicas must be 1, 3 or 5")
	}
	if c.Spec.Profile == "production" && n == 1 {
		return fmt.Errorf("production requires 3 or 5 masters")
	}
	repo, hash, ok := strings.Cut(c.Spec.Image, "@sha256:")
	if !ok || !validRepository(repo) || !digest.MatchString(hash) {
		return fmt.Errorf("image must use a lowercase repository (optional DNS registry and port, no tag) followed by @sha256:<64 lowercase hex characters>")
	}
	if c.Spec.DeletionPolicy != "Retain" {
		return fmt.Errorf("only deletionPolicy=Retain is supported")
	}
	if err := validateStorage(c.Spec.Master.Storage); err != nil {
		return fmt.Errorf("master: %w", err)
	}
	if len(c.Spec.WorkerPools) == 0 || len(c.Spec.WorkerPools) > 64 {
		return fmt.Errorf("between 1 and 64 worker pools are required")
	}
	seen := map[string]bool{}
	for _, pool := range c.Spec.WorkerPools {
		if !validLabel(pool.Name, 16) || seen[pool.Name] {
			return fmt.Errorf("worker pool %q must have a unique DNS label name of at most 16 characters", pool.Name)
		}
		seen[pool.Name] = true
		if pool.Replicas < 1 || pool.Replicas > 10000 {
			return fmt.Errorf("worker pool %s replicas must be between 1 and 10000", pool.Name)
		}
		if err := validateStorage(pool.Storage); err != nil {
			return fmt.Errorf("worker pool %s: %w", pool.Name, err)
		}
	}
	// Future master Pod hostnames use <cluster>-master-<ordinal>.<service>.<ns>.svc.<domain>.
	fqdn := fmt.Sprintf("%s-master-4.%s-master.%s.svc.%s", c.Metadata.Name, c.Metadata.Name, c.Metadata.Namespace, c.Spec.ClusterDomain)
	if len(fqdn) > 253 {
		return fmt.Errorf("generated master DNS name exceeds 253 characters")
	}
	return nil
}

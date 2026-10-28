// Package v1alpha1 defines the offline cluster-spec prototype.
// It is not yet a registered Kubernetes runtime.Object or an installed CRD.
package v1alpha1

import (
	"fmt"
	"regexp"
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

type Spec struct {
	ClusterID      string       `json:"clusterID"`
	ClusterDomain  string       `json:"clusterDomain"`
	Profile        string       `json:"profile"`
	Image          string       `json:"image"`
	DeletionPolicy string       `json:"deletionPolicy"`
	Master         MasterSpec   `json:"master"`
	WorkerPools    []WorkerPool `json:"workerPools"`
}

type MasterSpec struct {
	Replicas int     `json:"replicas"`
	Storage  Storage `json:"storage"`
}

type Storage struct {
	StorageClassName string `json:"storageClassName"`
	SizeGiB          int    `json:"sizeGiB"`
}

type WorkerPool struct {
	Name     string  `json:"name"`
	Replicas int     `json:"replicas"`
	Storage  Storage `json:"storage"`
}

var dnsLabel = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)
var dnsPart = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var repository = regexp.MustCompile(`^[a-z0-9][a-z0-9./:_-]*$`)

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
	if !ok || !repository.MatchString(repo) || !digest.MatchString(hash) {
		return fmt.Errorf("image must use a repository@sha256:<64 lowercase hex characters> reference")
	}
	if c.Spec.DeletionPolicy != "Retain" {
		return fmt.Errorf("only deletionPolicy=Retain is supported")
	}
	if err := validateStorage(c.Spec.Master.Storage); err != nil {
		return fmt.Errorf("master: %w", err)
	}
	if len(c.Spec.WorkerPools) == 0 {
		return fmt.Errorf("at least one worker pool is required")
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

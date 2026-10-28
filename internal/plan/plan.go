// Package plan implements pure intent comparison. It never mutates Kubernetes
// or treats an offline specification as evidence of observed runtime state.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
)

type Blocker struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type Step struct {
	ID        string   `json:"id"`
	Operation string   `json:"operation"`
	Target    string   `json:"target"`
	DependsOn []string `json:"dependsOn,omitempty"`
}

type Plan struct {
	APIVersion            string    `json:"apiVersion"`
	Stage                 string    `json:"stage"`
	Executable            bool      `json:"executable"`
	DesiredSpecHash       string    `json:"desiredSpecHash"`
	Blockers              []Blocker `json:"blockers"`
	Steps                 []Step    `json:"steps"`
	RuntimeChecksRequired []string  `json:"runtimeChecksRequired"`
}

func (p *Plan) block(reason, message string) {
	p.Blockers = append(p.Blockers, Blocker{reason, message})
}

func pools(c api.Cluster) []api.WorkerPool {
	out := append([]api.WorkerPool(nil), c.Spec.WorkerPools...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Build compares desired intent with optional previous intent. A previous spec
// is not an inventory snapshot, a last-applied guarantee, or a health report.
// Even an unblocked plan is advisory: S0 has no actuator or live observer.
func Build(desired api.Cluster, previous *api.Cluster) (Plan, error) {
	if err := desired.Validate(); err != nil {
		return Plan{}, fmt.Errorf("desired: %w", err)
	}
	if previous != nil {
		if err := previous.Validate(); err != nil {
			return Plan{}, fmt.Errorf("previous: %w", err)
		}
	}
	canonical := desired
	canonical.Spec.WorkerPools = pools(desired)
	data, err := json.Marshal(canonical)
	if err != nil {
		return Plan{}, err
	}
	hash := sha256.Sum256(data)
	p := Plan{
		APIVersion: "plan.curvine.io/v1alpha1", Stage: "S0-offline", Executable: false,
		DesiredSpecHash: hex.EncodeToString(hash[:]), Blockers: []Blocker{}, Steps: []Step{},
		RuntimeChecksRequired: []string{
			"Read current resource UIDs, generations, ownership and pending operations; recompute the plan before any write",
			"Verify image availability and Curvine version/feature compatibility",
			"Verify storage classes, failure domains, capacity and existing retained volumes",
			"Observe Raft quorum, worker registration and application-level health; Pod Ready is insufficient",
		},
	}
	if previous == nil {
		p.Steps = append(p.Steps,
			Step{ID: "identity", Operation: "reserve-cluster-identity-and-check-retained-volumes", Target: desired.Spec.ClusterID},
			Step{ID: "config", Operation: "render-versioned-config-and-headless-services", Target: desired.Metadata.Name, DependsOn: []string{"identity"}},
			Step{ID: "masters", Operation: "bootstrap-persistent-master-statefulset", Target: desired.Metadata.Name + "-master", DependsOn: []string{"config"}},
			Step{ID: "quorum", Operation: "wait-for-application-raft-quorum", Target: desired.Metadata.Name + "-master", DependsOn: []string{"masters"}},
		)
		for _, pool := range pools(desired) {
			p.Steps = append(p.Steps, Step{ID: "workers-" + pool.Name, Operation: "create-persistent-worker-pool", Target: pool.Name, DependsOn: []string{"quorum"}})
		}
		return p, nil
	}
	old := previous.Spec
	next := desired.Spec
	if previous.Metadata != desired.Metadata || old.ClusterID != next.ClusterID || old.ClusterDomain != next.ClusterDomain {
		p.block("IdentityImmutable", "name, namespace, clusterID and clusterDomain require an explicit migration")
	}
	if old.Profile != next.Profile {
		p.block("ProfileMigrationRequired", "development/production migration needs topology and data checks")
	}
	if old.Master.Replicas != next.Master.Replicas {
		p.block("RaftMembershipChangeRequired", "master replicas cannot be changed by scaling a StatefulSet")
	}
	if old.Master.Storage != next.Master.Storage {
		p.block("StorageWorkflowRequired", "master storage changes require a separate resize or migration workflow")
	}
	if old.Image != next.Image {
		p.block("UpgradeWorkflowRequired", "image changes require compatibility, quorum, drain and rollout checks")
	}
	oldPools := map[string]api.WorkerPool{}
	newPools := map[string]api.WorkerPool{}
	for _, pool := range old.WorkerPools {
		oldPools[pool.Name] = pool
	}
	for _, pool := range next.WorkerPools {
		newPools[pool.Name] = pool
	}
	for _, before := range pools(*previous) {
		after, exists := newPools[before.Name]
		if !exists || after.Replicas < before.Replicas {
			p.block("WorkerDrainRequired", "pool "+before.Name+": removing workers requires proven data evacuation and write safety")
		}
		if exists && before.Storage != after.Storage {
			p.block("StorageWorkflowRequired", "pool "+before.Name+": storage changes require a separate workflow")
		}
	}
	// Do not emit partially executable steps for a mixed safe/unsafe change.
	if len(p.Blockers) > 0 {
		return p, nil
	}
	for _, pool := range pools(desired) {
		before, exists := oldPools[pool.Name]
		if !exists {
			p.Steps = append(p.Steps, Step{ID: "workers-" + pool.Name, Operation: "create-persistent-worker-pool", Target: pool.Name})
		} else if pool.Replicas > before.Replicas {
			p.Steps = append(p.Steps, Step{ID: "workers-" + pool.Name, Operation: "scale-out-worker-pool", Target: pool.Name})
		}
	}
	return p, nil
}

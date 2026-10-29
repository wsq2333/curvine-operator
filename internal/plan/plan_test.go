package plan

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
)

func fixture(t *testing.T) api.Cluster {
	t.Helper()
	data, err := os.ReadFile("../../examples/cluster-production.json")
	if err != nil {
		t.Fatal(err)
	}
	var c api.Cluster
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBootstrapHasQuorumDependencyAndIsNeverExecutable(t *testing.T) {
	p, err := Build(fixture(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Executable || len(p.Blockers) != 0 || len(p.RuntimeChecksRequired) == 0 {
		t.Fatalf("invalid safety contract: %+v", p)
	}
	if p.Mode != "bootstrap" || p.PreviousSpecHash != "" || p.Cluster != fixture(t).Metadata || p.ClusterID != fixture(t).Spec.ClusterID {
		t.Fatalf("missing bootstrap identity: %+v", p)
	}
	seen := map[string]bool{}
	for _, step := range p.Steps {
		for _, dep := range step.DependsOn {
			if !seen[dep] {
				t.Fatalf("unresolved dependency %s", dep)
			}
		}
		seen[step.ID] = true
		if step.Replicas != nil && (step.Replicas.From != 0 || step.Replicas.To != 3) {
			t.Fatalf("incorrect bootstrap replicas: %+v", step.Replicas)
		}
		if step.Operation == "create-persistent-worker-pool" && !reflect.DeepEqual(step.DependsOn, []string{"quorum"}) {
			t.Fatal("workers must depend on observed quorum")
		}
	}
}

func TestUnsafeTransitionsBlockWholePlan(t *testing.T) {
	cases := []struct {
		name, reason string
		edit         func(*api.Cluster)
	}{
		{"master-scale", "RaftMembershipChangeRequired", func(c *api.Cluster) { c.Spec.Master.Replicas = 5 }},
		{"worker-scale-in", "WorkerDrainRequired", func(c *api.Cluster) { c.Spec.WorkerPools[0].Replicas-- }},
		{"remove-pool", "WorkerDrainRequired", func(c *api.Cluster) { c.Spec.WorkerPools[0].Name = "replacement" }},
		{"master-disk", "StorageWorkflowRequired", func(c *api.Cluster) { c.Spec.Master.Storage.SizeGiB++ }},
		{"worker-disk", "StorageWorkflowRequired", func(c *api.Cluster) { c.Spec.WorkerPools[0].Storage.StorageClassName = "other" }},
		{"image", "UpgradeWorkflowRequired", func(c *api.Cluster) {
			c.Spec.Image = "registry.example.com/curvine-other@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{"identity", "IdentityImmutable", func(c *api.Cluster) { c.Spec.ClusterID = "other" }},
		{"domain", "IdentityImmutable", func(c *api.Cluster) { c.Spec.ClusterDomain = "other.local" }},
		{"profile", "ProfileMigrationRequired", func(c *api.Cluster) { c.Spec.Profile = "development" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old, next := fixture(t), fixture(t)
			tc.edit(&next)
			// A simultaneous safe addition must not leak steps out of a blocked plan.
			next.Spec.WorkerPools = append(next.Spec.WorkerPools, api.WorkerPool{Name: "new", Replicas: 1, Storage: old.Spec.WorkerPools[0].Storage})
			p, err := Build(next, &old)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Steps) != 0 || p.Executable {
				t.Fatalf("unsafe partial plan: %+v", p)
			}
			found := false
			for _, blocker := range p.Blockers {
				if blocker.Reason == tc.reason {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s: %+v", tc.reason, p)
			}
		})
	}
}

func TestScaleOutAndNoop(t *testing.T) {
	old, next := fixture(t), fixture(t)
	p, err := Build(next, &old)
	if err != nil || len(p.Steps) != 0 || len(p.Blockers) != 0 {
		t.Fatalf("no-op: %+v %v", p, err)
	}
	if p.Mode != "update" || p.PreviousSpecHash != p.DesiredSpecHash {
		t.Fatalf("invalid no-op identity: %+v", p)
	}
	next.Spec.WorkerPools[0].Replicas++
	p, err = Build(next, &old)
	if err != nil || len(p.Steps) != 1 || p.Steps[0].Operation != "scale-out-worker-pool" || p.Executable {
		t.Fatalf("scale out: %+v %v", p, err)
	}
	if p.PreviousSpecHash == p.DesiredSpecHash || p.Steps[0].Replicas == nil || *p.Steps[0].Replicas != (ReplicaChange{From: 3, To: 4}) {
		t.Fatalf("missing replica diff: %+v", p)
	}
}

func TestNewPoolAndInvalidPrevious(t *testing.T) {
	old, next := fixture(t), fixture(t)
	next.Spec.WorkerPools = append(next.Spec.WorkerPools, api.WorkerPool{Name: "new", Replicas: 2, Storage: old.Spec.WorkerPools[0].Storage})
	p, err := Build(next, &old)
	if err != nil || len(p.Steps) != 1 {
		t.Fatalf("new pool: %+v %v", p, err)
	}
	if p.Steps[0].Target != "new" || p.Steps[0].Replicas == nil || *p.Steps[0].Replicas != (ReplicaChange{From: 0, To: 2}) {
		t.Fatalf("incorrect new pool plan: %+v", p)
	}
	old.Spec.Master.Replicas = 2
	if _, err := Build(next, &old); err == nil {
		t.Fatal("accepted invalid previous intent")
	}
}

func TestPoolOrderDoesNotChangePlanOrMutateInput(t *testing.T) {
	c := fixture(t)
	c.Spec.WorkerPools = append(c.Spec.WorkerPools, api.WorkerPool{Name: "alpha", Replicas: 1, Storage: c.Spec.WorkerPools[0].Storage})
	firstName := c.Spec.WorkerPools[0].Name
	a, err := Build(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Spec.WorkerPools[0].Name != firstName {
		t.Fatal("Build mutated caller slice")
	}
	c.Spec.WorkerPools[0], c.Spec.WorkerPools[1] = c.Spec.WorkerPools[1], c.Spec.WorkerPools[0]
	b, err := Build(c, nil)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("nondeterministic plan: %v", err)
	}
}

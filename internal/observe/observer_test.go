package observe

import (
	"context"
	"errors"
	"testing"
	"time"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
)

func TestSnapshotEvidenceContract(t *testing.T) {
	now := time.Now().UTC()
	target := Target{UID: "incarnation-1", Generation: 2}
	valid := Snapshot{ClusterUID: target.UID, Generation: target.Generation, ObservedAt: now, Version: "test-fixture", Quorum: Healthy, Workers: Unknown, DataAvailability: Unknown}
	if err := valid.Validate(target, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Snapshot){
		"wrong-uid":       func(s *Snapshot) { s.ClusterUID = "other" },
		"old-generation":  func(s *Snapshot) { s.Generation-- },
		"stale":           func(s *Snapshot) { s.ObservedAt = now.Add(-2 * time.Minute) },
		"future":          func(s *Snapshot) { s.ObservedAt = now.Add(time.Minute) },
		"no-time":         func(s *Snapshot) { s.ObservedAt = time.Time{} },
		"no-version":      func(s *Snapshot) { s.Version = "" },
		"implicit-health": func(s *Snapshot) { s.Quorum = "" },
		"negative-count":  func(s *Snapshot) { n := int32(-1); s.RegisteredWorkers = &n },
	} {
		t.Run(name, func(t *testing.T) {
			s := valid
			change(&s)
			if s.Validate(target, now, time.Minute) == nil {
				t.Fatal("accepted invalid evidence")
			}
		})
	}
}

func TestDefaultAndTargetIsolation(t *testing.T) {
	if _, err := (Unsupported{}).Observe(context.Background(), Target{}); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	c := &api.CurvineCluster{Spec: api.Spec{WorkerPools: []api.WorkerPool{{Name: "original"}}}}
	target := TargetFor(c)
	target.Spec.WorkerPools[0].Name = "changed"
	if c.Spec.WorkerPools[0].Name != "original" {
		t.Fatal("target aliases resource")
	}
}

package observe

import (
	"context"
	"errors"
	"strings"
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

func TestPartialEvidenceCannotEscalateUnsupportedCapabilities(t *testing.T) {
	now := time.Now()
	target := Target{UID: "test", Generation: 1}
	s := Snapshot{ClusterUID: target.UID, Generation: 1, ObservedAt: now, Quorum: Unknown, Workers: Unknown, DataAvailability: Unknown, Application: &api.ApplicationObservation{Contract: "test-contract", SourceCommit: strings.Repeat("a", 40), Reason: "PartialEvidence", Masters: []api.MasterObservation{{Name: "master-0", Role: "Active", Reachable: true}}, Capabilities: []api.CapabilityStatus{{Name: "Quorum", Reason: "Unavailable"}, {Name: "DataAvailability", Reason: "Unavailable"}, {Name: "MasterVersion", Reason: "Unavailable"}}}}
	if err := s.Validate(target, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*Snapshot){
		"quorum":  func(s *Snapshot) { s.Quorum = Healthy },
		"data":    func(s *Snapshot) { s.DataAvailability = Healthy },
		"version": func(s *Snapshot) { s.Version = "guessed-tag" },
		"duplicate": func(s *Snapshot) {
			s.Application.Capabilities = append(s.Application.Capabilities, s.Application.Capabilities[0])
		},
		"source":           func(s *Snapshot) { s.Application.SourceCommit = strings.Repeat("z", 40) },
		"unreachable-role": func(s *Snapshot) { s.Application.Masters[0].Reachable = false },
		"pool-count": func(s *Snapshot) {
			s.Application.WorkerPools = []api.WorkerPoolObservation{{Name: "pool", Registered: 1, Fresh: 2}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			copy := s
			copy.Application = s.Application.DeepCopy()
			edit(&copy)
			if copy.Validate(target, now, time.Minute) == nil {
				t.Fatal("accepted contradictory evidence")
			}
		})
	}
}

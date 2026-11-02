// Package observe defines the read-only contract between the controller and
// version-specific Curvine adapters. It does not implement a Curvine protocol.
package observe

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
)

var ErrUnsupported = errors.New("Curvine observation capability is not configured")

type Health string

const (
	Unknown   Health = "Unknown"
	Healthy   Health = "Healthy"
	Unhealthy Health = "Unhealthy"
)

// Target binds an observation to a resource incarnation and generation.
// Adapters must not mutate it, derive health from desired replicas, or call
// mutating endpoints. They must honor cancellation and deadlines.
type Target struct {
	UID        string
	Generation int64
	Namespace  string
	Name       string
	Spec       api.Spec
}

func TargetFor(c *api.CurvineCluster) Target {
	return Target{UID: string(c.UID), Generation: c.Generation, Namespace: c.Namespace, Name: c.Name, Spec: *c.Spec.DeepCopy()}
}

// Snapshot reports evidence, including explicit unknowns. RegisteredWorkers is
// nil when unavailable; an absent count must never silently become zero.
type Snapshot struct {
	ClusterUID        string
	Generation        int64
	ObservedAt        time.Time
	Version           string
	Quorum            Health
	Workers           Health
	DataAvailability  Health
	Application       *api.ApplicationObservation
	RegisteredWorkers *int32
}

type Observer interface {
	Observe(context.Context, Target) (Snapshot, error)
}

// Unsupported is the explicit default when no application contract is selected.
type Unsupported struct{}

func (Unsupported) Observe(context.Context, Target) (Snapshot, error) {
	return Snapshot{}, ErrUnsupported
}

func (s Snapshot) Validate(target Target, now time.Time, maxAge time.Duration) error {
	if target.UID == "" || s.ClusterUID != target.UID || s.Generation != target.Generation {
		return fmt.Errorf("observation does not match resource identity and generation")
	}
	if s.ObservedAt.IsZero() || now.Sub(s.ObservedAt) > maxAge || s.ObservedAt.After(now.Add(5*time.Second)) {
		return fmt.Errorf("observation timestamp is missing, stale or in the future")
	}
	if (strings.TrimSpace(s.Version) == "" && (s.Application == nil || s.Application.Contract == "")) || len(s.Version) > 128 {
		return fmt.Errorf("observation must identify a reported version or an explicit adapter contract")
	}
	for _, h := range []Health{s.Quorum, s.Workers, s.DataAvailability} {
		if h != Unknown && h != Healthy && h != Unhealthy {
			return fmt.Errorf("observation must specify explicit health states")
		}
	}
	if s.RegisteredWorkers != nil && *s.RegisteredWorkers < 0 {
		return fmt.Errorf("negative worker count")
	}
	if s.Application != nil {
		a := s.Application
		if strings.TrimSpace(a.Contract) == "" || len(a.Contract) > 128 || len(a.SourceCommit) != 40 || a.Reason == "" || len(a.Reason) > 64 || len(a.Masters) == 0 || len(a.Masters) > 5 || len(a.WorkerPools) > 64 || len(a.Capabilities) == 0 || len(a.Capabilities) > 8 {
			return fmt.Errorf("invalid application evidence bounds")
		}
		if _, err := hex.DecodeString(a.SourceCommit); err != nil {
			return fmt.Errorf("invalid source commit")
		}
		names := map[string]bool{}
		for _, m := range a.Masters {
			if m.Name == "" || len(m.Name) > 63 || names[m.Name] {
				return fmt.Errorf("invalid master evidence")
			}
			names[m.Name] = true
			switch m.Role {
			case "Unknown", "Active", "Standby", "SafeMode", "Exit":
			default:
				return fmt.Errorf("invalid master role")
			}
			if !m.Reachable && m.Role != "Unknown" {
				return fmt.Errorf("unreachable master cannot report a role")
			}
		}
		names = map[string]bool{}
		for _, cap := range a.Capabilities {
			if cap.Name == "" || len(cap.Name) > 64 || names[cap.Name] || cap.Reason == "" || len(cap.Reason) > 64 {
				return fmt.Errorf("invalid capability evidence")
			}
			names[cap.Name] = true
			if !cap.Supported && ((cap.Name == "Quorum" && s.Quorum == Healthy) || (cap.Name == "DataAvailability" && s.DataAvailability == Healthy) || (cap.Name == "MasterVersion" && s.Version != "")) {
				return fmt.Errorf("unsupported capability cannot be reported as verified")
			}
		}
		seen := map[string]bool{}
		for _, p := range a.WorkerPools {
			if p.Name == "" || len(p.Name) > 16 || seen[p.Name] || p.Desired < 0 || p.Registered < 0 || p.Fresh < 0 || p.VersionVerified < 0 || p.Fresh > p.Registered || p.VersionVerified > p.Registered {
				return fmt.Errorf("invalid worker pool evidence")
			}
			seen[p.Name] = true
		}
	}
	return nil
}

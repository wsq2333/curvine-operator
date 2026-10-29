// Package observe defines the read-only contract between the controller and
// version-specific Curvine adapters. It does not implement a Curvine protocol.
package observe

import (
	"context"
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
	RegisteredWorkers *int32
}

type Observer interface {
	Observe(context.Context, Target) (Snapshot, error)
}

// Unsupported is the manager's explicit default until a real version adapter
// and its application-level contract tests are available.
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
	if strings.TrimSpace(s.Version) == "" || len(s.Version) > 128 {
		return fmt.Errorf("observation must identify the application version")
	}
	for _, h := range []Health{s.Quorum, s.Workers, s.DataAvailability} {
		if h != Unknown && h != Healthy && h != Unhealthy {
			return fmt.Errorf("observation must specify explicit health states")
		}
	}
	if s.RegisteredWorkers != nil && *s.RegisteredWorkers < 0 {
		return fmt.Errorf("negative worker count")
	}
	return nil
}

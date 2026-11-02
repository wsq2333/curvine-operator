package v054

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/observe"
)

// HeartbeatMaxAge is deliberately shorter than Curvine's lost-worker timeout.
// A recent master-recorded heartbeat is registration evidence, not an I/O probe.
const HeartbeatMaxAge = 30 * time.Second

type Observer struct {
	http *http.Client
	now  func() time.Time
}

// New selects only the audited, read-only HTTP routes. A transport can be
// supplied for tests; nil uses a bounded direct client with no ambient proxy.
func New(transport http.RoundTripper) *Observer {
	return &Observer{http: newClient(transport), now: time.Now}
}

func (o *Observer) Observe(ctx context.Context, t observe.Target) (observe.Snapshot, error) {
	intent := api.Cluster{APIVersion: api.APIVersion, Kind: "CurvineCluster", Metadata: api.Metadata{Name: t.Name, Namespace: t.Namespace}, Spec: t.Spec}
	if t.UID == "" || t.Generation < 1 || intent.Validate() != nil {
		return observe.Snapshot{}, ErrInvalidResponse
	}
	start := o.now()
	a := &api.ApplicationObservation{Contract: Contract, SourceCommit: SourceCommit, Capabilities: capabilities(), Reason: "PartialApplicationEvidence"}
	s := observe.Snapshot{ClusterUID: t.UID, Generation: t.Generation, ObservedAt: start, Quorum: observe.Unknown, Workers: observe.Unknown, DataAvailability: observe.Unknown, Application: a}
	masters, active, reason, err := o.masters(ctx, t)
	if err != nil {
		return observe.Snapshot{}, err
	}
	a.Masters = masters
	if reason != "" {
		a.Reason = reason
		return s, nil
	}
	host := MasterHost(t.Name, t.Namespace, t.Spec.ClusterDomain, active)
	var inv inventory
	if err = o.get(ctx, host, WorkersPath, &inv); err != nil {
		return observe.Snapshot{}, err
	}
	// Bracket inventory with role reads. This rejects a visible leadership
	// transition but is not an atomic snapshot, lease or quorum certificate.
	after, next, why, err := o.masters(ctx, t)
	if err != nil {
		return observe.Snapshot{}, err
	}
	a.Masters = after
	if why != "" || next != active {
		a.Reason = "MasterRoleChanged"
		return s, nil
	}
	if err = o.workers(t, inv, o.now(), &s); err != nil {
		return observe.Snapshot{}, err
	}
	if ctx.Err() != nil {
		return observe.Snapshot{}, ctx.Err()
	}
	return s, nil
}

func (o *Observer) masters(ctx context.Context, t observe.Target) ([]api.MasterObservation, int, string, error) {
	n := t.Spec.Master.Replicas
	views := make([]api.MasterObservation, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			views[i] = api.MasterObservation{Name: fmt.Sprintf("%s-master-%d", t.Name, i), Role: "Unknown"}
			host := MasterHost(t.Name, t.Namespace, t.Spec.ClusterDomain, i)
			var v overview
			if errs[i] = o.get(ctx, host, OverviewPath, &v); errs[i] != nil {
				return
			}
			if v.ClusterID == nil || v.MasterAddress == nil || v.State == nil || v.LiveWorkers == nil || v.LostWorkers == nil || *v.LiveWorkers < 0 || *v.LostWorkers < 0 {
				errs[i] = ErrInvalidResponse
				return
			}
			if *v.ClusterID != t.Spec.ClusterID || *v.MasterAddress != fmt.Sprintf("%s:%d", host, MasterRPCPort) {
				errs[i] = ErrIdentityMismatch
				return
			}
			switch *v.State {
			case "Active", "Standby", "SafeMode", "Exit":
			default:
				errs[i] = ErrInvalidResponse
				return
			}
			views[i].Reachable = true
			views[i].Role = *v.State
		}(i)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, -1, "", ctx.Err()
	}
	reachable, active, count := 0, -1, 0
	for i, err := range errs {
		if err != nil {
			if !errors.Is(err, ErrUnavailable) {
				return nil, -1, "", err
			}
			continue
		}
		reachable++
		if views[i].Role == "Active" {
			active = i
			count++
		}
	}
	if reachable == 0 {
		return nil, -1, "", ErrUnavailable
	}
	if count > 1 {
		return views, -1, "ConflictingActiveMasters", nil
	}
	if reachable != n {
		return views, -1, "IncompleteMasterObservation", nil
	}
	if count == 0 {
		return views, -1, "NoActiveMaster", nil
	}
	return views, active, "", nil
}

func (o *Observer) workers(t observe.Target, inv inventory, now time.Time, s *observe.Snapshot) error {
	if inv.Live == nil || inv.Lost == nil {
		return ErrInvalidResponse
	}
	pools := append([]api.WorkerPool(nil), t.Spec.WorkerPools...)
	sort.Slice(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })
	for _, p := range pools {
		s.Application.WorkerPools = append(s.Application.WorkerPools, api.WorkerPoolObservation{Name: p.Name, Desired: int32(p.Replicas)})
	}
	ids := map[uint32]bool{}
	hosts := map[string]bool{}
	sessions := map[string]bool{}
	unhealthy, unknown := false, false
	reason := "WorkerRegistrationVerified"
	records := append(append([]worker(nil), (*inv.Live)...), (*inv.Lost)...)
	for i, w := range records {
		if w.Address == nil || w.Address.ID == nil || w.Address.Host == nil || w.Address.RPCPort == nil || w.Status == nil || w.LastUpdate == nil || w.Capacity == nil || w.Available == nil || w.Storage == nil {
			return ErrInvalidResponse
		}
		id, host := *w.Address.ID, *w.Address.Host
		if host == "" || len(host) > 253 || ids[id] || hosts[host] || *w.Capacity < 0 || *w.Available < 0 || *w.Available > *w.Capacity || *w.LastUpdate < 0 || *w.LastUpdate > now.Add(5*time.Second).UnixMilli() {
			return ErrInvalidResponse
		}
		ids[id] = true
		hosts[host] = true
		switch *w.Status {
		case "Live", "Unknown", "Blacklist", "Decommission", "Lost":
		default:
			return ErrInvalidResponse
		}
		if i >= len(*inv.Live) && *w.Status != "Lost" {
			return ErrInvalidResponse
		}
		pool := -1
		for j, p := range pools {
			prefix := t.Name + "-worker-" + p.Name + "-"
			suffix := "." + t.Name + "-worker-" + p.Name + "." + t.Namespace + ".svc." + t.Spec.ClusterDomain
			if !strings.HasPrefix(host, prefix) || !strings.HasSuffix(host, suffix) {
				continue
			}
			ordinal := strings.TrimSuffix(strings.TrimPrefix(host, prefix), suffix)
			n, err := strconv.Atoi(ordinal)
			if err == nil && n >= 0 && n < p.Replicas && strconv.Itoa(n) == ordinal {
				pool = j
			}
		}
		if pool < 0 || *w.Address.RPCPort != WorkerRPCPort {
			unhealthy = true
			reason = "UnexpectedWorker"
			continue
		}
		if *w.Status != "Live" {
			unhealthy = true
			reason = "WorkerNotLive"
			continue
		}
		p := &s.Application.WorkerPools[pool]
		p.Registered++
		if *w.LastUpdate > 0 && now.Sub(time.UnixMilli(*w.LastUpdate)) <= HeartbeatMaxAge {
			p.Fresh++
		} else {
			unhealthy = true
			reason = "WorkerHeartbeatStale"
		}
		if w.Session == nil || *w.Session == "" {
			unknown = true
			reason = "WorkerSessionUnavailable"
		} else {
			if len(*w.Session) > 256 || sessions[*w.Session] {
				return ErrInvalidResponse
			}
			sessions[*w.Session] = true
		}
		if w.Version == nil {
			unknown = true
			reason = "WorkerVersionUnavailable"
		} else if matchingBuild(w.Version.GitCommit, w.Version.GitTag) && w.Version.ProtocolVersion == 1 && w.Version.MinProtocolVersion == 1 {
			p.VersionVerified++
		} else {
			unhealthy = true
			reason = "WorkerVersionMismatch"
		}
		if len(w.Storage) == 0 || *w.Capacity == 0 {
			unhealthy = true
			reason = "WorkerStorageUnavailable"
		}
		for _, disk := range w.Storage {
			if disk.Failed == nil {
				return ErrInvalidResponse
			}
			if *disk.Failed {
				unhealthy = true
				reason = "WorkerStorageFailed"
			}
		}
	}
	total := int32(0)
	for _, p := range s.Application.WorkerPools {
		total += p.Registered
		if p.Registered != p.Desired {
			unhealthy = true
			reason = "WorkerRegistrationIncomplete"
		}
	}
	s.RegisteredWorkers = &total
	s.Workers = observe.Healthy
	if unknown {
		s.Workers = observe.Unknown
	}
	if unhealthy {
		s.Workers = observe.Unhealthy
	}
	s.Application.Reason = reason
	return nil
}

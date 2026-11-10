package v054

import (
	"context"
	"net"
	"time"
)

// ImportedReport does not assert release provenance, quorum or data readiness.
// Runtime image identity is checked separately against Kubernetes imageID.
type ImportedReport struct {
	Registered                    int32
	ExistingHealthy, AddedHealthy bool
}

// ObserveImported is the narrow wire contract for a single existing read-cache
// Master and at most two host-network Workers. URLs come only from pinned K8s
// objects, never from HTTP responses. Unknown build commits remain unknown.
func (o *Observer) ObserveImported(ctx context.Context, masterIP, masterAddress, clusterID, existingIP, targetIP string, now time.Time) (ImportedReport, error) {
	out := ImportedReport{}
	if net.ParseIP(masterIP) == nil || net.ParseIP(existingIP) == nil || net.ParseIP(targetIP) == nil || existingIP == targetIP {
		return out, ErrIdentityMismatch
	}
	check := func() error {
		var v overview
		if err := o.get(ctx, masterIP, OverviewPath, &v); err != nil {
			return err
		}
		if v.ClusterID == nil || *v.ClusterID != clusterID || v.MasterAddress == nil || *v.MasterAddress != masterAddress || v.State == nil || *v.State != "Active" {
			return ErrIdentityMismatch
		}
		return nil
	}
	if err := check(); err != nil {
		return out, err
	}
	var inv inventory
	if err := o.get(ctx, masterIP, WorkersPath, &inv); err != nil {
		return out, err
	}
	if inv.Live == nil || inv.Lost == nil || len(*inv.Lost) > 0 || len(*inv.Live) > 2 {
		return out, ErrInvalidResponse
	}
	ids := map[uint32]bool{}
	hosts := map[string]bool{}
	sessions := map[string]bool{}
	for _, w := range *inv.Live {
		if w.Address == nil || w.Address.ID == nil || w.Address.Host == nil || w.Address.RPCPort == nil || *w.Address.RPCPort != 8997 || w.Status == nil || *w.Status != "Live" || w.LastUpdate == nil || *w.LastUpdate <= 0 || now.Sub(time.UnixMilli(*w.LastUpdate)) > HeartbeatMaxAge || time.UnixMilli(*w.LastUpdate).After(now.Add(5*time.Second)) || w.Session == nil || *w.Session == "" || len(*w.Session) > 256 || w.Capacity == nil || *w.Capacity <= 0 || w.Available == nil || *w.Available < 0 || *w.Available > *w.Capacity || len(w.Storage) == 0 || w.Version == nil || w.Version.ProtocolVersion != 1 || w.Version.MinProtocolVersion != 1 {
			return out, ErrInvalidResponse
		}
		host := *w.Address.Host
		if (host != existingIP && host != targetIP) || ids[*w.Address.ID] || hosts[host] || sessions[*w.Session] {
			return out, ErrIdentityMismatch
		}
		for _, d := range w.Storage {
			if d.Failed == nil || *d.Failed {
				return out, ErrInvalidResponse
			}
		}
		ids[*w.Address.ID] = true
		hosts[host] = true
		sessions[*w.Session] = true
		out.Registered++
		if host == existingIP {
			out.ExistingHealthy = true
		} else {
			out.AddedHealthy = true
		}
	}
	if err := check(); err != nil {
		return ImportedReport{}, err
	}
	return out, nil
}

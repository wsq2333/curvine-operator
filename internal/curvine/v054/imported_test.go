package v054

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type importedTransport func(*http.Request) (*http.Response, error)

func (f importedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestImportedRuntimeEvidence(t *testing.T) {
	now := time.Now()
	host := "master-0.master.ns.svc.cluster.local:8995"
	record := func(ip, session string, id uint32) map[string]any {
		return map[string]any{"address": map[string]any{"worker_id": id, "hostname": ip, "rpc_port": 8997}, "status": "Live", "last_update": now.UnixMilli(), "worker_session_id": session, "capacity": 100, "available": 80, "storage_map": map[string]any{"disk": map[string]any{"failed": false}}, "component_info": map[string]any{"git_commit": "unknown", "protocol_version": 1, "min_protocol_version": 1}}
	}
	for _, tt := range []struct {
		name   string
		change func([]map[string]any)
		fail   bool
	}{
		{"digest-protocol-mode-unknown-source", func([]map[string]any) {}, false},
		{"stale", func(w []map[string]any) { w[0]["last_update"] = now.Add(-time.Minute).UnixMilli() }, true},
		{"foreign-worker", func(w []map[string]any) { w[1]["address"].(map[string]any)["hostname"] = "192.0.2.99" }, true},
		{"duplicate-session", func(w []map[string]any) { w[1]["worker_session_id"] = "old" }, true},
		{"unsupported-protocol", func(w []map[string]any) { w[1]["component_info"].(map[string]any)["protocol_version"] = 2 }, true},
		{"failed-disk", func(w []map[string]any) {
			w[1]["storage_map"].(map[string]any)["disk"].(map[string]any)["failed"] = true
		}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workers := []map[string]any{record("192.0.2.1", "old", 1), record("192.0.2.2", "new", 2)}
			tt.change(workers)
			count := 0
			o := New(importedTransport(func(r *http.Request) (*http.Response, error) {
				count++
				if r.Host != "192.0.2.10:9000" {
					t.Fatal("unexpected request host", r.Host)
				}
				var data any
				switch r.URL.Path {
				case OverviewPath:
					data = map[string]any{"cluster_id": "cache", "master_addr": host, "master_state": "Active"}
				case WorkersPath:
					data = map[string]any{"live_workers": workers, "lost_workers": []any{}}
				default:
					t.Fatal("unexpected endpoint")
				}
				b, _ := json.Marshal(data)
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
			}))
			report, err := o.ObserveImported(context.Background(), "192.0.2.10", host, "cache", "192.0.2.1", "192.0.2.2", now)
			if (err != nil) != tt.fail {
				t.Fatalf("report=%+v error=%v", report, err)
			}
			if !tt.fail && (count != 3 || report.Registered != 2 || !report.ExistingHealthy || !report.AddedHealthy) {
				t.Fatal(report, count)
			}
		})
	}
}
func TestImportedRejectsMasterChange(t *testing.T) {
	count := 0
	now := time.Now()
	o := New(importedTransport(func(r *http.Request) (*http.Response, error) {
		count++
		data := `{"cluster_id":"cache","master_addr":"master:8995","master_state":"Active"}`
		if r.URL.Path == WorkersPath {
			data = `{"live_workers":[],"lost_workers":[]}`
		}
		if count == 3 {
			data = `{"cluster_id":"cache","master_addr":"master:8995","master_state":"Standby"}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(data))}, nil
	}))
	if _, err := o.ObserveImported(context.Background(), "192.0.2.10", "master:8995", "cache", "192.0.2.1", "192.0.2.2", now); err == nil {
		t.Fatal("role change accepted")
	}
}

package v054

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/observe"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func target(t *testing.T) observe.Target {
	t.Helper()
	f, err := os.Open("../../../examples/cluster-production.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c, err := api.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	c.Spec.Master.Replicas = 1
	c.Spec.Profile = "development"
	c.Spec.WorkerPools = c.Spec.WorkerPools[:1]
	c.Spec.WorkerPools[0].Replicas = 1
	return observe.Target{UID: "test-uid", Generation: 1, Name: c.Metadata.Name, Namespace: c.Metadata.Namespace, Spec: c.Spec}
}
func fixture(t *testing.T, target observe.Target, now time.Time) string {
	t.Helper()
	b, err := os.ReadFile("testdata/workers.json")
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReplacer("${WORKER_HOST}", WorkerHost(target.Name, target.Namespace, target.Spec.ClusterDomain, target.Spec.WorkerPools[0].Name, 0), "${NOW_MS}", fmt.Sprint(now.UnixMilli())).Replace(string(b))
}
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func overviewBody(target observe.Target, host, role string) string {
	b, _ := json.Marshal(map[string]any{"cluster_id": target.Spec.ClusterID, "master_addr": host + ":8995", "master_state": role, "live_workers": 1, "lost_workers": 0})
	return string(b)
}
func adapter(t *testing.T, target observe.Target, body string, now time.Time) *Observer {
	t.Helper()
	o := New(transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Scheme != "http" || r.URL.Port() != "9000" || r.Header.Get("Accept") != "application/json" {
			t.Error("unexpected request contract")
		}
		expected := false
		for i := 0; i < target.Spec.Master.Replicas; i++ {
			expected = expected || r.URL.Hostname() == MasterHost(target.Name, target.Namespace, target.Spec.ClusterDomain, i)
		}
		if !expected {
			t.Error("request escaped generated Master endpoints")
		}
		switch r.URL.Path {
		case OverviewPath:
			return response(overviewBody(target, r.URL.Hostname(), "Active")), nil
		case WorkersPath:
			return response(body), nil
		default:
			t.Error("unexpected or mutating path")
			return nil, ErrUnavailable
		}
	}))
	o.now = func() time.Time { return now }
	return o
}
func TestReleaseEvidenceDoesNotInventQuorumOrRuntimeVersion(t *testing.T) {
	target := target(t)
	now := time.Now()
	o := adapter(t, target, fixture(t, target, now), now)
	s, err := o.Observe(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Validate(target, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if s.Version != "" || s.Quorum != observe.Unknown || s.DataAvailability != observe.Unknown || s.Workers != observe.Healthy || s.RegisteredWorkers == nil || *s.RegisteredWorkers != 1 {
		t.Fatalf("invalid evidence: %+v", s)
	}
	p := s.Application.WorkerPools[0]
	if p.Fresh != 1 || p.VersionVerified != 1 || s.Application.SourceCommit != SourceCommit {
		t.Fatalf("missing build/heartbeat evidence: %+v", s.Application)
	}
	if !matchingBuild("89df141", "") || matchingBuild("89df141-dirty", Release) || matchingBuild("89df141", "v0.5.3") {
		t.Fatal("bad provenance matching")
	}
}
func TestWorkerContractFailures(t *testing.T) {
	target := target(t)
	now := time.Now()
	original := fixture(t, target, now)
	cases := []struct {
		name    string
		edit    func(string) string
		health  observe.Health
		invalid bool
	}{
		{"stale", func(s string) string {
			return strings.ReplaceAll(s, fmt.Sprint(now.UnixMilli()), fmt.Sprint(now.Add(-time.Minute).UnixMilli()))
		}, observe.Unhealthy, false},
		{"foreign", func(s string) string { return strings.ReplaceAll(s, "-worker-", "-foreign-") }, observe.Unhealthy, false},
		{"decommission-in-live-array", func(s string) string { return strings.ReplaceAll(s, `"Live"`, `"Decommission"`) }, observe.Unhealthy, false},
		{"wrong-source", func(s string) string { return strings.ReplaceAll(s, SourceCommit, strings.Repeat("a", 40)) }, observe.Unhealthy, false},
		{"unknown-build", func(s string) string { return strings.ReplaceAll(s, SourceCommit, "") }, observe.Unhealthy, false},
		{"legacy", func(s string) string { return strings.Replace(s, `"component_info":`, `"legacy_unused":`, 1) }, observe.Unknown, false},
		{"no-session", func(s string) string { return strings.ReplaceAll(s, "fixture-session-1", "") }, observe.Unknown, false},
		{"failed-storage", func(s string) string { return strings.ReplaceAll(s, `"failed": false`, `"failed": true`) }, observe.Unhealthy, false},
		{"missing-list", func(string) string { return `{}` }, "", true},
		{"null-list", func(string) string { return `{"live_workers":null,"lost_workers":[]}` }, "", true},
		{"empty", func(string) string { return `{"live_workers":[],"lost_workers":[]}` }, observe.Unhealthy, false},
		{"future", func(s string) string {
			return strings.ReplaceAll(s, fmt.Sprint(now.UnixMilli()), fmt.Sprint(now.Add(time.Minute).UnixMilli()))
		}, "", true},
		{"missing-required", func(s string) string { return strings.ReplaceAll(s, `"last_update":`, `"unrecognized":`) }, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := adapter(t, target, tc.edit(original), now).Observe(context.Background(), target)
			if tc.invalid {
				if !errors.Is(err, ErrInvalidResponse) {
					t.Fatalf("expected invalid response, got %v", err)
				}
				return
			}
			if err != nil || s.Workers != tc.health {
				t.Fatalf("health=%s err=%v", s.Workers, err)
			}
			if s.Quorum != observe.Unknown || s.DataAvailability != observe.Unknown {
				t.Fatal("invented health")
			}
		})
	}
}
func TestMasterIdentityAndRoleFences(t *testing.T) {
	target := target(t)
	target.Spec.Master.Replicas = 3
	now := time.Now()
	for _, mode := range []string{"stable", "split", "missing", "none", "identity", "transition"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			o := New(transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == WorkersPath {
					return response(fixture(t, target, now)), nil
				}
				n := requests.Add(1)
				host := r.URL.Hostname()
				role := "Standby"
				first := host == MasterHost(target.Name, target.Namespace, target.Spec.ClusterDomain, 0)
				if first && mode != "none" {
					role = "Active"
				}
				if mode == "split" {
					role = "Active"
				}
				if mode == "missing" && !first {
					return nil, ErrUnavailable
				}
				if mode == "transition" && n > 3 {
					role = "Standby"
				}
				body := overviewBody(target, host, role)
				if mode == "identity" {
					body = strings.ReplaceAll(body, target.Spec.ClusterID, "other-id")
				}
				return response(body), nil
			}))
			o.now = func() time.Time { return now }
			s, err := o.Observe(context.Background(), target)
			if mode == "identity" {
				if !errors.Is(err, ErrIdentityMismatch) {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "stable" {
				if s.Workers != observe.Healthy {
					t.Fatal(s)
				}
				return
			}
			if s.RegisteredWorkers != nil || s.Workers != observe.Unknown || s.Quorum != observe.Unknown {
				t.Fatalf("unsafe evidence after %s: %+v", mode, s)
			}
		})
	}
}
func TestHTTPBoundary(t *testing.T) {
	target := target(t)
	now := time.Now()
	cases := []struct {
		name, body, ctype string
		code              int
	}{
		{"html-fallback", "<html>UI</html>", "text/html", 200},
		{"duplicate", `{"cluster_id":"x","cluster_id":"y"}`, "application/json", 200},
		{"escaped-duplicate", `{"cluster_id":"x","cluster_\u0069d":"y"}`, "application/json", 200},
		{"trailing", `{} {}`, "application/json", 200},
		{"oversize", strings.Repeat(" ", maxBodyBytes+1), "application/json", 200},
		{"wrong-type", `{"cluster_id":42}`, "application/json", 200},
		{"redirect", "", "application/json", 302},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			o := New(transportFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				r := response(tc.body)
				r.StatusCode = tc.code
				r.Header.Set("Content-Type", tc.ctype)
				r.Header.Set("Location", "http://other.invalid/api/overview")
				return r, nil
			}))
			o.now = func() time.Time { return now }
			if _, err := o.Observe(context.Background(), target); err == nil {
				t.Fatal("accepted malformed response")
			}
			if calls.Load() != 1 {
				t.Fatal("followed redirect")
			}
		})
	}
	t.Run("cancellation", func(t *testing.T) {
		o := New(transportFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() }))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if _, err := o.Observe(ctx, target); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	})
	t.Run("invalid-target", func(t *testing.T) {
		o := New(transportFunc(func(*http.Request) (*http.Response, error) {
			t.Error("must not send request")
			return nil, ErrUnavailable
		}))
		target.Name = "bad/user"
		if _, err := o.Observe(context.Background(), target); err == nil {
			t.Fatal("accepted bad target")
		}
	})
}

// Package cacheobserve aggregates current, identity-bound FUSE node reports.
package cacheobserve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/cachecontract"
	"github.com/wsq2333/curvine-operator/internal/observe"
	"github.com/wsq2333/curvine-operator/internal/workload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Summary struct {
	Mount, Origin, Read, Cache observe.Health
	Nodes                      int32
	Reason                     string
}

func Unknown() Summary {
	return Summary{Mount: observe.Unknown, Origin: observe.Unknown, Read: observe.Unknown, Cache: observe.Unknown, Reason: "ProbeEvidenceUnavailable"}
}
func Collect(ctx context.Context, r client.Reader, c *api.CurvineCluster, transport http.RoundTripper) Summary {
	result := Unknown()
	var ds appsv1.DaemonSet
	if r.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: c.Name + "-fuse"}, &ds) != nil {
		return result
	}
	owned := false
	for _, ref := range ds.OwnerReferences {
		owned = owned || (ref.Controller != nil && *ref.Controller && ref.UID == c.UID && ref.Kind == "CurvineCluster" && ref.APIVersion == api.APIVersion)
	}
	if !owned || ds.Status.ObservedGeneration < ds.Generation || ds.Status.DesiredNumberScheduled <= 0 || ds.Status.DesiredNumberScheduled > 256 {
		return result
	}
	var pods corev1.PodList
	if r.List(ctx, &pods, client.InNamespace(c.Namespace), client.MatchingLabels{workload.UIDLabel: string(c.UID), workload.RoleLabel: "fuse"}) != nil {
		return result
	}
	var live []corev1.Pod
	seen := map[string]bool{}
	for _, p := range pods.Items {
		owner := false
		for _, ref := range p.OwnerReferences {
			owner = owner || (ref.Controller != nil && *ref.Controller && ref.UID == ds.UID && ref.Kind == "DaemonSet" && ref.APIVersion == "apps/v1")
		}
		if !owner || !p.DeletionTimestamp.IsZero() {
			continue
		}
		if p.Status.Phase != corev1.PodRunning || p.Spec.NodeName == "" || seen[p.Spec.NodeName] || net.ParseIP(p.Status.PodIP) == nil {
			return result
		}
		seen[p.Spec.NodeName] = true
		live = append(live, p)
	}
	if len(live) != int(ds.Status.DesiredNumberScheduled) {
		return result
	}
	if transport == nil {
		transport = &http.Transport{Proxy: nil, MaxConnsPerHost: 1, ResponseHeaderTimeout: 2 * time.Second}
	}
	httpClient := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer httpClient.CloseIdleConnections()
	conf := cachecontract.For(c)
	reports := make([]cachecontract.NodeReport, len(live))
	valid := make([]bool, len(live))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, p := range live {
		wg.Add(1)
		go func(i int, p corev1.Pod) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			request, _ := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(p.Status.PodIP, fmt.Sprint(cachecontract.ProbePort))+"/report", nil)
			response, err := httpClient.Do(request)
			if err != nil {
				return
			}
			defer response.Body.Close()
			if response.StatusCode != 200 || response.Header.Get("Content-Type") != "application/json" {
				return
			}
			body, err := io.ReadAll(io.LimitReader(response.Body, cachecontract.MaxReportBytes+1))
			if err != nil || len(body) > cachecontract.MaxReportBytes {
				return
			}
			var report cachecontract.NodeReport
			if json.Unmarshal(body, &report) != nil || !report.Valid(conf, time.Now()) {
				return
			}
			reports[i] = report
			valid[i] = true
		}(i, p)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return result
	}
	result = Summary{Mount: observe.Healthy, Origin: observe.Healthy, Read: observe.Healthy, Cache: observe.Healthy, Reason: "ScopedProbeVerified"}
	for i, v := range reports {
		if !valid[i] {
			return Unknown()
		}
		result.Nodes++
		if !v.MountVerified {
			result.Mount = observe.Unhealthy
		}
		if !v.OriginVerified {
			result.Origin = observe.Unhealthy
		}
		if !v.ReadVerified {
			result.Read = observe.Unhealthy
		}
		if !v.CacheHitObserved {
			result.Cache = observe.Unknown
		}
	}
	// An unsuccessful probe establishes a failure only for its configured scope.
	if result.Mount != observe.Healthy || result.Read != observe.Healthy {
		result.Cache = observe.Unknown
	}
	return result
}

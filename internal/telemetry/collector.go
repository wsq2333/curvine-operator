// Package telemetry exports bounded, fresh status evidence. Reading from the
// informer cache during collection avoids unbounded label registries and removes
// deleted clusters automatically. Missing/stale application samples emit no counts.
package telemetry

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Collector struct {
	Reader client.Reader
	Now    func() time.Time
	MaxAge time.Duration
}

var poolLabels = []string{"namespace", "cluster", "uid", "pool"}

func desc(name, help string, labels []string) *prometheus.Desc {
	return prometheus.NewDesc("curvine_operator_"+name, help, labels, nil)
}

var inventory = desc("inventory_available", "Whether the Kubernetes informer inventory could be read", nil)
var allocation = desc("worker_pool_replicas", "Current pool allocation or desired replicas", append(append([]string{}, poolLabels...), "state"))
var phase = desc("worker_pool_phase", "Current allocation phase; registration does not prove data placement", append(append([]string{}, poolLabels...), "phase"))
var stalled = desc("worker_pool_stalled", "No progress for ten minutes", poolLabels)
var registered = desc("worker_pool_registered", "Freshly observed registered workers", poolLabels)
var sampleTime = desc("application_sample_timestamp_seconds", "Application observation timestamp", []string{"namespace", "cluster", "uid"})

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{inventory, allocation, phase, stalled, registered, sampleTime} {
		ch <- d
	}
}
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	age := c.MaxAge
	if age <= 0 {
		age = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var list api.CurvineClusterList
	if c.Reader.List(ctx, &list) != nil {
		ch <- prometheus.MustNewConstMetric(inventory, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(inventory, prometheus.GaugeValue, 1)
	for _, cr := range list.Items {
		if !cr.DeletionTimestamp.IsZero() || cr.Status.ObservedGeneration != cr.Generation {
			continue
		}
		for _, p := range cr.Status.WorkerPools {
			if p.ObservedGeneration != cr.Generation || cr.Status.AllocationSampleTime == nil || now.Sub(cr.Status.AllocationSampleTime.Time) > age || cr.Status.AllocationSampleTime.After(now.Add(5*time.Second)) {
				continue
			}
			labels := []string{cr.Namespace, cr.Name, string(cr.UID), p.Name}
			for key, n := range map[string]int32{"desired": p.Desired, "created": p.Created, "scheduled": p.Scheduled, "ready": p.Ready, "volumes_bound": p.VolumesBound} {
				if key != "desired" && (p.Phase == "Unknown" || p.Phase == "Blocked") {
					continue
				}
				ch <- prometheus.MustNewConstMetric(allocation, prometheus.GaugeValue, float64(n), append(labels, key)...)
			}
			ch <- prometheus.MustNewConstMetric(phase, prometheus.GaugeValue, 1, append(labels, p.Phase)...)
			v := 0.
			if p.Stalled {
				v = 1
			}
			ch <- prometheus.MustNewConstMetric(stalled, prometheus.GaugeValue, v, labels...)
		}
		o := cr.Status.Observation
		if o == nil || o.SampleTime.IsZero() || now.Sub(o.SampleTime.Time) > age || o.SampleTime.After(now.Add(5*time.Second)) {
			continue
		}
		ch <- prometheus.MustNewConstMetric(sampleTime, prometheus.GaugeValue, float64(o.SampleTime.Unix()), cr.Namespace, cr.Name, string(cr.UID))
		if o.Application != nil {
			for _, p := range o.Application.WorkerPools {
				ch <- prometheus.MustNewConstMetric(registered, prometheus.GaugeValue, float64(p.Registered), cr.Namespace, cr.Name, string(cr.UID), p.Name)
			}
		}
	}
}

package v1alpha1

import (
	"encoding/json"
	"os"
	"testing"
)

func validCluster(t *testing.T) Cluster {
	t.Helper()
	data, err := os.ReadFile("../../examples/cluster-production.json")
	if err != nil {
		t.Fatal(err)
	}
	var c Cluster
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRejectUnsafeOrAmbiguousSpecs(t *testing.T) {
	cases := map[string]func(*Cluster){
		"production-single-master": func(c *Cluster) { c.Spec.Master.Replicas = 1 },
		"even-quorum":              func(c *Cluster) { c.Spec.Master.Replicas = 2 },
		"mutable-image":            func(c *Cluster) { c.Spec.Image = "curvine:latest" },
		"fake-digest-syntax":       func(c *Cluster) { c.Spec.Image = "curvine@sha256:abc" },
		"delete-data":              func(c *Cluster) { c.Spec.DeletionPolicy = "Delete" },
		"implicit-storage":         func(c *Cluster) { c.Spec.Master.Storage.StorageClassName = "" },
		"zero-disk":                func(c *Cluster) { c.Spec.Master.Storage.SizeGiB = 0 },
		"duplicate-pool":           func(c *Cluster) { c.Spec.WorkerPools = append(c.Spec.WorkerPools, c.Spec.WorkerPools[0]) },
		"zero-workers":             func(c *Cluster) { c.Spec.WorkerPools[0].Replicas = 0 },
		"no-pools":                 func(c *Cluster) { c.Spec.WorkerPools = nil },
		"bad-domain":               func(c *Cluster) { c.Spec.ClusterDomain = "cluster..local" },
		"wrong-api":                func(c *Cluster) { c.APIVersion = "v1" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			c := validCluster(t)
			edit(&c)
			if c.Validate() == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestExamplesAndDevelopmentSingleMaster(t *testing.T) {
	c := validCluster(t)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Spec.Profile = "development"
	c.Spec.Master.Replicas = 1
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/curvine/v054"
	"github.com/wsq2333/curvine-operator/internal/workload"
	corev1 "k8s.io/api/core/v1"
)

// Only renders isolated test inputs from the production builder. It does not
// count as a data-plane test: docker_read_cache.py runs the actual scenarios.
func TestRender(t *testing.T) {
	dir := os.Getenv("CURVINE_E2E_OUTPUT")
	if dir == "" {
		t.Fatal("CURVINE_E2E_OUTPUT is required")
	}
	data, err := os.ReadFile("../../examples/cluster-read-cache.json")
	if err != nil {
		t.Fatal(err)
	}
	var c api.CurvineCluster
	if err = json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	c.UID = "isolated-e2e"
	c.Generation = 1
	c.Spec.Profile = "development"
	c.Spec.Master.Replicas = 1
	c.Spec.WorkerPools = c.Spec.WorkerPools[:1]
	c.Spec.WorkerPools[0].Replicas = 1
	c.Spec.ReadCache.Endpoint = "http://origin:8080"
	c.Spec.ReadCache.TTLSeconds = 60
	b, err := workload.Build(&c, v054.Contract)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range b.Objects {
		if cm, ok := obj.(*corev1.ConfigMap); ok {
			encoded, _ := json.Marshal(cm)
			if err = os.WriteFile(filepath.Join(dir, "configmap.json"), encoded, 0600); err != nil {
				t.Fatal(err)
			}
			for name, content := range cm.Data {
				if err = os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	data, _ = json.Marshal(c)
	if err = os.WriteFile(filepath.Join(dir, "cluster.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	worker := b.Workers[0].Spec.Template.Spec.Containers[0].Env[1].Value
	worker = strings.ReplaceAll(worker, "$(POD_NAME)", b.Workers[0].Name+"-0")
	if err = os.WriteFile(filepath.Join(dir, "worker-host"), []byte(worker), 0600); err != nil {
		t.Fatal(err)
	}
}

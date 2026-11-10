//go:build integration

package existing

import (
	"context"
	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"os"
	"path/filepath"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"testing"
)

func TestExpansionAdmissionAndDefaulting(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	download := os.Getenv("ENVTEST_DOWNLOAD") == "1"
	if assets == "" && !download {
		t.Fatal("envtest assets required")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	useExisting := false
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(root, "config/crd/bases")}, ErrorIfCRDPathMissing: true, BinaryAssetsDirectory: assets, DownloadBinaryAssets: download, DownloadBinaryAssetsVersion: "1.36.0", UseExistingCluster: &useExisting}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	cl, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = cl.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}}); err != nil {
		t.Fatal(err)
	}
	c, in := fixture(t)
	c.UID = ""
	c.Generation = 0
	c.Spec.Replicas = 1
	if err = cl.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.Spec.Replicas = 2
	if err = cl.Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.Spec.Replicas = 1
	if err = cl.Update(ctx, c); err == nil {
		t.Fatal("scale-in admitted")
	}
	if err = cl.Get(ctx, client.ObjectKeyFromObject(c), c); err != nil {
		t.Fatal(err)
	}
	c.Spec.Binding.TargetNode = "different"
	if err = cl.Update(ctx, c); err == nil {
		t.Fatal("binding migration admitted")
	}
	if err = cl.Get(ctx, client.ObjectKeyFromObject(c), c); err != nil {
		t.Fatal(err)
	}
	source := in.Worker.DeepCopy()
	source.UID = ""
	source.ResourceVersion = ""
	source.Generation = 0
	if err = cl.Create(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err = cl.Get(ctx, client.ObjectKeyFromObject(source), source); err != nil {
		t.Fatal(err)
	}
	want, _, _ := Build(c, source)
	live := want.DeepCopy()
	if err = cl.Create(ctx, live); err != nil {
		t.Fatal(err)
	}
	if err = cl.Get(ctx, client.ObjectKeyFromObject(live), live); err != nil {
		t.Fatal(err)
	}
	if !matches(c, live, want) {
		t.Fatalf("server defaults cause false drift\nwant=%+v\ngot=%+v", want.Spec, live.Spec)
	}
}

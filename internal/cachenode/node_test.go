package cachenode

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/cachecontract"
)

func TestProbeIntegrityAndSymlinks(t *testing.T) {
	root := t.TempDir()
	content := []byte("immutable data")
	c := cachecontract.Config{ReadCache: api.ReadCacheSpec{Probe: api.ReadCacheProbe{Object: "probe", SizeBytes: len(content), SHA256: fmt.Sprintf("%x", sha256.Sum256(content))}}}
	if err := os.WriteFile(filepath.Join(root, "probe"), content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(root, c); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "probe"), []byte("different data"), 0600); err != nil {
		t.Fatal(err)
	}
	if VerifyFile(root, c) == nil {
		t.Fatal("changed source accepted")
	}
	if err := os.Remove(filepath.Join(root, "probe")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "probe")); err != nil {
		t.Fatal(err)
	}
	if VerifyFile(root, c) == nil {
		t.Fatal("symlink followed")
	}
}
func TestMountReadOnlyEvidence(t *testing.T) {
	for _, tc := range []struct {
		line string
		ok   bool
	}{
		{"1 2 0:1 / /cache ro,relatime - fuse.curvine curvine rw", true},
		{"1 2 0:1 / /cache rw,relatime - fuse.curvine curvine rw", false},
		{"1 2 0:1 / /cache ro - ext4 /dev/a rw", false},
		{"1 2 0:1 / /other ro - fuse.curvine curvine rw", false},
	} {
		if mountedRO(tc.line, "/cache") != tc.ok {
			t.Fatalf("unexpected mount evidence: %s", tc.line)
		}
	}
}
func TestNodeReportMustBeCurrentAndBound(t *testing.T) {
	now := time.Now()
	c := cachecontract.Config{ClusterUID: "uid", ConfigHash: "hash"}
	r := cachecontract.NodeReport{ClusterUID: "uid", ConfigHash: "hash", SampleTime: now}
	if !r.Valid(c, now) {
		t.Fatal("valid report rejected")
	}
	r.ConfigHash = "old"
	if r.Valid(c, now) {
		t.Fatal("old config accepted")
	}
	r.ConfigHash = "hash"
	r.SampleTime = now.Add(-2 * time.Minute)
	if r.Valid(c, now) {
		t.Fatal("stale report accepted")
	}
}

func TestCacheEvidenceRequiresSuccessfulCurvineBytes(t *testing.T) {
	before := parseMetrics("client_mount_cache_hits{id=\"1\"} 4\nclient_read_bytes 100\n")
	after := parseMetrics("client_mount_cache_hits{id=\"1\"} 5\nclient_read_bytes 125\n")
	if !hitObserved(before, after, 25) {
		t.Fatal("cache read evidence rejected")
	}
	after.Bytes = before.Bytes
	if hitObserved(before, after, 25) {
		t.Fatal("fallback open mislabeled as cached read")
	}
	after.Bytes = 110
	if hitObserved(before, after, 25) {
		t.Fatal("partial cached data proves a full probe")
	}
	after = parseMetrics("client_mount_cache_hits{id=\"1\"} 6\n")
	if hitObserved(before, after, 25) {
		t.Fatal("missing byte evidence accepted")
	}
	after = parseMetrics("client_mount_cache_hits{id=\"1\"} 6\nclient_read_bytes +Inf\n")
	if hitObserved(before, after, 25) {
		t.Fatal("invalid counter accepted")
	}
}

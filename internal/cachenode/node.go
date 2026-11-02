// Package cachenode supervises the pinned FUSE binary and exposes bounded,
// scoped probe evidence. File reads run in a single subprocess so a stalled
// FUSE read cannot wedge the HTTP server or accumulate probe goroutines.
package cachenode

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wsq2333/curvine-operator/internal/cachecontract"
)

const ConfigPath = "/etc/curvine/cache.json"
const MountRoot = "/var/lib/curvine-export/data"
const helper = "/app/curvine/lib/curvine-cache-mount"

func Load(path string) (cachecontract.Config, error) {
	var c cachecontract.Config
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 65536 {
		return c, errors.New("invalid cache config")
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.ClusterUID == "" || len(c.ConfigHash) != 64 || c.MountPath != cachecontract.MountPath || c.ReadCache.Validate() != nil {
		return c, errors.New("invalid cache config")
	}
	return c, nil
}
func command(ctx context.Context, path string, args ...string) bool {
	call, cancel := context.WithTimeout(ctx, 28*time.Second)
	defer cancel()
	cmd := exec.CommandContext(call, path, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}
func mountedRO(data string, path string) bool {
	for _, line := range strings.Split(data, "\n") {
		left, right, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		a, b := strings.Fields(left), strings.Fields(right)
		if len(a) >= 6 && len(b) >= 3 && a[4] == path && strings.HasPrefix(b[0], "fuse") {
			for _, opt := range strings.Split(a[5], ",") {
				if opt == "ro" {
					return true
				}
			}
		}
	}
	return false
}

// VerifyMountedFile also rejects an ordinary host directory exposed before the
// FUSE mount has propagated into a consumer pod.
func VerifyMountedFile(c cachecontract.Config) error {
	info, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil || !mountedRO(string(info), MountRoot) {
		return errors.New("read-only FUSE mount unavailable")
	}
	return VerifyFile(MountRoot, c)
}

// VerifyFile never writes through FUSE and never follows probe symlinks.
func VerifyFile(root string, c cachecontract.Config) error {
	current := root
	for _, part := range strings.Split(c.ReadCache.Probe.Object, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("probe inaccessible")
		}
	}
	f, err := os.Open(current)
	if err != nil {
		return errors.New("probe inaccessible")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() != int64(c.ReadCache.Probe.SizeBytes) {
		return errors.New("probe size mismatch")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, int64(c.ReadCache.Probe.SizeBytes)+1))
	if err != nil || n != int64(c.ReadCache.Probe.SizeBytes) || fmt.Sprintf("%x", h.Sum(nil)) != c.ReadCache.Probe.SHA256 {
		return errors.New("probe content mismatch")
	}
	return nil
}

type cacheMetrics struct {
	Hits, Bytes float64
	Valid       bool
}

func parseMetrics(data string) cacheMetrics {
	var m cacheMetrics
	var hitFound, bytesFound bool
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			continue
		}
		name := fields[0]
		if strings.HasPrefix(name, "client_mount_cache_hits{") || strings.HasPrefix(name, "client_mount_cache_hits_total{") {
			m.Hits += value
			hitFound = true
		}
		if name == "client_read_bytes" || name == "client_read_bytes_total" {
			m.Bytes = value
			bytesFound = true
		}
	}
	m.Valid = hitFound && bytesFound
	return m
}

func counters(ctx context.Context) cacheMetrics {
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:9000/metrics", nil)
	resp, err := client.Do(req)
	if err != nil {
		return cacheMetrics{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return cacheMetrics{}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return cacheMetrics{}
	}
	return parseMetrics(string(data))
}

func hitObserved(before, after cacheMetrics, size int) bool {
	// Upstream increments hits before FallbackFsReader runs. A hit counter
	// alone can therefore describe an open that ultimately read only from UFS.
	return before.Valid && after.Valid && after.Hits > before.Hits && after.Bytes-before.Bytes >= float64(size)
}

func sample(ctx context.Context, c cachecontract.Config) cachecontract.NodeReport {
	r := cachecontract.NodeReport{ClusterUID: c.ClusterUID, ConfigHash: c.ConfigHash, SampleTime: time.Now().UTC()}
	r.MountVerified = command(ctx, helper, "check")
	r.OriginVerified = command(ctx, helper, "origin")
	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil || !mountedRO(string(mountinfo), MountRoot) {
		return r
	}
	before := counters(ctx)
	binary, err := os.Executable()
	if err != nil {
		return r
	}
	r.ReadVerified = command(ctx, binary, "--read-probe")
	after := counters(ctx)
	r.CacheHitObserved = r.ReadVerified && hitObserved(before, after, c.ReadCache.Probe.SizeBytes)
	// Observations are deliberately stamped at the beginning of the full probe.
	return r
}
func Run() error {
	c, err := Load(ConfigPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err = os.MkdirAll(MountRoot, 0755); err != nil {
		return errors.New("mount directory unavailable")
	}
	// Refuse stale mounts instead of unmounting an unknown live client.
	info, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return errors.New("mount table unavailable")
	}
	for _, line := range strings.Split(string(info), "\n") {
		f := strings.Fields(line)
		if len(f) > 4 && f[4] == MountRoot {
			return errors.New("mount already exists; manual recovery required")
		}
	}
	fuse := exec.Command("/app/curvine/lib/curvine-fuse", "--conf", "/etc/curvine/fuse.toml", "--readonly")
	fuse.Stdout = io.Discard
	fuse.Stderr = io.Discard
	if err = fuse.Start(); err != nil {
		return errors.New("FUSE startup failed")
	}
	done := make(chan error, 1)
	go func() { done <- fuse.Wait() }()
	defer func() { _ = fuse.Process.Kill() }()
	var mu sync.RWMutex
	report := cachecontract.NodeReport{ClusterUID: c.ClusterUID, ConfigHash: c.ConfigHash}
	go func() {
		for {
			r := sample(ctx, c)
			mu.Lock()
			report = r
			mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/report", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		mu.RLock()
		v := report
		mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		v := report
		mu.RUnlock()
		if !v.Valid(c, time.Now()) || !v.MountVerified || !v.ReadVerified {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	})
	server := &http.Server{Addr: fmt.Sprintf(":%d", cachecontract.ProbePort), Handler: mux, ReadHeaderTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second}
	failed := make(chan error, 1)
	go func() { failed <- server.ListenAndServe() }()
	defer server.Close()
	select {
	case <-ctx.Done():
		_ = fuse.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
			return nil
		case <-time.After(45 * time.Second):
			return errors.New("FUSE shutdown timed out")
		}
	case <-done:
		return errors.New("FUSE exited")
	case <-failed:
		return errors.New("probe server failed")
	}
}

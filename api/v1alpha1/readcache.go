package v1alpha1

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// ReadCacheSpec is an opt-in, immutable deployment purpose. UFS is the source
// of truth; credentials must grant only read/list on the declared prefix.
// The first implementation serves immutable datasets through read-only FUSE.
type ReadCacheSpec struct {
	// +kubebuilder:validation:Enum=S3;OSS
	Backend string `json:"backend"`
	// +kubebuilder:validation:MaxLength=1024
	Source string `json:"source"`
	// +kubebuilder:validation:MaxLength=253
	Endpoint string `json:"endpoint"`
	// +kubebuilder:validation:MaxLength=63
	Region string `json:"region"`
	// CredentialsSecret must contain accessKey and secretKey. The manager never
	// reads Secret values; Kubernetes projects them only into the mount/probe pods.
	// +kubebuilder:validation:MaxLength=253
	CredentialsSecret string `json:"credentialsSecret"`
	// +kubebuilder:validation:Enum=Immutable
	Consistency string `json:"consistency"`
	// +kubebuilder:validation:Minimum=60
	// +kubebuilder:validation:Maximum=2592000
	TTLSeconds int `json:"ttlSeconds"`
	// Per-reader UFS range concurrency; not a cluster-wide request rate limit.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16
	ReadConcurrency int `json:"readConcurrency"`
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=50
	MinFreePercent int            `json:"minFreePercent"`
	Probe          ReadCacheProbe `json:"probe"`
}

type ReadCacheProbe struct {
	// Relative, immutable object key below Source. It must be non-sensitive.
	// +kubebuilder:validation:MaxLength=512
	Object string `json:"object"`
	// +kubebuilder:validation:Pattern="^[a-f0-9]{64}$"
	SHA256 string `json:"sha256"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16777216
	SizeBytes int `json:"sizeBytes"`
}

var safeObject = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

func cleanObject(s string) bool {
	return s != "" && safeObject.MatchString(s) && !strings.HasPrefix(s, "/") && path.Clean(s) == s && s != "." && s != ".." && !strings.HasPrefix(s, "../")
}
func (r ReadCacheSpec) Validate() error {
	scheme := "s3"
	if r.Backend == "OSS" {
		scheme = "oss"
	} else if r.Backend != "S3" {
		return fmt.Errorf("readCache backend must be S3 or OSS")
	}
	u, err := url.Parse(r.Source)
	if err != nil || len(r.Source) > 1024 || u.Scheme != scheme || !validDomain(u.Host) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Port() != "" {
		return fmt.Errorf("readCache source must be a credential-free bucket/prefix URI matching backend")
	}
	if u.Path != "" && u.Path != "/" && !cleanObject(strings.TrimPrefix(u.Path, "/")) {
		return fmt.Errorf("readCache source prefix must be canonical without trailing slash")
	}
	e, err := url.Parse(r.Endpoint)
	if err != nil || len(r.Endpoint) > 253 || (e.Scheme != "https" && e.Scheme != "http") || e.Hostname() == "" || e.User != nil || e.RawQuery != "" || e.Fragment != "" || e.Path != "" || e.RawPath != "" {
		return fmt.Errorf("readCache endpoint must be an HTTP(S) origin without credentials, path or query")
	}
	if !validLabel(r.Region, 63) || !validDomain(r.CredentialsSecret) || r.Consistency != "Immutable" {
		return fmt.Errorf("readCache requires region, Secret reference and Immutable consistency")
	}
	if r.TTLSeconds < 60 || r.TTLSeconds > 2592000 || r.ReadConcurrency < 1 || r.ReadConcurrency > 16 || r.MinFreePercent < 10 || r.MinFreePercent > 50 {
		return fmt.Errorf("readCache TTL, concurrency or free-space budget outside supported bounds")
	}
	if !cleanObject(r.Probe.Object) || len(r.Probe.Object) > 512 || !digest.MatchString(r.Probe.SHA256) || r.Probe.SizeBytes < 1 || r.Probe.SizeBytes > 16<<20 {
		return fmt.Errorf("readCache probe requires a canonical object key, SHA256 and bounded positive size")
	}
	return nil
}

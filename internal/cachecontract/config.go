// Package cachecontract is the non-secret contract shared with node helpers.
package cachecontract

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
)

const MountPath = "/read-cache"
const NodeLabel = "storage.curvine.io/fuse"
const ProbePort = 9101
const MaxReportBytes = 4096

type Config struct {
	ClusterUID string            `json:"clusterUID"`
	ConfigHash string            `json:"configHash"`
	MountPath  string            `json:"mountPath"`
	ReadCache  api.ReadCacheSpec `json:"readCache"`
}

func For(c *api.CurvineCluster) Config {
	conf := Config{ClusterUID: string(c.UID), MountPath: MountPath, ReadCache: *c.Spec.ReadCache}
	b, _ := json.Marshal(conf)
	conf.ConfigHash = fmt.Sprintf("%x", sha256.Sum256(b))
	return conf
}
func HostPath(c *api.CurvineCluster) string { return "/var/lib/curvine-fuse/" + string(c.UID) }

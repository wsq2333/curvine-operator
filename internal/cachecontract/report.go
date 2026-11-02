package cachecontract

import "time"

// NodeReport covers only the configured immutable probe on one FUSE client.
// It is neither an aggregate data availability nor a Raft quorum certificate.
type NodeReport struct {
	ClusterUID       string    `json:"clusterUID"`
	ConfigHash       string    `json:"configHash"`
	SampleTime       time.Time `json:"sampleTime"`
	MountVerified    bool      `json:"mountVerified"`
	OriginVerified   bool      `json:"originVerified"`
	ReadVerified     bool      `json:"readVerified"`
	CacheHitObserved bool      `json:"cacheHitObserved"`
}

func (r NodeReport) Valid(c Config, now time.Time) bool {
	return r.ClusterUID == c.ClusterUID && r.ConfigHash == c.ConfigHash && !r.SampleTime.IsZero() && now.Sub(r.SampleTime) <= 90*time.Second && !r.SampleTime.After(now.Add(5*time.Second))
}

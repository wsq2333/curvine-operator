package v054

// Pointers distinguish missing/null from legitimate zero-valued fields. Unknown
// additive fields are ignored; these names/types follow the pinned Rust serde.
type overview struct {
	ClusterID     *string `json:"cluster_id"`
	MasterAddress *string `json:"master_addr"`
	State         *string `json:"master_state"`
	LiveWorkers   *int32  `json:"live_workers"`
	LostWorkers   *int32  `json:"lost_workers"`
}
type inventory struct {
	Live *[]worker `json:"live_workers"`
	Lost *[]worker `json:"lost_workers"`
}
type worker struct {
	Address    *address           `json:"address"`
	Status     *string            `json:"status"`
	LastUpdate *int64             `json:"last_update"`
	Session    *string            `json:"worker_session_id"`
	Capacity   *int64             `json:"capacity"`
	Available  *int64             `json:"available"`
	Storage    map[string]storage `json:"storage_map"`
	Version    *component         `json:"component_info"`
}
type address struct {
	ID      *uint32 `json:"worker_id"`
	Host    *string `json:"hostname"`
	RPCPort *uint32 `json:"rpc_port"`
}
type storage struct {
	Failed *bool `json:"failed"`
}
type component struct {
	GitCommit          string `json:"git_commit"`
	GitTag             string `json:"git_tag"`
	ProtocolVersion    uint32 `json:"protocol_version"`
	MinProtocolVersion uint32 `json:"min_protocol_version"`
}

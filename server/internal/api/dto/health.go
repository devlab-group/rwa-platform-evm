package dto

// Health mirrors components.schemas.Health, shared by the liveness and
// readiness probes — Status distinguishes them ("ok" vs "ready").
type Health struct {
	Status           string `json:"status"`
	ChainID          int64  `json:"chainId"`
	LastIndexedBlock uint64 `json:"lastIndexedBlock"`
}

// NewHealth builds the probe view from server state; like
// NewBootstrapConfigResponse there is no model behind it.
func NewHealth(status string, chainID int64, lastIndexedBlock uint64) Health {
	return Health{Status: status, ChainID: chainID, LastIndexedBlock: lastIndexedBlock}
}

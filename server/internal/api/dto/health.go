package dto

// Health mirrors components.schemas.Health, shared by the liveness and
// readiness probes. Status is "ok" for liveness and "ready" for readiness.
type Health struct {
	Status           string `json:"status"`
	ChainID          int64  `json:"chainId"`
	LastIndexedBlock uint64 `json:"lastIndexedBlock"`
}

// NewHealth builds the probe view from server state; no model backs it
func NewHealth(status string, chainID int64, lastIndexedBlock uint64) Health {
	return Health{Status: status, ChainID: chainID, LastIndexedBlock: lastIndexedBlock}
}

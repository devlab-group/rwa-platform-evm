package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rwa-platform/server/internal/api/dto"
)

// TestGetConfigReturnsChainAndFactory covers the PUBLIC GET /api/v1/config
// bootstrap endpoint the admin's web wallet reads to broadcast RWAFactory.deploy
// itself (deployment moved off the server — it now only observes the
// ProjectDeployed event; see project.ReconcileDeployment).
func TestGetConfigReturnsChainAndFactory(t *testing.T) {
	env := setupTestApp(t)
	env.app.FactoryAddress = "0x0000000000000000000000000000000000FACE"
	env.app.ProjectID = "11111111-2222-3333-4444-555555555555"

	// No auth header: /config is public.
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/config", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("config status = %d, body=%s", w.Code, w.Body.String())
	}
	var got dto.BootstrapConfigResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.FactoryAddress != env.app.FactoryAddress {
		t.Errorf("FactoryAddress = %q, want %q", got.FactoryAddress, env.app.FactoryAddress)
	}
	if got.ChainID != env.app.ChainID {
		t.Errorf("ChainID = %d, want %d", got.ChainID, env.app.ChainID)
	}
	if got.ProjectID != env.app.ProjectID {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, env.app.ProjectID)
	}
}

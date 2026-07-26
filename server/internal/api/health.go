package api

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
)

// getHealth implements GET /healthz (operationId getHealth).
// Public liveness probe: always 200 with the chain id and the indexer's last
// processed block — it asserts nothing about dependencies (see getReady).
func (app *App) getHealth(c *gin.Context) {
	c.JSON(http.StatusOK, dto.NewHealth("ok", app.ChainID, app.lastIndexedBlock()))
}

// getReady implements GET /readyz (operationId getReady).
// Public readiness probe: 200 only when the configured chain/storage/indexer
// checks all pass, 503 with a coarse reason otherwise.
func (app *App) getReady(c *gin.Context) {
	// Default-deny. A nil ReadyCheck means the chain-dependent services were
	// never wired (no/failed/wrong-chain RPC), which is NOT ready. Treating a
	// nil check as success would report "ready" and let an instance with no
	// chain connectivity pass orchestration health checks. Never report ready
	// by omission.
	if app.ReadyCheck == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": "readiness check not configured (chain services unavailable)"})
		return
	}
	if err := app.ReadyCheck(); err != nil {
		// /readyz is public and unauthenticated, so the client gets only a
		// coarse category — the underlying errors wrap RPC dial and Mongo ping
		// failures and would otherwise hand out backend host:port and RPC
		// endpoint details to anyone who can reach the probe. The operator
		// still gets the full text, in the log.
		log.Printf("api: readiness check failed: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": readinessReason(err)})
		return
	}
	c.JSON(http.StatusOK, dto.NewHealth("ready", app.ChainID, app.lastIndexedBlock()))
}

// readinessReason classifies a readiness failure into a fixed, non-revealing
// label. The readiness closure (see cmd/platform's buildApp) tags each check it
// runs with a stable prefix; anything unrecognized degrades to the generic
// "not ready" rather than leaking the raw text.
func readinessReason(err error) string {
	switch {
	case err == nil:
		return ""
	case strings.HasPrefix(err.Error(), "chain:"):
		return "chain unavailable"
	case strings.HasPrefix(err.Error(), "storage:"):
		return "storage unavailable"
	case strings.HasPrefix(err.Error(), "indexer:"):
		return "indexer unavailable"
	default:
		return "not ready"
	}
}

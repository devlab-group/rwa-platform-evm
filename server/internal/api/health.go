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
// processed block. It checks no dependencies (see getReady).
func (app *App) getHealth(c *gin.Context) {
	c.JSON(http.StatusOK, dto.NewHealth("ok", app.ChainID, app.lastIndexedBlock()))
}

// getReady implements GET /readyz (operationId getReady).
// Public readiness probe: 200 when the chain, storage and indexer checks all
// pass, otherwise 503 with a coarse reason.
func (app *App) getReady(c *gin.Context) {
	// A nil ReadyCheck means the chain-dependent services were never wired (no,
	// failed or wrong-chain RPC), so the instance must not report ready
	if app.ReadyCheck == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": "readiness check not configured (chain services unavailable)"})
		return
	}
	if err := app.ReadyCheck(); err != nil {
		// /readyz is unauthenticated, so the client gets only a coarse category.
		// The underlying errors wrap RPC dial and Mongo ping failures that carry
		// backend host:port and RPC endpoint details; the full text goes to the log.
		log.Printf("api: readiness check failed: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": readinessReason(err)})
		return
	}
	c.JSON(http.StatusOK, dto.NewHealth("ready", app.ChainID, app.lastIndexedBlock()))
}

// readinessReason maps a readiness failure to a fixed label that reveals no
// backend detail. The readiness closure in cmd/platform prefixes each failure
// with "chain:", "storage:" or "indexer:"; any other error maps to "not ready".
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

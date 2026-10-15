package api

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
)

// getHealth serves public GET /healthz: always 200 with chain ID and indexer block,
// without dependency checks; see getReady
func (app *App) getHealth(c *gin.Context) {
	c.JSON(http.StatusOK, dto.NewHealth("ok", app.ChainID, app.lastIndexedBlock()))
}

// getReady serves public GET /readyz: 200 if chain, storage and indexer checks pass,
// otherwise 503 with a coarse reason
func (app *App) getReady(c *gin.Context) {
	// A nil ReadyCheck means the chain-dependent services were never wired (no,
	// failed or wrong-chain RPC), so the instance must not report ready
	if app.ReadyCheck == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": "readiness check not configured (chain services unavailable)"})
		return
	}
	if err := app.ReadyCheck(); err != nil {
		// Public /readyz returns only a coarse category. Full errors, which may expose Mongo
		// hosts or RPC endpoints, go to the log.
		log.Printf("api: readiness check failed: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": readinessReason(err)})
		return
	}
	c.JSON(http.StatusOK, dto.NewHealth("ready", app.ChainID, app.lastIndexedBlock()))
}

// readinessReason maps cmd/platform's "chain:", "storage:" and "indexer:" error
// prefixes to fixed labels. Other errors become "not ready"; backend details remain
// private.
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

package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// getInventory implements GET /api/v1/sales/inventory (operationId
// getInventory).
// PUBLIC: market-facing supply/price figures an investor reads before ever
// authenticating — the same on-chain state is trivially readable directly.
func (app *App) getInventory(c *gin.Context) {
	if app.Sales == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "sales is not configured on this server")
		return
	}
	inv, err := app.Sales.GetInventory(c.Request.Context())
	if err != nil {
		failErr(c, http.StatusInternalServerError, CodeInternal, err)
		return
	}
	c.JSON(http.StatusOK, inv)
}

// listPurchases implements GET /api/v1/sales/purchases (operationId
// listPurchases).
// Admin-only, keyset-paginated page of the indexed purchase history.
//
// Uses repository-level keyset pagination
// (PurchaseRepository.ListPage): the database query itself is bounded to
// one page — this handler no longer fetches the whole collection and
// slices it. X-Total-Count is omitted (not cheaply knowable without an
// extra full-collection count, which would reintroduce exactly the
// unbounded work being removed here) — same "omitted rather than sent
// wrong" convention setPaginationHeaders already documents for
// listAuditLogs.
func (app *App) listPurchases(c *gin.Context) {
	if app.Sales == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "sales is not configured on this server")
		return
	}
	cursor, limit := cursorLimitParams(c)
	page, next, err := app.Sales.ListPurchases(c.Request.Context(), app.lastIndexedBlock(), cursor, limit)
	if err != nil {
		failErr(c, http.StatusInternalServerError, CodeInternal, err)
		return
	}
	setPaginationHeaders(c, -1, len(page), next)
	c.JSON(http.StatusOK, page)
}

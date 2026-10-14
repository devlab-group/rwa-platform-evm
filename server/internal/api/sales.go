package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// getInventory implements GET /api/v1/sales/inventory (operationId
// getInventory).
// Public: supply and price figures an investor reads before authenticating.
// The same state is readable on-chain.
func (app *App) getInventory(c *gin.Context) {
	if app.Sales == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "sales is not configured on this server")
		return
	}
	inv, err := app.Sales.GetInventory(c.Request.Context())
	if err != nil {
		failInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, inv)
}

// listPurchases implements GET /api/v1/sales/purchases (operationId
// listPurchases).
// Admin-only, keyset-paginated page of the indexed purchase history. The query
// is bounded to one page by PurchaseRepository.ListPage, and X-Total-Count is
// omitted because counting would scan the whole collection.
func (app *App) listPurchases(c *gin.Context) {
	if app.Sales == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "sales is not configured on this server")
		return
	}
	cursor, limit := cursorLimitParams(c)
	page, next, err := app.Sales.ListPurchases(c.Request.Context(), app.lastIndexedBlock(), cursor, limit)
	if err != nil {
		failInternal(c, err)
		return
	}
	setPaginationHeaders(c, -1, len(page), next)
	c.JSON(http.StatusOK, page)
}

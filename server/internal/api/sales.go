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

// listPurchases handles admin-only GET /api/v1/sales/purchases.
// PurchaseRepository.ListPage bounds the keyset query; X-Total-Count is omitted to
// avoid a collection-wide count.
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

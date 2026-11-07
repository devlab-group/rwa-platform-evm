package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/sales"
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

// listPurchaseOrders implements GET /api/v1/sales/orders (operationId listPurchaseOrders).
// Public Vault event projections use keyset pagination without X-Total-Count.
// Optional filters match status exactly and buyer addresses without regard to case.
func (app *App) listPurchaseOrders(c *gin.Context) {
	if app.Sales == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "sales is not configured on this server")
		return
	}
	cursor, limit := cursorLimitParams(c)
	page, next, err := app.Sales.ListOrdersPage(c.Request.Context(), c.Query("status"), c.Query("address"), cursor, limit)
	if err != nil {
		failOrders(c, err)
		return
	}
	out := make([]dto.PurchaseOrderResponse, len(page))
	for i, o := range page {
		out[i] = dto.ToPurchaseOrderResponse(o)
	}
	setPaginationHeaders(c, -1, len(out), next)
	c.JSON(http.StatusOK, out)
}

// getPurchaseOrder implements GET /api/v1/sales/orders/{id} (operationId getPurchaseOrder).
// The response is public, like the on-chain Vault.getPurchase(id) view
func (app *App) getPurchaseOrder(c *gin.Context) {
	if app.Sales == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "sales is not configured on this server")
		return
	}
	o, err := app.Sales.GetOrder(c.Request.Context(), c.Param("id"))
	if err != nil {
		failOrders(c, err)
		return
	}
	c.JSON(http.StatusOK, dto.ToPurchaseOrderResponse(o))
}

// failOrders maps an unconfigured order read model to 501, otherwise as notFoundOrInternal
func failOrders(c *gin.Context, err error) {
	if errors.Is(err, sales.ErrOrdersNotConfigured) {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "purchase orders are not configured on this server")
		return
	}
	notFoundOrInternal(c, err)
}

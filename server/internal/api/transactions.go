package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
)

// listTransactions serves public server and indexed-wallet transaction history.
// Keyset pagination and a case-insensitive From/To filter run in the repository query.
// Indexed actors come from event fields; events without one, such as Minted, leave From empty.
func (app *App) listTransactions(c *gin.Context) {
	cursor, limit := cursorLimitParams(c)
	page, next, err := app.Repos.Transactions.ListPage(c.Request.Context(), c.Query("address"), cursor, limit)
	if err != nil {
		failInternal(c, err)
		return
	}
	out := make([]dto.TransactionResponse, len(page))
	for i, tx := range page {
		out[i] = dto.ToTransactionResponse(tx, app.ExplorerURL)
	}
	setPaginationHeaders(c, -1, len(out), next)
	c.JSON(http.StatusOK, out)
}

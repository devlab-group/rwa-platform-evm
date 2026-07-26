package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
)

// listTransactions implements GET /api/v1/transactions (operationId
// listTransactions).
// PUBLIC (on-chain-public data; issuers build their own investor UIs),
// keyset-paginated, with an optional `address` filter matched against From OR
// To.
//
// This collection holds BOTH transactions the server itself submitted via
// TxManager (compliance status, mint/burn relay, project deploy) AND
// event-derived records synthesized from indexed chain events by
// txindex.ReconcileStackTransactions for on-chain actions broadcast from
// wallets (treasury withdrawals, role changes, pauses, price updates, buys,
// redemption lifecycle, admin transfers, compliance status). The event-derived
// records set From to the acting EOA decoded from the event
// (caller/sender/buyer/...), so an `address` filter also finds a wallet's
// actions that never touched TxManager — subject to what each event exposes (an
// event with no acting-EOA field, e.g. Minted, leaves From empty).
//
// The address filter is pushed into the query itself (case-insensitive,
// matching the exact strings.ToLower comparison this handler used to do
// post-fetch), not applied to an unbounded in-handler fetch. See
// api.listPurchases' doc comment for the pagination-contract reasoning (bounded
// query, X-Total-Count omitted).
func (app *App) listTransactions(c *gin.Context) {
	cursor, limit := cursorLimitParams(c)
	page, next, err := app.Repos.Transactions.ListPage(c.Request.Context(), c.Query("address"), cursor, limit)
	if err != nil {
		failErr(c, http.StatusInternalServerError, CodeInternal, err)
		return
	}
	out := make([]dto.TransactionResponse, len(page))
	for i, tx := range page {
		out[i] = dto.ToTransactionResponse(tx)
	}
	setPaginationHeaders(c, -1, len(out), next)
	c.JSON(http.StatusOK, out)
}

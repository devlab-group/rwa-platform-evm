package api

import (
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/auth"
	"github.com/rwa-platform/server/internal/compliance"
	"github.com/rwa-platform/server/internal/dal/models"
)

// listWallets implements GET /api/v1/compliance/wallets (operationId
// listWallets).
// Admin-only, keyset-paginated page of investor wallet/KYC statuses.
//
// Uses repository-level keyset pagination (InvestorRepository.ListPage) — see
// api.listPurchases' doc comment for the same reasoning (bounded query,
// X-Total-Count omitted).
func (app *App) listWallets(c *gin.Context) {
	cursor, limit := cursorLimitParams(c)
	page, next, err := app.Repos.Investors.ListPage(c.Request.Context(), cursor, limit)
	if err != nil {
		failErr(c, http.StatusInternalServerError, CodeInternal, err)
		return
	}
	out := make([]dto.WalletStatus, len(page))
	for i, inv := range page {
		out[i] = dto.ToWalletStatus(inv)
	}
	setPaginationHeaders(c, -1, len(out), next)
	c.JSON(http.StatusOK, out)
}

// createChallenge implements POST /api/v1/compliance/challenge (operationId
// createChallenge).
// Public: issues a single-use nonce message for an investor wallet to sign,
// under its own strict per-IP limiter plus a per-address active-challenge cap.
func (app *App) createChallenge(c *gin.Context) {
	if app.Challenges == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "wallet challenges are not configured on this server")
		return
	}
	var body struct {
		Address string `json:"address" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	ch, err := app.Challenges.Create(c.Request.Context(), body.Address)
	if err != nil {
		if errors.Is(err, compliance.ErrTooManyActiveChallenges) {
			// 429, not 400: this is a quota/storage-growth guard, not a
			// malformed-request rejection — the caller
			// should back off and retry once an existing challenge for this
			// address is used or expires, exactly like a rate limit.
			fail(c, http.StatusTooManyRequests, CodeTooManyActiveChallenges, err.Error())
			return
		}
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, dto.ToChallengeResponse(ch))
}

// ChallengeVerifyBody mirrors components.schemas.ChallengeVerify.
type ChallengeVerifyBody struct {
	Address   string `json:"address" binding:"required"`
	Nonce     string `json:"nonce" binding:"required"`
	Signature string `json:"signature" binding:"required"`
}

// verifyChallenge implements POST /api/v1/compliance/challenge/verify
// (operationId verifyChallenge).
// Public: verifies the signed challenge, marks the wallet ownership-verified,
// and mints the subject-scoped X-Wallet-Session bearer returned with the status.
func (app *App) verifyChallenge(c *gin.Context) {
	if app.Challenges == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "wallet challenges are not configured on this server")
		return
	}
	var body ChallengeVerifyBody
	if err := c.ShouldBindJSON(&body); err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	sigBytes, err := hexBytes(body.Signature)
	if err != nil {
		fail(c, http.StatusBadRequest, CodeBadRequest, "signature is not valid hex")
		return
	}

	inv, err := app.Challenges.VerifyOwnership(c.Request.Context(), body.Address, body.Nonce, sigBytes)
	if err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	app.recordAudit(c.Request.Context(), "compliance", caller(c), "compliance.verifyChallenge", body.Address, nil)

	// Proving wallet ownership mints a short-lived, subject-scoped
	// session so the investor page can read ITS OWN status via
	// GET /me/wallet-status without an operator X-API-Key. If no
	// SessionManager is wired (reduced deployment) we still return the
	// verified status — the token fields simply stay empty and the client
	// falls back to re-verifying when it next needs a session.
	result := dto.VerifyChallengeResult{
		Address: inv.Address, Status: string(inv.Status),
		ValidUntil: inv.ValidUntil, OwnershipVerified: inv.OwnershipVerified,
	}
	if app.Sessions != nil {
		token, expiresAt, err := app.Sessions.Issue(c.Request.Context(), inv.Address)
		if err != nil {
			failErr(c, http.StatusInternalServerError, CodeInternal, err)
			return
		}
		result.SessionToken = token
		result.SessionExpiresAt = expiresAt.Format(time.RFC3339)
	}
	c.JSON(http.StatusOK, result)
}

// listWebhookEvents implements GET /api/v1/compliance/webhooks (operationId
// listWebhookEvents).
// Admin-only, offset-paginated page of received KYC webhook deliveries and
// their outbox apply state.
func (app *App) listWebhookEvents(c *gin.Context) {
	events, err := app.Repos.KYCEvents.List(c.Request.Context())
	if err != nil {
		failErr(c, http.StatusInternalServerError, CodeInternal, err)
		return
	}
	offset, limit := paginationParams(c)
	start, end, next := paginateWindow(len(events), offset, limit)
	page := events[start:end]
	out := make([]dto.WebhookEventResponse, len(page))
	for i, e := range page {
		out[i] = dto.ToWebhookEventResponse(e)
	}
	setPaginationHeaders(c, len(events), len(out), next)
	c.JSON(http.StatusOK, out)
}

// listAuditLogs implements GET /api/v1/audit-logs (operationId listAuditLogs).
// Admin-only, offset-paginated page of the operational audit trail, optionally
// filtered by `category`; an unwired audit logger yields an empty list.
func (app *App) listAuditLogs(c *gin.Context) {
	if app.Audit == nil {
		setPaginationHeaders(c, 0, 0, "")
		c.JSON(http.StatusOK, []dto.AuditLogResponse{})
		return
	}
	category := c.Query("category")
	offset, limit := paginationParams(c)
	// Recent's limit is always > 0 and hard-capped (maxAuditLogFetch), never
	// an unbounded 0 that would fetch the whole collection.
	fetchLimit := offset + limit
	if fetchLimit > maxAuditLogFetch {
		fetchLimit = maxAuditLogFetch
	}
	entries, err := app.Audit.Recent(c.Request.Context(), category, fetchLimit)
	if err != nil {
		failErr(c, http.StatusInternalServerError, CodeInternal, err)
		return
	}
	start, end, next := paginateWindow(len(entries), offset, limit)
	// A next cursor only means "there may be more" here (entries was
	// already itself limited to fetchLimit, not the true collection size);
	// suppress it once the fetch itself came back short of fetchLimit,
	// since that means Recent genuinely ran out of matching rows.
	if len(entries) < fetchLimit {
		next = ""
	}
	page := entries[start:end]
	out := make([]dto.AuditLogResponse, len(page))
	for i, e := range page {
		out[i] = dto.ToAuditLogResponse(e)
	}
	setPaginationHeaders(c, -1, len(out), next)
	c.JSON(http.StatusOK, out)
}

// SetStatusRequestBody mirrors components.schemas.SetStatusRequest.
type SetStatusRequestBody struct {
	Address    string `json:"address" binding:"required"`
	Status     string `json:"status" binding:"required"`
	ValidUntil uint64 `json:"validUntil"`
}

// setComplianceStatus implements POST /api/v1/compliance/status (operationId
// setComplianceStatus).
// Admin-only: broadcasts the ComplianceRegistry status change with the server's
// compliance hot key and returns 202 with the submitted TxRef.
func (app *App) setComplianceStatus(c *gin.Context) {
	if app.Status == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "compliance hot key is not configured on this server")
		return
	}
	var body SetStatusRequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	status, ok := compliance.StatusFromString(body.Status)
	if !ok {
		fail(c, http.StatusBadRequest, CodeBadRequest, "status must be one of Unknown, Allowed, Blocked")
		return
	}
	if !common.IsHexAddress(body.Address) {
		fail(c, http.StatusBadRequest, CodeBadRequest, "address is not a valid hex address")
		return
	}

	// Persist a durable audit INTENT BEFORE broadcasting the compliance
	// transaction, and FAIL CLOSED if it cannot be made durable — a Mongo
	// fault must not let a privileged status change go on-chain with no
	// actor/action trail. Recording the audit only AFTER submission (and
	// swallowing a failure) would be fail-open.
	actor := caller(c)
	intentID, err := app.recordAuditIntent(c.Request.Context(), "compliance", actor, "compliance.setStatus", body.Address,
		map[string]any{"status": body.Status, "validUntil": body.ValidUntil})
	if err != nil {
		failErr(c, http.StatusInternalServerError, CodeInternal, err)
		return
	}

	tx, err := app.Status.SetStatus(c.Request.Context(), c.GetHeader("Idempotency-Key"), common.HexToAddress(body.Address), status, body.ValidUntil)
	if err != nil {
		app.recordAuditResult(c.Request.Context(), intentID, "compliance", actor, "compliance.setStatus", body.Address, false, map[string]any{"error": err.Error()})
		failErr(c, http.StatusInternalServerError, CodeInternal, err)
		return
	}

	// The compliance operation write is logged rather than silently
	// discarded. The transaction itself is already durably submitted
	// (app.Status.SetStatus
	// persisted it before this point — see blockchain.TxManager.Submit); this
	// record is a convenience read-model row, so a failure here does not need
	// to fail the request, only be visible.
	if err := app.Repos.ComplianceOperations.Create(c.Request.Context(), &models.ComplianceOperation{
		ID: tx.ID, Address: body.Address, Status: models.ComplianceStatus(body.Status), ValidUntil: int64(body.ValidUntil),
		TxHash: tx.TxHash, Caller: actor, CreatedAt: tx.SubmittedAt,
	}); err != nil {
		log.Printf("api: persist compliance operation record failed (txId=%s address=%s): %v", tx.ID, body.Address, err)
	}
	// Link the completion (with the broadcast txHash) to the intent recorded
	// above.
	app.recordAuditResult(c.Request.Context(), intentID, "compliance", actor, "compliance.setStatus", body.Address, true, map[string]any{"txHash": tx.TxHash})

	c.JSON(http.StatusAccepted, dto.ToTxRef(tx))
}

// kycWebhook implements POST /api/v1/compliance/webhook (operationId
// kycWebhook).
// Authenticated by its own HMAC-SHA256 signature (hex, optionally
// "0x"-prefixed) over the raw body in X-Webhook-Signature, not by a role;
// durably records the decision and returns 202. The header name is a project
// convention; api/openapi.yaml only specifies "HMAC-signed ... webhook".
func (app *App) kycWebhook(c *gin.Context) {
	if app.Webhooks == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "KYC webhook is not configured on this server")
		return
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	sig := c.GetHeader("X-Webhook-Signature")

	wp, err := app.Webhooks.Process(c.Request.Context(), body, sig, false)
	if err != nil {
		switch {
		case errors.Is(err, compliance.ErrInvalidSignature):
			fail(c, http.StatusUnauthorized, CodeUnauthorized, err.Error())
		case errors.Is(err, compliance.ErrReplayed):
			fail(c, http.StatusConflict, CodeConflict, err.Error())
		case errors.Is(err, compliance.ErrStale):
			// 409 (not 400): the payload/signature are valid, this specific
			// delivery is just too old or superseded — same conflict family
			// as ErrReplayed.
			fail(c, http.StatusConflict, CodeConflict, err.Error())
		case errors.Is(err, compliance.ErrOwnershipNotVerified):
			fail(c, http.StatusUnprocessableEntity, CodeBadRequest, err.Error())
		default:
			failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		}
		return
	}

	// Process already durably stored this decision as Accepted (or Recorded,
	// for "Pending") — the on-chain compliance transaction is submitted
	// asynchronously by the WebhookReconciler, never synchronously here.
	// Submitting it here would set the OLD Applied flag true (or, on a
	// wired-Status-but-failed-relay deployment, leave the caller with a 500
	// for a delivery that was already durably accepted and will still be
	// retried) before the transaction was even durable, and would silently
	// no-op with a misleading 204 when app.Status is nil. 202 Accepted
	// reflects the true state: durably queued, not yet applied.
	app.recordAudit(c.Request.Context(), "compliance", "kyc-webhook", "compliance.webhookAccepted", wp.Address, map[string]any{"status": wp.Status})
	c.Status(http.StatusAccepted)
}

// caller identifies the API caller for audit logging without persisting the
// raw credential.
//
// An earlier version hashed X-API-Key ONLY — but a bearer-authenticated
// request (the current SPA's normal path: exchange the operator API key for a
// short-lived session at POST /auth/session, then present ONLY the bearer token
// — see auth.Authenticate) carries no X-API-Key header at all, so every such
// privileged action was logged as "anonymous". It also truncated the digest to
// a 32-bit prefix, weak enough to occasionally collide between two different
// keys in a large-enough audit log. auth.PrincipalFromContext already resolves
// the correct identity for EITHER credential path (set once by
// auth.Authenticate, the same value auth.Idempotency's cache scoping already
// relies on) as the FULL SHA-256 digest — reusing it here also removes a
// second, weaker, redundant hash.
func caller(c *gin.Context) string {
	return auth.PrincipalFromContext(c)
}

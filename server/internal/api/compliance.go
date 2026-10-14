package api

import (
	"context"
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
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/kyc"
	"github.com/rwa-platform/server/internal/metrics"
)

// listWallets serves admin-only wallet/KYC history with repository keyset pagination
func (app *App) listWallets(c *gin.Context) {
	cursor, limit := cursorLimitParams(c)
	page, next, err := app.Repos.Investors.ListPage(c.Request.Context(), cursor, limit)
	if err != nil {
		failInternal(c, err)
		return
	}
	out := make([]dto.WalletStatus, len(page))
	for i, inv := range page {
		out[i] = dto.ToWalletStatus(inv)
	}
	setPaginationHeaders(c, -1, len(out), next)
	c.JSON(http.StatusOK, out)
}

// createChallenge issues a public single-use ownership challenge under per-IP
// rate limits and a per-wallet active-challenge cap
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
			// Return 429 so callers retry after an existing challenge is used or expires
			fail(c, http.StatusTooManyRequests, CodeTooManyActiveChallenges, err.Error())
			return
		}
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, dto.ToChallengeResponse(ch))
}

// ChallengeVerifyBody mirrors components.schemas.ChallengeVerify
type ChallengeVerifyBody struct {
	Address   string `json:"address" binding:"required"`
	Nonce     string `json:"nonce" binding:"required"`
	Signature string `json:"signature" binding:"required"`
}

// verifyChallenge records ownership proof and returns wallet status and a scoped session
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

	// Issue a wallet-scoped session when configured; reduced deployments return status only
	result := dto.VerifyChallengeResult{
		Address: inv.Address, Status: string(inv.Status),
		ValidUntil: inv.ValidUntil, OwnershipVerified: inv.OwnershipVerified,
	}
	if app.Sessions != nil {
		token, expiresAt, err := app.Sessions.Issue(c.Request.Context(), inv.Address)
		if err != nil {
			failInternal(c, err)
			return
		}
		result.SessionToken = token
		result.SessionExpiresAt = expiresAt.Format(time.RFC3339)
	}
	c.JSON(http.StatusOK, result)
}

// listWebhookEvents serves admin-only delivery history and outbox state
func (app *App) listWebhookEvents(c *gin.Context) {
	events, err := app.Repos.KYCEvents.List(c.Request.Context())
	if err != nil {
		failInternal(c, err)
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

// listAuditLogs serves the admin audit trail, optionally filtered by category;
// an unwired logger returns an empty list
func (app *App) listAuditLogs(c *gin.Context) {
	if app.Audit == nil {
		setPaginationHeaders(c, 0, 0, "")
		c.JSON(http.StatusOK, []dto.AuditLogResponse{})
		return
	}
	category := c.Query("category")
	offset, limit := paginationParams(c)
	// Bound the repository fetch even when client pagination asks for later rows
	fetchLimit := offset + limit
	if fetchLimit > maxAuditLogFetch {
		fetchLimit = maxAuditLogFetch
	}
	entries, err := app.Audit.Recent(c.Request.Context(), category, fetchLimit)
	if err != nil {
		failInternal(c, err)
		return
	}
	start, end, next := paginateWindow(len(entries), offset, limit)
	// Offer another cursor only when the bounded fetch might have additional rows
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

// SetStatusRequestBody mirrors components.schemas.SetStatusRequest
type SetStatusRequestBody struct {
	Address    string `json:"address" binding:"required"`
	Status     string `json:"status" binding:"required"`
	ValidUntil uint64 `json:"validUntil"`
}

// setComplianceStatus submits an admin-authorized status write with the server key
// and returns 202 with its TxRef
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

	// Persist intent before broadcast; an audit failure must abort the privileged action
	actor := caller(c)
	intentID, err := app.recordAuditIntent(c.Request.Context(), "compliance", actor, "compliance.setStatus", body.Address,
		map[string]any{"status": body.Status, "validUntil": body.ValidUntil})
	if err != nil {
		failInternal(c, err)
		return
	}

	tx, err := app.Status.SetStatus(c.Request.Context(), c.GetHeader("Idempotency-Key"), common.HexToAddress(body.Address), status, body.ValidUntil)
	if err != nil {
		app.recordAuditResult(c.Request.Context(), intentID, "compliance", actor, "compliance.setStatus", body.Address, false, map[string]any{"error": err.Error()})
		failInternal(c, err)
		return
	}

	// The transaction is already persisted; log a convenience read-model write failure
	// without changing its submission result
	if err := app.Repos.ComplianceOperations.Create(c.Request.Context(), &models.ComplianceOperation{
		ID: tx.ID, Address: body.Address, Status: models.ComplianceStatus(body.Status), ValidUntil: int64(body.ValidUntil),
		TxHash: tx.TxHash, Caller: actor, CreatedAt: tx.SubmittedAt,
	}); err != nil {
		log.Printf("api: persist compliance operation record failed (txId=%s address=%s): %v", tx.ID, body.Address, err)
	}
	// Link the broadcast result to the recorded intent
	app.recordAuditResult(c.Request.Context(), intentID, "compliance", actor, "compliance.setStatus", body.Address, true, map[string]any{"txHash": tx.TxHash})

	c.JSON(http.StatusAccepted, dto.ToTxRef(tx))
}

// startKYC starts verification for the session wallet without changing on-chain status
func (app *App) startKYC(c *gin.Context) {
	if app.KYC == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "KYC provider is not configured on this server")
		return
	}
	address, ok := auth.WalletSessionAddress(c)
	if !ok {
		// Reject a missing session even if middleware wiring changes
		fail(c, http.StatusUnauthorized, CodeUnauthorized, "no wallet session")
		return
	}
	sess, err := app.KYC.StartVerification(c.Request.Context(), address)
	if err != nil {
		if errors.Is(err, kyc.ErrStartNotSupported) {
			fail(c, http.StatusNotImplemented, CodeNotImplemented, "the configured KYC provider does not support server-initiated verification")
			return
		}
		// Map upstream verification failure to bad gateway
		failErr(c, http.StatusBadGateway, CodeInternal, err)
		return
	}
	// Persist the provider reference for webhook resolution and audit history;
	// Sumsub includes the wallet directly, while Onfido requires this binding
	if sess.Ref != "" {
		if err := app.Repos.KYCVerifications.Upsert(c.Request.Context(), &models.KYCVerification{
			Provider: app.KYC.Name(), Ref: sess.Ref, Address: address, CreatedAt: time.Now().UTC(),
		}); err != nil {
			failInternal(c, err)
			return
		}
	}
	app.recordAudit(c.Request.Context(), "compliance", address, "compliance.kycStart", address, map[string]any{"provider": sess.Provider})
	c.JSON(http.StatusOK, dto.KYCSession{
		Provider: sess.Provider, Token: sess.Token, URL: sess.URL, Ref: sess.Ref,
		ExpiresAt: kycExpiresAt(sess.ExpiresAt),
	})
}

func kycExpiresAt(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// kycWebhook authenticates the configured provider's signature over the raw body.
// Decisions enter shared replay, freshness, ownership and outbox checks;
// walletless monitoring signals are handled separately without a status relay.
func (app *App) kycWebhook(c *gin.Context) {
	if app.KYC == nil || app.Webhooks == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "KYC webhook is not configured on this server")
		return
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}

	dec, err := app.KYC.VerifyWebhook(body, c.Request.Header)
	if err != nil {
		switch {
		case errors.Is(err, kyc.ErrInvalidSignature):
			fail(c, http.StatusUnauthorized, CodeUnauthorized, err.Error())
		case errors.Is(err, kyc.ErrUnhandledEvent):
			// Acknowledge irrelevant signed events without recording them
			c.Status(http.StatusAccepted)
		case errors.Is(err, kyc.ErrMonitoringSignal):
			app.alertKYCMonitoringHit(c.Request.Context(), "", err.Error())
			c.Status(http.StatusAccepted)
		case errors.Is(err, kyc.ErrMissingTimestamp):
			// Drop and audit decisions with no usable timestamp; return 202 because
			// redelivery cannot repair the missing ordering information
			app.recordAudit(c.Request.Context(), "compliance", "kyc-webhook", "compliance.webhookDroppedNoTimestamp", "",
				map[string]any{"provider": app.KYC.Name(), "reason": err.Error()})
			log.Printf("api: kyc webhook dropped, provider sent no usable decision timestamp (provider=%s): %v", app.KYC.Name(), err)
			c.Status(http.StatusAccepted)
		default:
			failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		}
		return
	}

	// Use the embedded wallet or resolve the provider reference recorded at verification start
	address := dec.Address
	if address == "" {
		v, gerr := app.Repos.KYCVerifications.GetByRef(c.Request.Context(), dec.Provider, dec.Ref)
		if gerr != nil {
			if errors.Is(gerr, repository.ErrNotFound) {
				// Reject an unknown verification without applying its decision
				fail(c, http.StatusUnprocessableEntity, CodeBadRequest, "kyc webhook references an unknown verification")
				return
			}
			failInternal(c, gerr)
			return
		}
		address = v.Address
	}

	wp := compliance.WebhookPayload{
		EventID: dec.EventID, Address: address, Provider: dec.Provider,
		Status: dec.Status, OccurredAt: dec.OccurredAt, ValidUntil: dec.ValidUntil,
	}
	// Persist the inbox/outbox decision before acknowledging it; the reconciler
	// relays actionable statuses asynchronously, so 202 does not mean applied on-chain
	if _, perr := app.Webhooks.ProcessDecision(c.Request.Context(), body, wp, false); perr != nil {
		switch {
		case errors.Is(perr, compliance.ErrReplayed):
			fail(c, http.StatusConflict, CodeConflict, perr.Error())
		case errors.Is(perr, compliance.ErrStale):
			// Return conflict for an old or superseded decision
			fail(c, http.StatusConflict, CodeConflict, perr.Error())
		case errors.Is(perr, compliance.ErrOwnershipNotVerified):
			fail(c, http.StatusUnprocessableEntity, CodeBadRequest, perr.Error())
		default:
			failErr(c, http.StatusBadRequest, CodeBadRequest, perr)
		}
		return
	}

	app.recordAudit(c.Request.Context(), "compliance", "kyc-webhook", "compliance.webhookAccepted", wp.Address, map[string]any{"status": wp.Status, "provider": wp.Provider})
	// Alert after deduplicated acceptance only when the projected wallet is still Allowed
	if dec.MonitoringHit != "" {
		if inv, err := app.Repos.Investors.Get(c.Request.Context(), address); err == nil && investorAllowed(inv, time.Now()) {
			app.alertKYCMonitoringHit(c.Request.Context(), address, dec.MonitoringHit)
		}
	}
	c.Status(http.StatusAccepted)
}

// investorAllowed checks Allowed status and expiry in the read model
func investorAllowed(inv *models.Investor, now time.Time) bool {
	return inv.Status == models.ComplianceAllowed && (inv.ValidUntil == 0 || inv.ValidUntil >= now.Unix())
}

// alertKYCMonitoringHit logs and counts a monitoring signal and attempts an audit append;
// address is empty for walletless deliveries
func (app *App) alertKYCMonitoringHit(ctx context.Context, address, detail string) {
	log.Printf("api: ALERT [kyc_monitoring_hit] wallet=%q: %s", address, detail)
	metrics.AlertsFiredTotal.WithLabelValues("kyc_monitoring_hit").Inc()
	app.recordAudit(ctx, "alerts", "kyc-webhook", "alerts.kyc_monitoring_hit", address, map[string]any{"detail": detail})
}

// caller reuses the authenticated principal for audit attribution, matching idempotency scope
func caller(c *gin.Context) string {
	return auth.PrincipalFromContext(c)
}

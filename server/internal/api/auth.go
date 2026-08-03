package api

import (
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/auth"
)

// createAuthChallenge implements POST /auth/challenge (operationId
// createAuthChallenge).
// Public step 1 of admin wallet login: issues a single-use, time-limited
// message for the connected wallet to personal_sign. Anyone may request one —
// only createSession's signer-recovery + admin-address comparison decides
// authorization, so this deliberately does not reveal whether the address is
// the admin.
func (app *App) createAuthChallenge(c *gin.Context) {
	if app.AdminChallenges == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "admin auth is not configured on this server")
		return
	}
	var body struct {
		Address string `json:"address" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	message, expiresAt, err := app.AdminChallenges.Create(c.Request.Context(), body.Address)
	if err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": message, "expiresAt": expiresAt.Unix()})
}

// createSession implements POST /auth/session (operationId createSession).
// Public step 2 of admin wallet login: recovers the signer of the personal_sign
// signature over the challenge message and, if it equals the configured admin
// address, issues a stateless HMAC JWT. There is no DELETE counterpart — the
// JWT is stateless, so logout is client-side (drop the token).
func (app *App) createSession(c *gin.Context) {
	if app.AdminChallenges == nil || len(app.JWTSecret) == 0 || app.AdminAddress == "" {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "admin auth is not configured on this server")
		return
	}
	var body struct {
		Address   string `json:"address" binding:"required"`
		Signature string `json:"signature" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	sigBytes, err := hexBytes(body.Signature)
	if err != nil {
		fail(c, http.StatusBadRequest, CodeBadRequest, "signature is not valid hex")
		return
	}

	// Verify enforces single-use + expiry and recovers the signer. Any failure
	// here — unknown/expired challenge, replay, bad signature, address mismatch,
	// or a signer that is simply not the admin — collapses to one opaque 401 so
	// a login probe never learns which case it hit.
	recovered, err := app.AdminChallenges.Verify(c.Request.Context(), body.Address, sigBytes)
	if err != nil || !strings.EqualFold(recovered.Hex(), common.HexToAddress(app.AdminAddress).Hex()) {
		fail(c, http.StatusUnauthorized, CodeUnauthorized, "invalid credentials")
		return
	}

	adminAddr := common.HexToAddress(app.AdminAddress).Hex()
	token, expiresAt, err := auth.IssueAdminJWT(app.JWTSecret, adminAddr, app.JWTTTL)
	if err != nil {
		failInternal(c, err)
		return
	}
	// Attribute the admin login in the operational audit trail (a security
	// event worth recording); best-effort, never blocks issuing the token.
	app.recordAudit(c.Request.Context(), "auth", adminAddr, "auth.login", adminAddr, nil)
	c.JSON(http.StatusOK, dto.AuthSession{
		Token: token, ExpiresAt: expiresAt.Unix(), Role: string(auth.RoleAdmin), Address: adminAddr,
	})
}

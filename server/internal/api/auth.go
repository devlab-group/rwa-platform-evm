package api

import (
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/auth"
)

// createAuthChallenge handles public POST /auth/challenge, issuing a single-use,
// expiring personal_sign message. Anyone may request one without revealing admin
// status; createSession authorizes the recovered signer.
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
	nonce, message, expiresAt, err := app.AdminChallenges.Create(c.Request.Context(), body.Address)
	if err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"address": common.HexToAddress(body.Address).Hex(), "nonce": nonce,
		"message": message, "expiresAt": expiresAt.Unix(),
	})
}

// createSession handles public POST /auth/session, exchanging a challenge signature
// from the configured admin for an HS256 JWT. Logout discards the stateless token on
// the client; there is no DELETE endpoint.
func (app *App) createSession(c *gin.Context) {
	if app.AdminChallenges == nil || len(app.JWTSecret) == 0 || app.AdminAddress == "" {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "admin auth is not configured on this server")
		return
	}
	var body struct {
		Address   string `json:"address" binding:"required"`
		Nonce     string `json:"nonce" binding:"required"`
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

	// Verify checks expiry, single use and the signer. Unknown or expired challenges,
	// replays, bad signatures, address mismatches and non-admin signers all receive the
	// same 401.
	recovered, err := app.AdminChallenges.Verify(c.Request.Context(), body.Address, body.Nonce, sigBytes)
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
	// Record the login in the audit trail; best effort, never blocks the token
	app.recordAudit(c.Request.Context(), "auth", adminAddr, "auth.login", adminAddr, nil)
	c.JSON(http.StatusOK, dto.AuthSession{
		Token: token, ExpiresAt: expiresAt.Unix(), Role: string(auth.RoleAdmin), Address: adminAddr,
	})
}

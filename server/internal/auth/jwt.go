package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// adminJWTRole is the single role V1 issues (single-admin model): the one
// configured admin wallet is admin everywhere. Carried as a claim so a token
// minted for anything else (were the issuer ever extended) is rejected by
// ParseAdminJWT rather than silently trusted as admin.
const adminJWTRole = "admin"

// adminClaims is the admin JWT payload: the standard registered claims (we
// set Subject=admin address, IssuedAt, ExpiresAt) plus an explicit Role so a
// token's authority is self-describing and verified, not inferred.
type adminClaims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// IssueAdminJWT mints an HMAC-SHA256 JWT for the admin wallet `address`,
// signed with `secret`, expiring after `ttl`. Claims: sub=address,
// role="admin", iat=now, exp=now+ttl. Returns the compact token and its
// absolute expiry (the same value the API surfaces as AuthSession.expiresAt).
func IssueAdminJWT(secret []byte, address string, ttl time.Duration) (token string, expiresAt time.Time, err error) {
	if len(secret) == 0 {
		return "", time.Time{}, errors.New("auth: JWT secret is not configured")
	}
	now := time.Now().UTC()
	expiresAt = now.Add(ttl)
	claims := adminClaims{
		Role: adminJWTRole,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   address,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: signing admin JWT: %w", err)
	}
	return signed, expiresAt, nil
}

// ParseAdminJWT verifies token against secret and returns the admin address
// (the `sub` claim) for a valid, unexpired, HMAC-SHA256 admin token. It
// rejects any other signing method (so an `alg:none` or asymmetric-key
// confusion attack can't forge a token), a wrong signature, an expired token,
// or a token whose role is not "admin". The registered exp/iat claims are
// enforced by the parser (see the WithExpirationRequired option).
func ParseAdminJWT(secret []byte, token string) (address string, err error) {
	if len(secret) == 0 {
		return "", errors.New("auth: JWT secret is not configured")
	}
	var claims adminClaims
	_, err = jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("auth: unexpected JWT signing method %q", t.Header["alg"])
		}
		return secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	if err != nil {
		return "", err
	}
	if claims.Role != adminJWTRole {
		return "", fmt.Errorf("auth: JWT role %q is not %q", claims.Role, adminJWTRole)
	}
	if claims.Subject == "" {
		return "", errors.New("auth: JWT has no subject")
	}
	return claims.Subject, nil
}

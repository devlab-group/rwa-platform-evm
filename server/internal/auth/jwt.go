package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// adminJWTRole is the only role issued: the single configured admin wallet.
// It is carried as a claim so ParseAdminJWT rejects a token minted with any
// other role
const adminJWTRole = "admin"

// adminClaims is the admin JWT payload: the registered claims sub (admin
// address), iat and exp, plus a role claim
type adminClaims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// IssueAdminJWT returns a compact HS256 JWT for the admin wallet address,
// signed with secret and expiring after ttl, together with its expiry time
// (surfaced by the API as AuthSession.expiresAt). Claims are sub=address,
// role="admin", iat=now and exp=now+ttl.
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
// from its sub claim. It rejects any signing method other than HS256 (which
// rules out alg "none" and asymmetric-key confusion), a bad signature, a token
// with a missing or past exp, a role other than "admin", and an empty sub. The
// iat claim is not validated.
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

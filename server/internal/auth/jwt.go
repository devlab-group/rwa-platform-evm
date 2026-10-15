package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// adminJWTRole is the configured admin wallet's sole issued role. ParseAdminJWT rejects
// other role claims.
const adminJWTRole = "admin"

// adminClaims is the admin JWT payload: the registered claims sub (admin
// address), iat and exp, plus a role claim
type adminClaims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// IssueAdminJWT signs an HS256 token with secret and returns its expiry, exposed by the
// API as AuthSession.expiresAt. Claims are sub=address, role="admin", iat=now and
// exp=now+ttl.
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

// ParseAdminJWT returns sub after requiring HS256, a valid signature, a present
// unexpired exp, role="admin" and non-empty sub. It rejects alg="none" and
// asymmetric-key methods; iat is unchecked.
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

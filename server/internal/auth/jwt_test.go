package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const jwtTestAdmin = "0x1111111111111111111111111111111111111111"

// Fixed far-past and far-future instants keep these tests independent of the clock
var (
	jwtLongAgo = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	jwtFarOut  = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
)

func signJWT(t *testing.T, method jwt.SigningMethod, key any, claims jwt.Claims) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatalf("sign test JWT: %v", err)
	}
	return tok
}

func adminJWTClaims(role, sub string, exp time.Time) adminClaims {
	rc := jwt.RegisteredClaims{Subject: sub, IssuedAt: jwt.NewNumericDate(jwtLongAgo)}
	if !exp.IsZero() {
		rc.ExpiresAt = jwt.NewNumericDate(exp)
	}
	return adminClaims{Role: role, RegisteredClaims: rc}
}

func TestParseAdminJWT(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	good := adminJWTClaims("admin", jwtTestAdmin, jwtFarOut)
	otherSecret := []byte("some-other-secret-that-is-32-bytes-long!!")

	tests := []struct {
		name    string
		token   string
		want    string
		wantErr error  // matched with errors.Is when set
		wantMsg string // substring of the error when wantErr is nil
	}{
		{name: "valid admin token returns the subject", token: signJWT(t, jwt.SigningMethodHS256, testJWTSecret, good), want: jwtTestAdmin},
		{name: "expired token", token: signJWT(t, jwt.SigningMethodHS256, testJWTSecret, adminJWTClaims("admin", jwtTestAdmin, jwtLongAgo.Add(time.Hour))), wantErr: jwt.ErrTokenExpired},
		{name: "missing exp", token: signJWT(t, jwt.SigningMethodHS256, testJWTSecret, adminJWTClaims("admin", jwtTestAdmin, time.Time{})), wantErr: jwt.ErrTokenRequiredClaimMissing},
		{name: "wrong secret", token: signJWT(t, jwt.SigningMethodHS256, otherSecret, good), wantErr: jwt.ErrTokenSignatureInvalid},
		{name: "alg none", token: signJWT(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, good), wantErr: jwt.ErrTokenSignatureInvalid},
		{name: "HS384 with the right secret", token: signJWT(t, jwt.SigningMethodHS384, testJWTSecret, good), wantErr: jwt.ErrTokenSignatureInvalid},
		{name: "HS512 with the right secret", token: signJWT(t, jwt.SigningMethodHS512, testJWTSecret, good), wantErr: jwt.ErrTokenSignatureInvalid},
		{name: "RS256 token", token: signJWT(t, jwt.SigningMethodRS256, rsaKey, good), wantErr: jwt.ErrTokenSignatureInvalid},
		{name: "non-admin role", token: signJWT(t, jwt.SigningMethodHS256, testJWTSecret, adminJWTClaims("readonly", jwtTestAdmin, jwtFarOut)), wantMsg: `JWT role "readonly" is not "admin"`},
		{name: "missing role", token: signJWT(t, jwt.SigningMethodHS256, testJWTSecret, adminJWTClaims("", jwtTestAdmin, jwtFarOut)), wantMsg: `JWT role "" is not "admin"`},
		{name: "empty subject", token: signJWT(t, jwt.SigningMethodHS256, testJWTSecret, adminJWTClaims("admin", "", jwtFarOut)), wantMsg: "JWT has no subject"},
		{name: "not a JWT", token: "not-a-jwt", wantErr: jwt.ErrTokenMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseAdminJWT(testJWTSecret, tt.token)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ParseAdminJWT error = %v, want %v", err, tt.wantErr)
				}
			case tt.wantMsg != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
					t.Fatalf("ParseAdminJWT error = %v, want it to contain %q", err, tt.wantMsg)
				}
			default:
				if err != nil {
					t.Fatalf("ParseAdminJWT: %v", err)
				}
				if got != tt.want {
					t.Errorf("ParseAdminJWT = %q, want %q", got, tt.want)
				}
			}
			if err != nil && got != "" {
				t.Errorf("ParseAdminJWT returned %q alongside error %v", got, err)
			}
		})
	}
}

func TestParseAdminJWT_EmptySecretIsRejected(t *testing.T) {
	tok := signJWT(t, jwt.SigningMethodHS256, []byte{}, adminJWTClaims("admin", jwtTestAdmin, jwtFarOut))
	if _, err := ParseAdminJWT(nil, tok); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("ParseAdminJWT with no secret error = %v, want a not-configured error", err)
	}
}

// Every rejected token must leave the admin route forbidden (403) through the middleware
func TestAuthenticate_RejectedTokensAreForbidden(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	good := adminJWTClaims("admin", jwtTestAdmin, jwtFarOut)
	tests := []struct {
		name  string
		token string
		want  int
	}{
		{"valid", signJWT(t, jwt.SigningMethodHS256, testJWTSecret, good), http.StatusOK},
		{"expired", signJWT(t, jwt.SigningMethodHS256, testJWTSecret, adminJWTClaims("admin", jwtTestAdmin, jwtLongAgo.Add(time.Hour))), http.StatusForbidden},
		{"missing exp", signJWT(t, jwt.SigningMethodHS256, testJWTSecret, adminJWTClaims("admin", jwtTestAdmin, time.Time{})), http.StatusForbidden},
		{"alg none", signJWT(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, good), http.StatusForbidden},
		{"HS512", signJWT(t, jwt.SigningMethodHS512, testJWTSecret, good), http.StatusForbidden},
		{"RS256", signJWT(t, jwt.SigningMethodRS256, rsaKey, good), http.StatusForbidden},
		{"non-admin role", signJWT(t, jwt.SigningMethodHS256, testJWTSecret, adminJWTClaims("readonly", jwtTestAdmin, jwtFarOut)), http.StatusForbidden},
		{"another subject", signJWT(t, jwt.SigningMethodHS256, testJWTSecret, adminJWTClaims("admin", "0x3333333333333333333333333333333333333333", jwtFarOut)), http.StatusForbidden},
	}
	r := gin.New()
	r.Use(Authenticate(testJWTSecret, jwtTestAdmin))
	r.GET("/x", RequireRole(RoleAdmin), func(c *gin.Context) { c.Status(http.StatusOK) })
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("Authorization", "Bearer "+tt.token)
			r.ServeHTTP(w, req)
			if w.Code != tt.want {
				t.Errorf("status = %d, want %d", w.Code, tt.want)
			}
		})
	}
}

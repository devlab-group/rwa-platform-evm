package dto

// AuthSession mirrors components.schemas.AuthSession: the admin JWT and its
// metadata returned by POST /auth/session. createSession builds it from the
// issued token; no model backs it.
type AuthSession struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
	Role      string `json:"role"`
	Address   string `json:"address"`
}

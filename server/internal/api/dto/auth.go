package dto

// AuthSession mirrors components.schemas.AuthSession: the admin JWT and its
// metadata returned by POST /auth/session. Built inline by createSession from
// the freshly issued token — there is no model behind it.
type AuthSession struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
	Role      string `json:"role"`
	Address   string `json:"address"`
}

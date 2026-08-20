package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// readSubject decodes a JWT payload for display in `leaflow auth status`.
//
// It does not verify the signature and its result must not drive any decision:
// verification belongs to the services. Without it, status could only print a
// base64 blob.
func readSubject(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}

	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
		Subject           string `json:"sub"`
	}

	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}

	switch {
	case claims.Email != "":
		return claims.Email
	case claims.PreferredUsername != "":
		return claims.PreferredUsername
	default:
		return claims.Subject
	}
}

// isOfflineToken reports whether a refresh token is an offline token.
//
// Keycloak marks these with typ "Offline". Read for display only — the realm is
// what enforces the distinction, and this side just says which one is held.
func isOfflineToken(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}

	var claims struct {
		Type string `json:"typ"`
	}

	if err := json.Unmarshal(payload, &claims); err != nil {
		return false
	}

	return strings.EqualFold(claims.Type, "Offline")
}

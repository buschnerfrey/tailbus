package chat

import (
	"net/http"
	"strings"
)

// withAuth wraps an HTTP handler with mesh token authentication.
// The token is checked from the Authorization header ("Bearer <token>"),
// a "mesh_token" cookie, or the "token" query parameter (for WebSocket).
func (g *Gateway) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If no mesh token is configured, auth is disabled (local dev)
		if g.meshToken == "" {
			next(w, r)
			return
		}

		token := extractToken(r)
		if token != g.meshToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// checkAuth validates the mesh token from the request.
// Returns true if auth passes (or is disabled).
func (g *Gateway) checkAuth(r *http.Request) bool {
	if g.meshToken == "" {
		return true
	}
	return extractToken(r) == g.meshToken
}

func extractToken(r *http.Request) string {
	// 1. Authorization header
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimPrefix(auth, "Bearer ")
		}
	}

	// 2. Cookie
	if c, err := r.Cookie("mesh_token"); err == nil {
		return c.Value
	}

	// 3. Query parameter (for WebSocket upgrade)
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}

	return ""
}

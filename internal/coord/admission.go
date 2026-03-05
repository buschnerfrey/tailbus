package coord

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
)

// AdmissionResult holds information from a successful admission check.
type AdmissionResult struct {
	Email  string // non-empty if authenticated via JWT
	TeamID string // non-empty if JWT contains team scope
}

// Admission controls which nodes are allowed to register with the coord server.
// In open mode (no tokens configured), all registrations are allowed.
// In closed mode (any tokens exist), a valid auth token or JWT is required.
type Admission struct {
	store  *Store
	jwt    *JWTIssuer
	logger *slog.Logger
}

// NewAdmission creates a new admission controller.
func NewAdmission(store *Store, logger *slog.Logger) *Admission {
	return &Admission{store: store, logger: logger}
}

// SetJWT configures JWT validation for the admission controller.
func (a *Admission) SetJWT(issuer *JWTIssuer) {
	a.jwt = issuer
}

// HashToken returns the SHA-256 hex digest of a raw token string.
func HashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// SeedToken hashes and inserts a token if it doesn't already exist.
// Used at startup to seed tokens from config/flags.
func (a *Admission) SeedToken(name, raw string, singleUse bool) error {
	hash := HashToken(raw)
	err := a.store.InsertAuthToken(name, hash, singleUse, nil)
	if err != nil {
		// Ignore duplicate insert (name or hash already exists)
		a.logger.Debug("seed token insert (may already exist)", "name", name, "error", err)
		return nil
	}
	a.logger.Info("auth token seeded", "name", name, "single_use", singleUse)
	return nil
}

// ValidateRegistration checks whether a registration should be allowed.
// JWT path: if token starts with "eyJ", validate as JWT, extract email, bind node to user.
// Pre-shared token path: hash and check against the auth_tokens table.
// Open mode: if no tokens exist in the DB and no JWT issuer configured, allow everyone.
func (a *Admission) ValidateRegistration(authToken, nodeID string) (*AdmissionResult, error) {
	// JWT path: tokens starting with "eyJ" are JWTs
	if a.jwt != nil && strings.HasPrefix(authToken, "eyJ") {
		claims, err := a.jwt.Validate(authToken)
		if err != nil {
			return nil, fmt.Errorf("JWT validation failed: %w", err)
		}
		if claims.TokenType != "access" {
			return nil, fmt.Errorf("expected access token, got %s", claims.TokenType)
		}

		// Upsert user + bind node + ensure personal team
		if err := a.store.UpsertUser(claims.Email); err != nil {
			a.logger.Error("failed to upsert user", "email", claims.Email, "error", err)
		}
		if err := a.store.BindNodeToUser(nodeID, claims.Email); err != nil {
			a.logger.Error("failed to bind node to user", "node_id", nodeID, "email", claims.Email, "error", err)
		}
		if teamID, teamName, err := a.store.EnsurePersonalTeam(claims.Email); err != nil {
			a.logger.Error("failed to ensure personal team", "email", claims.Email, "error", err)
		} else if teamID != "" {
			a.logger.Info("personal team created", "email", claims.Email, "team_id", teamID, "name", teamName)
		}

		a.logger.Info("node admitted via JWT", "node_id", nodeID, "email", claims.Email, "team_id", claims.TeamID)
		return &AdmissionResult{Email: claims.Email, TeamID: claims.TeamID}, nil
	}

	// Pre-shared token path
	hasTokens, err := a.store.HasAuthTokens()
	if err != nil {
		return nil, fmt.Errorf("check auth tokens: %w", err)
	}

	// Open mode — no tokens configured and no OAuth, allow everyone
	if !hasTokens && a.jwt == nil {
		return &AdmissionResult{}, nil
	}

	// Closed mode — token required
	if authToken == "" {
		return nil, fmt.Errorf("auth token required (coord has admission tokens or OAuth configured)")
	}

	hash := HashToken(authToken)
	if err := a.store.ValidateAndConsumeToken(hash, nodeID); err != nil {
		return nil, fmt.Errorf("auth token rejected: %w", err)
	}

	a.logger.Info("node admitted via auth token", "node_id", nodeID)
	return &AdmissionResult{}, nil
}

package coord

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

//go:embed oauth_web/verify.html
var verifyHTML embed.FS

const (
	deviceCodeTTL    = 15 * time.Minute
	deviceCodeLength = 8
	userCodeLength   = 8
	pollInterval     = 5 // seconds
)

// OAuthConfig holds configuration for the OAuth device flow server.
type OAuthConfig struct {
	Providers   []OAuthProviderConfig
	ExternalURL string // e.g. "https://coord.tailbus.co" — base URL for callbacks
	WebAppURL   string // e.g. "https://tailbus.co" — redirect target for browser OAuth
}

// OAuthProviderConfig holds OIDC provider configuration.
type OAuthProviderConfig struct {
	Name         string
	Issuer       string
	ClientID     string
	ClientSecret string
}

// deviceCode represents an in-flight device authorization request.
type deviceCode struct {
	DeviceCode string
	UserCode   string
	ExpiresAt  time.Time
	Interval   int

	// Set after user verifies
	Email        string
	AccessToken  string
	RefreshToken string
	Completed    bool
	OAuthState   string // CSRF state for the OAuth redirect
}

// browserState tracks an in-flight browser OAuth login.
type browserState struct {
	State     string
	ExpiresAt time.Time
}

// OAuthServer implements the RFC 8628 device authorization flow and browser OAuth.
type OAuthServer struct {
	issuer      *JWTIssuer
	providers   map[string]*oauthProvider
	externalURL string
	webAppURL   string
	logger      *slog.Logger

	mu            sync.Mutex
	codes         map[string]*deviceCode   // keyed by device_code
	browserStates map[string]*browserState // keyed by state
}

type oauthProvider struct {
	name     string
	config   oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewOAuthServer creates a new OAuth server with the given providers.
func NewOAuthServer(cfg *OAuthConfig, issuer *JWTIssuer, logger *slog.Logger) (*OAuthServer, error) {
	webAppURL := strings.TrimRight(cfg.WebAppURL, "/")
	if webAppURL == "" {
		webAppURL = "https://tailbus.co"
	}

	s := &OAuthServer{
		issuer:        issuer,
		providers:     make(map[string]*oauthProvider),
		externalURL:   strings.TrimRight(cfg.ExternalURL, "/"),
		webAppURL:     webAppURL,
		logger:        logger,
		codes:         make(map[string]*deviceCode),
		browserStates: make(map[string]*browserState),
	}

	ctx := context.Background()
	for _, p := range cfg.Providers {
		provider, err := oidc.NewProvider(ctx, p.Issuer)
		if err != nil {
			return nil, fmt.Errorf("OIDC discovery for %s: %w", p.Name, err)
		}

		oauthCfg := oauth2.Config{
			ClientID:     p.ClientID,
			ClientSecret: p.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  s.externalURL + "/oauth/callback",
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		}

		verifier := provider.Verifier(&oidc.Config{ClientID: p.ClientID})

		s.providers[p.Name] = &oauthProvider{
			name:     p.Name,
			config:   oauthCfg,
			verifier: verifier,
		}
	}

	return s, nil
}

// StartCleanup starts a goroutine that removes expired device codes.
func (s *OAuthServer) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.mu.Lock()
				now := time.Now()
				for k, dc := range s.codes {
					if now.After(dc.ExpiresAt) {
						delete(s.codes, k)
					}
				}
				for k, bs := range s.browserStates {
					if now.After(bs.ExpiresAt) {
						delete(s.browserStates, k)
					}
				}
				s.mu.Unlock()
			}
		}
	}()
}

// Handler returns an http.Handler with all OAuth routes.
func (s *OAuthServer) Handler() http.Handler {
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	return mux
}

// RegisterRoutes adds OAuth routes to an existing mux.
func (s *OAuthServer) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /oauth/device/code", s.handleDeviceCode)
	mux.HandleFunc("POST /oauth/device/token", s.handleDeviceToken)
	mux.HandleFunc("GET /oauth/verify", s.handleVerifyPage)
	mux.HandleFunc("POST /oauth/verify", s.handleVerifySubmit)
	mux.HandleFunc("GET /oauth/callback", s.handleCallback)
	mux.HandleFunc("POST /oauth/refresh", s.handleRefresh)
	mux.HandleFunc("GET /oauth/login", s.handleBrowserLogin)
	mux.HandleFunc("GET /oauth/callback/web", s.handleBrowserCallback)
}

// handleDeviceCode implements POST /oauth/device/code — starts the device flow.
func (s *OAuthServer) handleDeviceCode(w http.ResponseWriter, r *http.Request) {
	deviceCodeStr, err := randomHex(16)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed to generate device code")
		return
	}
	userCode, err := randomUserCode(userCodeLength)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed to generate user code")
		return
	}

	dc := &deviceCode{
		DeviceCode: deviceCodeStr,
		UserCode:   userCode,
		ExpiresAt:  time.Now().Add(deviceCodeTTL),
		Interval:   pollInterval,
	}

	s.mu.Lock()
	s.codes[deviceCodeStr] = dc
	s.mu.Unlock()

	verificationURI := s.externalURL + "/oauth/verify"

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"device_code":               dc.DeviceCode,
		"user_code":                 formatUserCode(dc.UserCode),
		"verification_uri":          verificationURI,
		"verification_uri_complete": verificationURI + "?code=" + dc.UserCode,
		"expires_in":                int(deviceCodeTTL.Seconds()),
		"interval":                  dc.Interval,
	})

	s.logger.Info("device code issued", "user_code", formatUserCode(dc.UserCode))
}

// handleDeviceToken implements POST /oauth/device/token — polls for completion.
func (s *OAuthServer) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	s.mu.Lock()
	dc, ok := s.codes[req.DeviceCode]
	s.mu.Unlock()

	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_device_code"})
		return
	}

	if time.Now().After(dc.ExpiresAt) {
		s.mu.Lock()
		delete(s.codes, req.DeviceCode)
		s.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expired_token"})
		return
	}

	if !dc.Completed {
		writeJSON(w, http.StatusOK, map[string]string{"error": "authorization_pending"})
		return
	}

	// Device code is complete — return tokens
	s.mu.Lock()
	delete(s.codes, req.DeviceCode)
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token":  dc.AccessToken,
		"refresh_token": dc.RefreshToken,
		"token_type":    "Bearer",
		"expires_in":    int(accessTokenDuration.Seconds()),
		"email":         dc.Email,
	})
}

// handleVerifyPage serves the verification HTML page.
func (s *OAuthServer) handleVerifyPage(w http.ResponseWriter, r *http.Request) {
	data, err := verifyHTML.ReadFile("oauth_web/verify.html")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// handleVerifySubmit handles POST /oauth/verify — user submits their code.
func (s *OAuthServer) handleVerifySubmit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserCode string `json:"user_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid request")
		return
	}

	userCode := strings.ReplaceAll(strings.ToUpper(req.UserCode), "-", "")

	s.mu.Lock()
	var foundDC *deviceCode
	for _, dc := range s.codes {
		if dc.UserCode == userCode && !dc.Completed {
			foundDC = dc
			break
		}
	}
	s.mu.Unlock()

	if foundDC == nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": "Invalid or expired code. Please check and try again."})
		return
	}

	// Generate OAuth state and store it
	state, err := randomHex(16)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "internal error")
		return
	}

	s.mu.Lock()
	foundDC.OAuthState = state
	s.mu.Unlock()

	// Find the first provider (Google)
	var authURL string
	for _, p := range s.providers {
		authURL = p.config.AuthCodeURL(state, oauth2.AccessTypeOffline)
		break
	}

	if authURL == "" {
		httpError(w, http.StatusInternalServerError, "no OAuth provider configured")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"auth_url": authURL})
}

// handleCallback handles GET /oauth/callback — Google redirects here after consent.
func (s *OAuthServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if code == "" || state == "" {
		http.Error(w, "missing code or state parameter", http.StatusBadRequest)
		return
	}

	// Find the device code with this state
	s.mu.Lock()
	var foundDC *deviceCode
	for _, dc := range s.codes {
		if dc.OAuthState == state && !dc.Completed {
			foundDC = dc
			break
		}
	}
	s.mu.Unlock()

	if foundDC == nil {
		http.Error(w, "invalid or expired session", http.StatusBadRequest)
		return
	}

	// Exchange the authorization code for tokens with Google
	var email string
	for _, p := range s.providers {
		tok, err := p.config.Exchange(r.Context(), code)
		if err != nil {
			s.logger.Error("OAuth token exchange failed", "error", err)
			http.Error(w, "authentication failed", http.StatusInternalServerError)
			return
		}

		rawIDToken, ok := tok.Extra("id_token").(string)
		if !ok {
			http.Error(w, "no id_token in response", http.StatusInternalServerError)
			return
		}

		idToken, err := p.verifier.Verify(r.Context(), rawIDToken)
		if err != nil {
			s.logger.Error("ID token verification failed", "error", err)
			http.Error(w, "token verification failed", http.StatusInternalServerError)
			return
		}

		var claims struct {
			Email string `json:"email"`
		}
		if err := idToken.Claims(&claims); err != nil {
			http.Error(w, "failed to parse claims", http.StatusInternalServerError)
			return
		}
		email = claims.Email
		break
	}

	if email == "" {
		http.Error(w, "could not determine email", http.StatusInternalServerError)
		return
	}

	// Issue tailbus tokens
	accessToken, refreshToken, err := s.issuer.Issue(email)
	if err != nil {
		s.logger.Error("failed to issue JWT", "error", err)
		http.Error(w, "failed to issue tokens", http.StatusInternalServerError)
		return
	}

	// Mark device code as completed
	s.mu.Lock()
	foundDC.Email = email
	foundDC.AccessToken = accessToken
	foundDC.RefreshToken = refreshToken
	foundDC.Completed = true
	s.mu.Unlock()

	s.logger.Info("device code completed", "email", email)

	// Redirect to success page
	http.Redirect(w, r, s.externalURL+"/oauth/verify?success=true", http.StatusFound)
}

// handleRefresh handles POST /oauth/refresh — refresh an access token.
func (s *OAuthServer) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid request")
		return
	}

	newAccess, newRefresh, err := s.issuer.Refresh(req.RefreshToken)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_refresh_token"})
		return
	}

	// Extract email from the new access token
	claims, _ := s.issuer.Validate(newAccess)
	email := ""
	if claims != nil {
		email = claims.Email
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token":  newAccess,
		"refresh_token": newRefresh,
		"token_type":    "Bearer",
		"expires_in":    int(accessTokenDuration.Seconds()),
		"email":         email,
	})
}

// handleBrowserLogin starts the browser OAuth redirect flow.
func (s *OAuthServer) handleBrowserLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randomHex(16)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	s.browserStates[state] = &browserState{
		State:     state,
		ExpiresAt: time.Now().Add(15 * time.Minute),
	}
	s.mu.Unlock()

	// Find the first provider and build auth URL with web callback
	for _, p := range s.providers {
		// Use a separate redirect URI for browser flow
		webCfg := p.config
		webCfg.RedirectURL = s.externalURL + "/oauth/callback/web"
		authURL := webCfg.AuthCodeURL(state, oauth2.AccessTypeOffline)
		http.Redirect(w, r, authURL, http.StatusFound)
		return
	}

	http.Error(w, "no OAuth provider configured", http.StatusInternalServerError)
}

// handleBrowserCallback handles Google redirect for browser OAuth flow.
func (s *OAuthServer) handleBrowserCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Error(w, "missing code or state parameter", http.StatusBadRequest)
		return
	}

	// Verify state
	s.mu.Lock()
	bs, ok := s.browserStates[state]
	if ok {
		delete(s.browserStates, state)
	}
	s.mu.Unlock()

	if !ok || time.Now().After(bs.ExpiresAt) {
		http.Error(w, "invalid or expired session", http.StatusBadRequest)
		return
	}

	// Exchange code for ID token
	var email string
	for _, p := range s.providers {
		webCfg := p.config
		webCfg.RedirectURL = s.externalURL + "/oauth/callback/web"

		tok, err := webCfg.Exchange(r.Context(), code)
		if err != nil {
			s.logger.Error("browser OAuth token exchange failed", "error", err)
			http.Error(w, "authentication failed", http.StatusInternalServerError)
			return
		}

		rawIDToken, ok := tok.Extra("id_token").(string)
		if !ok {
			http.Error(w, "no id_token in response", http.StatusInternalServerError)
			return
		}

		idToken, err := p.verifier.Verify(r.Context(), rawIDToken)
		if err != nil {
			s.logger.Error("browser ID token verification failed", "error", err)
			http.Error(w, "token verification failed", http.StatusInternalServerError)
			return
		}

		var claims struct {
			Email string `json:"email"`
		}
		if err := idToken.Claims(&claims); err != nil {
			http.Error(w, "failed to parse claims", http.StatusInternalServerError)
			return
		}
		email = claims.Email
		break
	}

	if email == "" {
		http.Error(w, "could not determine email", http.StatusInternalServerError)
		return
	}

	// Issue JWT pair
	accessToken, refreshToken, err := s.issuer.Issue(email)
	if err != nil {
		s.logger.Error("failed to issue JWT for browser login", "error", err)
		http.Error(w, "failed to issue tokens", http.StatusInternalServerError)
		return
	}

	s.logger.Info("browser login completed", "email", email)

	// Redirect to web app with tokens in fragment (never sent to server)
	redirectURL := fmt.Sprintf("%s/dashboard#access_token=%s&refresh_token=%s&email=%s",
		s.webAppURL, accessToken, refreshToken, email)
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// Helper functions

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomUserCode(length int) (string, error) {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no I, O, 0, 1 to avoid confusion
	code := make([]byte, length)
	for i := range code {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}
		code[i] = chars[n.Int64()]
	}
	return string(code), nil
}

func formatUserCode(code string) string {
	if len(code) <= 4 {
		return code
	}
	return code[:4] + "-" + code[4:]
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

package coord

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// RESTHandler provides REST API endpoints for the dashboard.
type RESTHandler struct {
	store     *Store
	jwtIssuer *JWTIssuer
	logger    *slog.Logger
}

// NewRESTHandler creates a new REST API handler.
func NewRESTHandler(store *Store, jwtIssuer *JWTIssuer, logger *slog.Logger) *RESTHandler {
	return &RESTHandler{store: store, jwtIssuer: jwtIssuer, logger: logger}
}

// Handler returns an http.Handler with all REST routes.
func (h *RESTHandler) Handler() http.Handler {
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

// RegisterRoutes adds REST API routes to an existing mux.
func (h *RESTHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/me", h.handleMe)
	mux.HandleFunc("GET /api/v1/teams", h.handleListTeams)
	mux.HandleFunc("POST /api/v1/teams", h.handleCreateTeam)
	mux.HandleFunc("DELETE /api/v1/teams/{teamId}", h.handleDeleteTeam)
	mux.HandleFunc("GET /api/v1/teams/{teamId}/members", h.handleGetMembers)
	mux.HandleFunc("DELETE /api/v1/teams/{teamId}/members/{email}", h.handleRemoveMember)
	mux.HandleFunc("PUT /api/v1/teams/{teamId}/members/{email}/role", h.handleUpdateRole)
	mux.HandleFunc("POST /api/v1/teams/{teamId}/invites", h.handleCreateInvite)
	mux.HandleFunc("GET /api/v1/teams/{teamId}/nodes", h.handleGetNodes)
	mux.HandleFunc("POST /api/v1/invites/accept", h.handleAcceptInvite)
}

// authenticate extracts and validates the Bearer JWT from the request.
func (h *RESTHandler) authenticate(r *http.Request) (*Claims, error) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return nil, fmt.Errorf("missing or invalid Authorization header")
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	claims, err := h.jwtIssuer.Validate(token)
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}
	if claims.TokenType != "access" {
		return nil, fmt.Errorf("expected access token, got %s", claims.TokenType)
	}
	return claims, nil
}

func (h *RESTHandler) handleMe(w http.ResponseWriter, r *http.Request) {
	claims, err := h.authenticate(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"email": claims.Email})
}

func (h *RESTHandler) handleListTeams(w http.ResponseWriter, r *http.Request) {
	claims, err := h.authenticate(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, err.Error())
		return
	}

	teams, err := h.store.ListUserTeams(claims.Email)
	if err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Sprintf("list teams: %v", err))
		return
	}

	type teamJSON struct {
		TeamID string `json:"team_id"`
		Name   string `json:"name"`
		Role   string `json:"role"`
	}
	result := make([]teamJSON, 0, len(teams))
	for _, t := range teams {
		result = append(result, teamJSON{TeamID: t.TeamID, Name: t.Name, Role: t.Role})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"teams": result})
}

func (h *RESTHandler) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	claims, err := h.authenticate(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, err.Error())
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		httpError(w, http.StatusBadRequest, "name is required")
		return
	}

	teamID := generateID(8)
	if err := h.store.CreateTeam(teamID, req.Name, claims.Email); err != nil {
		httpError(w, http.StatusConflict, fmt.Sprintf("create team: %v", err))
		return
	}

	h.logger.Info("team created via REST", "team_id", teamID, "name", req.Name, "owner", claims.Email)
	writeJSON(w, http.StatusCreated, map[string]string{"team_id": teamID, "name": req.Name})
}

func (h *RESTHandler) handleDeleteTeam(w http.ResponseWriter, r *http.Request) {
	claims, err := h.authenticate(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, err.Error())
		return
	}

	teamID := r.PathValue("teamId")

	role, err := h.store.GetUserTeamRole(teamID, claims.Email)
	if err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Sprintf("lookup role: %v", err))
		return
	}
	if role != "owner" {
		httpError(w, http.StatusForbidden, "only team owners can delete teams")
		return
	}

	if err := h.store.DeleteTeam(teamID); err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Sprintf("delete team: %v", err))
		return
	}

	h.logger.Info("team deleted via REST", "team_id", teamID, "by", claims.Email)
	w.WriteHeader(http.StatusNoContent)
}

func (h *RESTHandler) handleGetMembers(w http.ResponseWriter, r *http.Request) {
	claims, err := h.authenticate(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, err.Error())
		return
	}

	teamID := r.PathValue("teamId")

	role, err := h.store.GetUserTeamRole(teamID, claims.Email)
	if err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Sprintf("lookup role: %v", err))
		return
	}
	if role == "" {
		httpError(w, http.StatusForbidden, "not a member of this team")
		return
	}

	members, err := h.store.GetTeamMembers(teamID)
	if err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Sprintf("get members: %v", err))
		return
	}

	type memberJSON struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	result := make([]memberJSON, 0, len(members))
	for _, m := range members {
		result = append(result, memberJSON{Email: m.Email, Role: m.Role})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"members": result})
}

func (h *RESTHandler) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	claims, err := h.authenticate(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, err.Error())
		return
	}

	teamID := r.PathValue("teamId")
	email := r.PathValue("email")

	role, err := h.store.GetUserTeamRole(teamID, claims.Email)
	if err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Sprintf("lookup role: %v", err))
		return
	}
	if role != "owner" {
		httpError(w, http.StatusForbidden, "only team owners can remove members")
		return
	}

	if email == claims.Email {
		httpError(w, http.StatusBadRequest, "cannot remove yourself; transfer ownership first or delete the team")
		return
	}

	if err := h.store.RemoveTeamMember(teamID, email); err != nil {
		httpError(w, http.StatusNotFound, err.Error())
		return
	}

	h.logger.Info("team member removed via REST", "team_id", teamID, "email", email, "by", claims.Email)
	w.WriteHeader(http.StatusNoContent)
}

func (h *RESTHandler) handleUpdateRole(w http.ResponseWriter, r *http.Request) {
	claims, err := h.authenticate(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, err.Error())
		return
	}

	teamID := r.PathValue("teamId")
	email := r.PathValue("email")

	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Role != "owner" && req.Role != "member") {
		httpError(w, http.StatusBadRequest, "role must be 'owner' or 'member'")
		return
	}

	callerRole, err := h.store.GetUserTeamRole(teamID, claims.Email)
	if err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Sprintf("lookup role: %v", err))
		return
	}
	if callerRole != "owner" {
		httpError(w, http.StatusForbidden, "only team owners can change roles")
		return
	}

	// Prevent demoting the last owner
	if email == claims.Email && req.Role != "owner" {
		members, err := h.store.GetTeamMembers(teamID)
		if err != nil {
			httpError(w, http.StatusInternalServerError, fmt.Sprintf("get members: %v", err))
			return
		}
		ownerCount := 0
		for _, m := range members {
			if m.Role == "owner" {
				ownerCount++
			}
		}
		if ownerCount <= 1 {
			httpError(w, http.StatusBadRequest, "cannot demote the last owner; promote another member first")
			return
		}
	}

	if err := h.store.UpdateTeamMemberRole(teamID, email, req.Role); err != nil {
		httpError(w, http.StatusNotFound, err.Error())
		return
	}

	h.logger.Info("team member role updated via REST", "team_id", teamID, "email", email, "role", req.Role, "by", claims.Email)
	writeJSON(w, http.StatusOK, map[string]string{"email": email, "role": req.Role})
}

func (h *RESTHandler) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	claims, err := h.authenticate(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, err.Error())
		return
	}

	teamID := r.PathValue("teamId")

	role, err := h.store.GetUserTeamRole(teamID, claims.Email)
	if err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Sprintf("lookup role: %v", err))
		return
	}
	if role != "owner" {
		httpError(w, http.StatusForbidden, "only team owners can create invites")
		return
	}

	var req struct {
		MaxUses    int `json:"max_uses"`
		TTLSeconds int `json:"ttl_seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	maxUses := req.MaxUses
	if maxUses <= 0 {
		maxUses = 1
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	expiresAt := time.Now().Add(ttl)

	code := generateID(4) + "-" + generateID(4)
	if err := h.store.CreateTeamInvite(code, teamID, claims.Email, expiresAt, maxUses); err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Sprintf("create invite: %v", err))
		return
	}

	h.logger.Info("team invite created via REST", "team_id", teamID, "code", code, "max_uses", maxUses)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"code":       code,
		"expires_at": expiresAt.Unix(),
	})
}

func (h *RESTHandler) handleGetNodes(w http.ResponseWriter, r *http.Request) {
	claims, err := h.authenticate(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, err.Error())
		return
	}

	teamID := r.PathValue("teamId")

	role, err := h.store.GetUserTeamRole(teamID, claims.Email)
	if err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Sprintf("lookup role: %v", err))
		return
	}
	if role == "" {
		httpError(w, http.StatusForbidden, "not a member of this team")
		return
	}

	nodes, err := h.store.GetNodesByTeam(teamID)
	if err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Sprintf("get nodes: %v", err))
		return
	}

	type nodeJSON struct {
		NodeID        string   `json:"node_id"`
		AdvertiseAddr string   `json:"advertise_addr"`
		Handles       []string `json:"handles"`
		LastHeartbeat int64    `json:"last_heartbeat"`
		IsRelay       bool     `json:"is_relay"`
	}
	result := make([]nodeJSON, 0, len(nodes))
	for _, n := range nodes {
		result = append(result, nodeJSON{
			NodeID:        n.NodeID,
			AdvertiseAddr: n.AdvertiseAddr,
			Handles:       n.Handles,
			LastHeartbeat: n.LastHeartbeat.Unix(),
			IsRelay:       n.IsRelay,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"nodes": result})
}

func (h *RESTHandler) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	claims, err := h.authenticate(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, err.Error())
		return
	}

	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		httpError(w, http.StatusBadRequest, "code is required")
		return
	}

	teamID, err := h.store.ConsumeTeamInvite(req.Code)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.store.AddTeamMember(teamID, claims.Email, "member"); err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Sprintf("add member: %v", err))
		return
	}

	var teamName string
	_ = h.store.db.QueryRow("SELECT name FROM teams WHERE team_id = ?", teamID).Scan(&teamName)

	h.logger.Info("team invite accepted via REST", "team_id", teamID, "email", claims.Email)
	writeJSON(w, http.StatusOK, map[string]string{"team_id": teamID, "team_name": teamName})
}

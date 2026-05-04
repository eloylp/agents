package config

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/eloylp/agents/internal/store"
)

type tokenBudgetPatchRequest struct {
	ScopeKind  *string `json:"scope_kind"`
	ScopeName  *string `json:"scope_name"`
	Period     *string `json:"period"`
	CapTokens  *int64  `json:"cap_tokens"`
	AlertAtPct *int    `json:"alert_at_pct"`
	Enabled    *bool   `json:"enabled"`
}

func (r tokenBudgetPatchRequest) toStorePatch() store.TokenBudgetPatch {
	return store.TokenBudgetPatch{
		ScopeKind:  r.ScopeKind,
		ScopeName:  r.ScopeName,
		Period:     r.Period,
		CapTokens:  r.CapTokens,
		AlertAtPct: r.AlertAtPct,
		Enabled:    r.Enabled,
	}
}

// handleTokenBudgets dispatches GET /token_budgets and POST /token_budgets.
func (h *Handler) handleTokenBudgets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listTokenBudgets(w, r)
	case http.MethodPost:
		h.createTokenBudget(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleTokenBudget dispatches GET/PATCH/DELETE /token_budgets/{id}.
func (h *Handler) handleTokenBudget(w http.ResponseWriter, r *http.Request) {
	idStr := mux.Vars(r)["id"]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.getTokenBudget(w, r, id)
	case http.MethodPatch:
		h.updateTokenBudget(w, r, id)
	case http.MethodDelete:
		h.deleteTokenBudget(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) listTokenBudgets(w http.ResponseWriter, _ *http.Request) {
	budgets, err := h.store.ListTokenBudgets()
	if err != nil {
		h.logger.Error().Err(err).Msg("list token budgets")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if budgets == nil {
		budgets = []store.TokenBudget{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(budgets)
}

func (h *Handler) createTokenBudget(w http.ResponseWriter, r *http.Request) {
	var b store.TokenBudget
	if !decodeBody(w, r, h.daemonCfg.HTTP.MaxBodyBytes, &b) {
		return
	}
	created, err := h.store.CreateTokenBudget(b)
	if err != nil {
		http.Error(w, err.Error(), storeErrStatus(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(created)
}

func (h *Handler) getTokenBudget(w http.ResponseWriter, _ *http.Request, id int64) {
	b, err := h.store.GetTokenBudget(id)
	if err != nil {
		http.Error(w, err.Error(), storeErrStatus(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(b)
}

func (h *Handler) updateTokenBudget(w http.ResponseWriter, r *http.Request, id int64) {
	var req tokenBudgetPatchRequest
	if !decodeBody(w, r, h.daemonCfg.HTTP.MaxBodyBytes, &req) {
		return
	}
	updated, err := h.store.PatchTokenBudget(id, req.toStorePatch())
	if err != nil {
		http.Error(w, err.Error(), storeErrStatus(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updated)
}

func (h *Handler) deleteTokenBudget(w http.ResponseWriter, _ *http.Request, id int64) {
	if err := h.store.DeleteTokenBudget(id); err != nil {
		http.Error(w, err.Error(), storeErrStatus(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTokenBudgetAlerts serves GET /token_budgets/alerts.
func (h *Handler) handleTokenBudgetAlerts(w http.ResponseWriter, _ *http.Request) {
	alerts, err := h.store.BudgetAlerts()
	if err != nil {
		h.logger.Error().Err(err).Msg("budget alerts")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if alerts == nil {
		alerts = []store.BudgetAlert{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"count":  len(alerts),
		"alerts": alerts,
	})
}

// handleTokenLeaderboard serves GET /token_leaderboard?repo=&period=.
func (h *Handler) handleTokenLeaderboard(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "monthly"
	}
	entries, err := h.store.TokenLeaderboard(repo, period)
	if err != nil {
		h.logger.Error().Err(err).Msg("token leaderboard")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []store.LeaderboardEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

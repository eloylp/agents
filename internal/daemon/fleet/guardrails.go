package fleet

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// ── Guardrail wire types ─────────────────────────────────────────────────────

type storeGuardrailJSON struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	Content        string `json:"content"`
	DefaultContent string `json:"default_content"`
	IsBuiltin      bool   `json:"is_builtin"`
	Enabled        bool   `json:"enabled"`
	Position       int    `json:"position"`
}

func guardrailToJSON(g fleet.Guardrail) storeGuardrailJSON {
	return storeGuardrailJSON{
		Name:           g.Name,
		Description:    g.Description,
		Content:        g.Content,
		DefaultContent: g.DefaultContent,
		IsBuiltin:      g.IsBuiltin,
		Enabled:        g.Enabled,
		Position:       g.Position,
	}
}

// GuardrailPatch is the partial-update shape for a guardrail. Used by both
// the REST PATCH /guardrails/{name} handler and the MCP update_guardrail
// tool. A nil field means "don't touch". The is_builtin and
// default_content fields are deliberately not patchable — built-in
// status is set by the migration; default_content is reset territory.
type GuardrailPatch struct {
	Description *string `json:"description,omitempty"`
	Content     *string `json:"content,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
	Position    *int    `json:"position,omitempty"`
}

func (p GuardrailPatch) anyFieldSet() bool {
	return p.Description != nil || p.Content != nil || p.Enabled != nil || p.Position != nil
}

func (p GuardrailPatch) apply(g *fleet.Guardrail) {
	if p.Description != nil {
		g.Description = *p.Description
	}
	if p.Content != nil {
		g.Content = *p.Content
	}
	if p.Enabled != nil {
		g.Enabled = *p.Enabled
	}
	if p.Position != nil {
		g.Position = *p.Position
	}
}

// ── Guardrail handlers ───────────────────────────────────────────────────────

func (h *Handler) handleGuardrails(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		gs, err := h.store.ReadAllGuardrails()
		if err != nil {
			http.Error(w, fmt.Sprintf("read guardrails: %v", err), http.StatusInternalServerError)
			return
		}
		out := make([]storeGuardrailJSON, 0, len(gs))
		for _, g := range gs {
			out = append(out, guardrailToJSON(g))
		}
		writeJSON(w, http.StatusOK, out)

	case http.MethodPost:
		var req storeGuardrailJSON
		if !decodeBody(w, r, h.maxBodyBytes, &req) {
			return
		}
		// Operator-supplied rows are never built-in; ignore any client-set
		// is_builtin / default_content values to keep the migration the
		// sole source of those flags.
		g, err := h.UpsertGuardrail(fleet.Guardrail{
			Name:        req.Name,
			Description: req.Description,
			Content:     req.Content,
			Enabled:     req.Enabled,
			Position:    req.Position,
		})
		if err != nil {
			h.writeErr(w, err, "guardrail upsert")
			return
		}
		writeJSON(w, http.StatusOK, guardrailToJSON(g))
	}
}

func (h *Handler) handleGuardrail(w http.ResponseWriter, r *http.Request) {
	name := fleet.NormalizeGuardrailName(mux.Vars(r)["name"])
	switch r.Method {
	case http.MethodGet:
		g, err := h.store.GetGuardrail(name)
		if err != nil {
			h.writeErr(w, err, "guardrail get")
			return
		}
		writeJSON(w, http.StatusOK, guardrailToJSON(g))

	case http.MethodPatch:
		h.handleGuardrailPatch(w, r, name)

	case http.MethodDelete:
		if err := h.DeleteGuardrail(name); err != nil {
			h.writeErr(w, err, "guardrail delete")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) handleGuardrailPatch(w http.ResponseWriter, r *http.Request, name string) {
	var req GuardrailPatch
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	if !req.anyFieldSet() {
		http.Error(w, "at least one field is required", http.StatusBadRequest)
		return
	}
	g, err := h.UpdateGuardrailPatch(name, req)
	if err != nil {
		h.writeErr(w, err, "guardrail patch")
		return
	}
	writeJSON(w, http.StatusOK, guardrailToJSON(g))
}

func (h *Handler) handleGuardrailReset(w http.ResponseWriter, r *http.Request) {
	name := fleet.NormalizeGuardrailName(mux.Vars(r)["name"])
	g, err := h.ResetGuardrail(name)
	if err != nil {
		h.writeErr(w, err, "guardrail reset")
		return
	}
	writeJSON(w, http.StatusOK, guardrailToJSON(g))
}

// ── Guardrail methods (exposed for MCP) ──────────────────────────────────────

// UpsertGuardrail writes or updates a guardrail. The Name is normalised
// before persistence; an empty name is rejected as *store.ErrValidation.
// Returns the canonical row as it sits in the database after the write
// (so the caller sees the post-normalisation form including the
// preserved is_builtin / default_content flags).
func (h *Handler) UpsertGuardrail(g fleet.Guardrail) (fleet.Guardrail, error) {
	if strings.TrimSpace(g.Name) == "" {
		return fleet.Guardrail{}, &store.ErrValidation{Msg: "name is required"}
	}
	if err := h.store.UpsertGuardrail(g); err != nil {
		return fleet.Guardrail{}, err
	}
	return h.store.GetGuardrail(g.Name)
}

// UpdateGuardrailPatch applies a partial patch to the named guardrail.
// Returns *store.ErrNotFound when the row does not exist. Used by both
// the REST PATCH handler and the MCP update_guardrail tool.
func (h *Handler) UpdateGuardrailPatch(name string, patch GuardrailPatch) (fleet.Guardrail, error) {
	normalized := fleet.NormalizeGuardrailName(name)
	existing, err := h.store.GetGuardrail(normalized)
	if err != nil {
		return fleet.Guardrail{}, err
	}
	patch.apply(&existing)
	if err := h.store.UpsertGuardrail(existing); err != nil {
		return fleet.Guardrail{}, err
	}
	return h.store.GetGuardrail(normalized)
}

// DeleteGuardrail removes the named guardrail. Returns *store.ErrNotFound
// when the row does not exist.
func (h *Handler) DeleteGuardrail(name string) error {
	return h.store.DeleteGuardrail(name)
}

// ResetGuardrail copies a built-in guardrail's default_content back into
// its content. Returns *store.ErrValidation when the row has no default
// (i.e., it is operator-added) and *store.ErrNotFound when the row does
// not exist. Used by both POST /guardrails/{name}/reset and the MCP
// reset_guardrail tool.
func (h *Handler) ResetGuardrail(name string) (fleet.Guardrail, error) {
	if err := h.store.ResetGuardrail(name); err != nil {
		return fleet.Guardrail{}, err
	}
	return h.store.GetGuardrail(name)
}

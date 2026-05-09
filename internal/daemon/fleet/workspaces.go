package fleet

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/gorilla/mux"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

type storeWorkspaceJSON struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type WorkspacePatch struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

func (p WorkspacePatch) AnyFieldSet() bool { return p.Name != nil || p.Description != nil }

func (p WorkspacePatch) apply(w *fleet.Workspace) {
	if p.Name != nil {
		w.Name = *p.Name
	}
	if p.Description != nil {
		w.Description = *p.Description
	}
}

func workspaceToStoreJSON(w fleet.Workspace) storeWorkspaceJSON {
	return storeWorkspaceJSON{ID: w.ID, Name: w.Name, Description: w.Description}
}

func (j storeWorkspaceJSON) toConfig() fleet.Workspace {
	return fleet.Workspace{ID: j.ID, Name: j.Name, Description: j.Description}
}

type workspaceGuardrailJSON struct {
	GuardrailName string `json:"guardrail_name"`
	Position      int    `json:"position"`
	Enabled       bool   `json:"enabled"`
}

func workspaceGuardrailToJSON(ref fleet.WorkspaceGuardrailRef) workspaceGuardrailJSON {
	return workspaceGuardrailJSON{
		GuardrailName: ref.GuardrailName,
		Position:      ref.Position,
		Enabled:       ref.Enabled,
	}
}

func (j workspaceGuardrailJSON) toConfig() fleet.WorkspaceGuardrailRef {
	return fleet.WorkspaceGuardrailRef{
		GuardrailName: j.GuardrailName,
		Position:      j.Position,
		Enabled:       j.Enabled,
	}
}

func (h *Handler) handleWorkspacesList(w http.ResponseWriter, _ *http.Request) {
	workspaces, err := h.store.ReadWorkspaces()
	if err != nil {
		http.Error(w, fmt.Sprintf("read workspaces: %v", err), http.StatusInternalServerError)
		return
	}
	out := make([]storeWorkspaceJSON, 0, len(workspaces))
	for _, workspace := range workspaces {
		out = append(out, workspaceToStoreJSON(workspace))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) handleWorkspaceCreate(w http.ResponseWriter, r *http.Request) {
	var req storeWorkspaceJSON
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	workspace, err := h.UpsertWorkspace(req.toConfig())
	if err != nil {
		h.writeErr(w, err, "workspace upsert")
		return
	}
	writeJSON(w, http.StatusOK, workspaceToStoreJSON(workspace))
}

func (h *Handler) handleWorkspaceGet(w http.ResponseWriter, r *http.Request) {
	workspace, err := h.getWorkspace(workspacePathValue(r))
	if err != nil {
		h.writeErr(w, err, "workspace get")
		return
	}
	writeJSON(w, http.StatusOK, workspaceToStoreJSON(workspace))
}

func (h *Handler) handleWorkspacePatch(w http.ResponseWriter, r *http.Request) {
	var req WorkspacePatch
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	if !req.AnyFieldSet() {
		http.Error(w, "at least one field is required", http.StatusBadRequest)
		return
	}
	workspace, err := h.UpdateWorkspacePatch(workspacePathValue(r), req)
	if err != nil {
		h.writeErr(w, err, "workspace patch")
		return
	}
	writeJSON(w, http.StatusOK, workspaceToStoreJSON(workspace))
}

func (h *Handler) handleWorkspaceDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.DeleteWorkspace(workspacePathValue(r)); err != nil {
		h.writeErr(w, err, "workspace delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleWorkspaceGuardrailsGet(w http.ResponseWriter, r *http.Request) {
	refs, err := h.store.ReadWorkspaceGuardrails(workspacePathValue(r))
	if err != nil {
		h.writeErr(w, err, "workspace guardrails get")
		return
	}
	out := make([]workspaceGuardrailJSON, 0, len(refs))
	for _, ref := range refs {
		out = append(out, workspaceGuardrailToJSON(ref))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) handleWorkspaceGuardrailsPut(w http.ResponseWriter, r *http.Request) {
	var req []workspaceGuardrailJSON
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	refs := make([]fleet.WorkspaceGuardrailRef, 0, len(req))
	for _, ref := range req {
		refs = append(refs, ref.toConfig())
	}
	updated, err := h.store.ReplaceWorkspaceGuardrails(workspacePathValue(r), refs)
	if err != nil {
		h.writeErr(w, err, "workspace guardrails replace")
		return
	}
	out := make([]workspaceGuardrailJSON, 0, len(updated))
	for _, ref := range updated {
		out = append(out, workspaceGuardrailToJSON(ref))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) UpsertWorkspace(w fleet.Workspace) (fleet.Workspace, error) {
	return h.store.UpsertWorkspace(w)
}

func (h *Handler) UpdateWorkspacePatch(workspace string, patch WorkspacePatch) (fleet.Workspace, error) {
	existing, err := h.getWorkspace(workspace)
	if err != nil {
		return fleet.Workspace{}, err
	}
	patch.apply(&existing)
	return h.store.UpsertWorkspace(existing)
}

func (h *Handler) DeleteWorkspace(workspace string) error {
	return h.store.DeleteWorkspace(workspace)
}

func (h *Handler) getWorkspace(workspace string) (fleet.Workspace, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = fleet.DefaultWorkspaceID
	}
	workspaces, err := h.store.ReadWorkspaces()
	if err != nil {
		return fleet.Workspace{}, err
	}
	idx := slices.IndexFunc(workspaces, func(w fleet.Workspace) bool {
		return w.ID == workspace || w.Name == workspace
	})
	if idx < 0 {
		return fleet.Workspace{}, &store.ErrNotFound{Msg: fmt.Sprintf("workspace %q not found", workspace)}
	}
	return workspaces[idx], nil
}

func workspacePathValue(r *http.Request) string {
	return strings.TrimSpace(mux.Vars(r)["workspace"])
}

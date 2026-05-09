package fleet

import (
	"net/http"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

type graphLayoutJSON struct {
	Positions []store.GraphNodePosition `json:"positions"`
}

func (h *Handler) handleGraphLayoutGet(w http.ResponseWriter, r *http.Request) {
	workspace := fleet.NormalizeWorkspaceID(r.URL.Query().Get("workspace"))
	positions, err := h.store.ReadWorkspaceGraphLayout(workspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if positions == nil {
		positions = []store.GraphNodePosition{}
	}
	writeJSON(w, http.StatusOK, graphLayoutJSON{Positions: positions})
}

func (h *Handler) handleGraphLayoutPut(w http.ResponseWriter, r *http.Request) {
	var req graphLayoutJSON
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	workspace := fleet.NormalizeWorkspaceID(r.URL.Query().Get("workspace"))
	if err := h.store.UpsertWorkspaceGraphLayout(workspace, req.Positions); err != nil {
		h.writeErr(w, err, "graph layout upsert")
		return
	}
	positions, err := h.store.ReadWorkspaceGraphLayout(workspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, graphLayoutJSON{Positions: positions})
}

func (h *Handler) handleGraphLayoutDelete(w http.ResponseWriter, r *http.Request) {
	workspace := fleet.NormalizeWorkspaceID(r.URL.Query().Get("workspace"))
	if err := h.store.ClearWorkspaceGraphLayout(workspace); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

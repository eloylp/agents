package fleet

import (
	"net/http"

	"github.com/eloylp/agents/internal/store"
)

type graphLayoutJSON struct {
	Positions []store.GraphNodePosition `json:"positions"`
}

func (h *Handler) handleGraphLayoutGet(w http.ResponseWriter, _ *http.Request) {
	positions, err := h.store.ReadGraphLayout()
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
	if err := h.store.UpsertGraphLayout(req.Positions); err != nil {
		h.writeErr(w, err, "graph layout upsert")
		return
	}
	positions, err := h.store.ReadGraphLayout()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, graphLayoutJSON{Positions: positions})
}

func (h *Handler) handleGraphLayoutDelete(w http.ResponseWriter, _ *http.Request) {
	if err := h.store.ClearGraphLayout(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

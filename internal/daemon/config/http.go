package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

func decodeBody[T any](w http.ResponseWriter, r *http.Request, limit int64, out *T) bool {
	if limit <= 0 {
		limit = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, fmt.Sprintf("read request: %v", err), http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(body, out); err != nil {
		http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
		return false
	}
	return true
}

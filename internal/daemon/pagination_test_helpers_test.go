package daemon_test

import (
	"encoding/json"
	"io"
	"testing"
)

func decodeItems[T any](t *testing.T, r io.Reader, out *[]T) {
	t.Helper()
	var page struct {
		Items  []T `json:"items"`
		Total  int `json:"total"`
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	}
	if err := json.NewDecoder(r).Decode(&page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	*out = page.Items
}

package pagination

import (
	"net/http"
	"strconv"
)

const (
	DefaultLimit = 50
	MaxLimit     = 500
)

type Params struct {
	Limit  int
	Offset int
}

type Page[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

func Clamp(limit, offset int) Params {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return Params{Limit: limit, Offset: offset}
}

func Parse(r *http.Request) Params {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	return Clamp(limit, offset)
}

func NewPage[T any](items []T, total int, params Params) Page[T] {
	if items == nil {
		items = []T{}
	}
	return Page[T]{Items: items, Total: total, Limit: params.Limit, Offset: params.Offset}
}

package store

const (
	DefaultPageLimit = 50
	MaxPageLimit     = 500
)

func clampPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	if limit > MaxPageLimit {
		limit = MaxPageLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

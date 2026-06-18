package store

import (
	"fmt"
	"strings"
	"time"
)

// boolToInt converts a bool to 0/1 for SQLite storage.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// intToBool converts a SQLite 0/1 to bool.
func intToBool(i int) bool { return i != 0 }

// bindingEnabledInt converts a nullable *bool flag to 0/1 for SQLite storage.
// Nil means the default (enabled); only an explicit non-nil false maps to 0.
// Used for both binding.Enabled and agent.AllowMemory, which share this
// nil-means-default-on semantics.
func bindingEnabledInt(enabled *bool) int {
	if enabled != nil && !*enabled {
		return 0
	}
	return 1
}

// SQLiteTime scans SQLite timestamp values without depending on the driver to
// materialize a particular Go type. SQLite may return TIMESTAMP columns as
// time.Time, []byte, or text in either RFC3339 or datetime('now') shape.
type SQLiteTime struct {
	Time  time.Time
	Valid bool
	raw   string
	err   error
}

func (t *SQLiteTime) Scan(value any) error {
	t.Time = time.Time{}
	t.Valid = false
	t.raw = ""
	t.err = nil
	switch v := value.(type) {
	case nil:
		return nil
	case time.Time:
		t.Time = v.UTC()
		t.Valid = true
		return nil
	case string:
		t.raw = strings.TrimSpace(v)
	case []byte:
		t.raw = strings.TrimSpace(string(v))
	default:
		t.err = fmt.Errorf("unsupported sqlite timestamp type %T", value)
		return nil
	}
	if t.raw == "" {
		return nil
	}
	parsed, err := ParseSQLiteTime(t.raw)
	if err != nil {
		t.err = err
		return nil
	}
	t.Time = parsed
	t.Valid = true
	return nil
}

func (t SQLiteTime) Err() error {
	if t.err == nil {
		return nil
	}
	if t.raw != "" {
		return fmt.Errorf("parse sqlite timestamp %q: %w", t.raw, t.err)
	}
	return t.err
}

func (t SQLiteTime) OrZero() time.Time {
	if !t.Valid || t.err != nil {
		return time.Time{}
	}
	return t.Time
}

func (t SQLiteTime) Ptr() *time.Time {
	if !t.Valid || t.err != nil {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

func ParseSQLiteTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		time.DateTime,
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 -0700",
		"2006-01-02 15:04:05.999999999 -0700 -0700",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp format")
}

func sqliteTimeArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

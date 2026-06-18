package store_test

import (
	"testing"
	"time"

	"github.com/eloylp/agents/internal/store"
)

func TestSQLiteTimeScansSupportedDriverShapes(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, 6, 18, 11, 6, 20, 123456789, time.UTC)
	tests := []struct {
		name  string
		value any
		want  time.Time
		valid bool
	}{
		{name: "nil nullable timestamp", value: nil, valid: false},
		{name: "time value", value: want, want: want, valid: true},
		{name: "rfc3339 nano text", value: "2026-06-18T11:06:20.123456789Z", want: want, valid: true},
		{name: "sqlite datetime text", value: "2026-06-18 11:06:20", want: want.Truncate(time.Second), valid: true},
		{name: "driver zone text", value: []byte("2026-06-18 13:06:20 +0200 +0200"), want: time.Date(2026, 6, 18, 11, 6, 20, 0, time.UTC), valid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got store.SQLiteTime
			if err := got.Scan(tt.value); err != nil {
				t.Fatalf("Scan() error = %v", err)
			}
			if err := got.Err(); err != nil {
				t.Fatalf("Err() = %v, want nil", err)
			}
			if got.Valid != tt.valid {
				t.Fatalf("Valid = %v, want %v", got.Valid, tt.valid)
			}
			if !got.OrZero().Equal(tt.want) {
				t.Fatalf("OrZero() = %v, want %v", got.OrZero(), tt.want)
			}
			if tt.valid && got.Ptr() == nil {
				t.Fatalf("Ptr() = nil, want parsed time")
			}
			if !tt.valid && got.Ptr() != nil {
				t.Fatalf("Ptr() = %v, want nil", got.Ptr())
			}
		})
	}
}

func TestSQLiteTimeReportsUnsupportedText(t *testing.T) {
	t.Parallel()

	var got store.SQLiteTime
	if err := got.Scan("not-a-time"); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if err := got.Err(); err == nil {
		t.Fatal("Err() = nil, want parse error")
	}
	if !got.OrZero().IsZero() {
		t.Fatalf("OrZero() = %v, want zero time", got.OrZero())
	}
}

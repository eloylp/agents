package webhook

import (
	"context"
	"testing"
	"time"
)

func TestDeliveryStore_SeenOrAdd(t *testing.T) {
	t.Parallel()

	now := time.Now()
	ttl := time.Hour

	tests := []struct {
		name      string
		setup     func(s *DeliveryStore)
		id        string
		now       time.Time
		wantSeen  bool
		wantCount int
	}{
		{
			name:      "new id returns false",
			id:        "abc",
			now:       now,
			wantSeen:  false,
			wantCount: 1,
		},
		{
			name: "duplicate id within TTL returns true",
			setup: func(s *DeliveryStore) {
				s.SeenOrAdd("abc", now)
			},
			id:        "abc",
			now:       now.Add(30 * time.Minute),
			wantSeen:  true,
			wantCount: 1,
		},
		{
			name: "expired id treated as new",
			setup: func(s *DeliveryStore) {
				s.SeenOrAdd("abc", now)
			},
			id:        "abc",
			now:       now.Add(2 * ttl),
			wantSeen:  false,
			wantCount: 1,
		},
		{
			name: "different ids are independent",
			setup: func(s *DeliveryStore) {
				s.SeenOrAdd("abc", now)
			},
			id:        "xyz",
			now:       now,
			wantSeen:  false,
			wantCount: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := NewDeliveryStore(ttl)
			if tc.setup != nil {
				tc.setup(s)
			}
			got := s.SeenOrAdd(tc.id, tc.now)
			if got != tc.wantSeen {
				t.Errorf("SeenOrAdd() = %v, want %v", got, tc.wantSeen)
			}
			s.mu.Lock()
			count := len(s.deliveries)
			s.mu.Unlock()
			if count != tc.wantCount {
				t.Errorf("deliveries count = %d, want %d", count, tc.wantCount)
			}
		})
	}
}

func TestDeliveryStore_Delete(t *testing.T) {
	t.Parallel()

	now := time.Now()
	s := NewDeliveryStore(time.Hour)
	s.SeenOrAdd("abc", now)

	s.Delete("abc")

	// After deletion the id should be treated as new.
	if s.SeenOrAdd("abc", now) {
		t.Error("expected SeenOrAdd to return false after Delete, got true")
	}
}

func TestDeliveryStore_BackgroundEviction(t *testing.T) {
	t.Parallel()

	ttl := 40 * time.Millisecond
	s := NewDeliveryStore(ttl)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()

	now := time.Now()
	s.SeenOrAdd("abc", now)

	// Confirm the entry is live.
	if !s.SeenOrAdd("abc", now) {
		t.Fatal("expected entry to be seen immediately after insertion")
	}

	// Wait longer than one eviction interval (ttl/4) plus one TTL so the entry expires
	// and the background ticker has had a chance to evict it.
	time.Sleep(ttl + ttl/4 + 20*time.Millisecond)

	s.mu.Lock()
	count := len(s.deliveries)
	s.mu.Unlock()

	if count != 0 {
		t.Errorf("expected 0 deliveries after background eviction, got %d", count)
	}
}

func TestDeliveryStore_StartStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	ttl := 50 * time.Millisecond
	s := NewDeliveryStore(ttl)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Run(ctx); close(done) }()
	cancel() // Stop the background goroutine immediately.
	<-done

	// Add an entry after the goroutine has stopped.
	s.SeenOrAdd("abc", time.Now())
	s.mu.Lock()
	count := len(s.deliveries)
	s.mu.Unlock()

	// Entry should still be present since eviction goroutine stopped.
	if count != 1 {
		t.Errorf("expected 1 delivery, got %d", count)
	}
}

func TestDeliveryStore_StartIsNoOpForNonPositiveTTL(t *testing.T) {
	t.Parallel()

	// Run must return immediately when TTL is zero or negative — no
	// ticker is started so there is nothing to keep alive.
	for _, ttl := range []time.Duration{0, -1 * time.Second} {
		s := NewDeliveryStore(ttl)
		if err := s.Run(context.Background()); err != nil {
			t.Errorf("Run with ttl=%v: got %v, want nil", ttl, err)
		}
	}
}

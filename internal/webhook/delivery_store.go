package webhook

import (
	"context"
	"sync"
	"time"
)

type DeliveryStore struct {
	ttl        time.Duration
	mu         sync.Mutex
	deliveries map[string]time.Time
}

func NewDeliveryStore(ttl time.Duration) *DeliveryStore {
	return &DeliveryStore{
		ttl:        ttl,
		deliveries: map[string]time.Time{},
	}
}

// Run blocks until ctx is cancelled, periodically evicting expired
// entries. The caller (typically the daemon's errgroup) owns goroutine
// creation and waits on Run for clean shutdown. If the TTL is
// non-positive Run returns immediately; entries are still correctly
// expired on SeenOrAdd access.
func (s *DeliveryStore) Run(ctx context.Context) error {
	if s.ttl <= 0 {
		return nil
	}
	ticker := time.NewTicker(s.ttl / 4)
	defer ticker.Stop()
	return s.run(ctx, ticker.C)
}

func (s *DeliveryStore) run(ctx context.Context, ticks <-chan time.Time) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticks:
			s.evict(now)
		}
	}
}

// evict removes all entries whose TTL has expired. Callers must not hold s.mu.
func (s *DeliveryStore) evict(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, expiresAt := range s.deliveries {
		if now.After(expiresAt) {
			delete(s.deliveries, key)
		}
	}
}

// SeenOrAdd returns true if id has been seen within the TTL window,
// otherwise it records id and returns false. Expired entries are evicted
// by the background goroutine driving Run.
func (s *DeliveryStore) SeenOrAdd(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if expiresAt, ok := s.deliveries[id]; ok && now.Before(expiresAt) {
		return true
	}

	s.deliveries[id] = now.Add(s.ttl)
	return false
}

// Delete removes a delivery id from the dedupe cache.
func (s *DeliveryStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.deliveries, id)
}

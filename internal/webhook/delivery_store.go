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

// Start launches a background goroutine that periodically evicts expired entries.
// It runs until ctx is cancelled. Call this once after constructing the store.
// If the TTL is non-positive Start is a no-op; entries will never be evicted in
// the background (they are still correctly expired on SeenOrAdd access).
func (s *DeliveryStore) Start(ctx context.Context) {
	if s.ttl <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(s.ttl / 4)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.evict(now)
			}
		}
	}()
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

// SeenOrAdd returns true if id has been seen within the TTL window, otherwise
// it records id and returns false. Expired entries are evicted by the background
// goroutine started with Start.
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

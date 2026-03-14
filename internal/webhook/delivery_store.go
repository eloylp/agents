package webhook

import (
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

// SeenOrAdd returns true if id has been seen within the TTL window, otherwise
// it records id and returns false. Expired entries are lazily evicted on each
// call to avoid a separate background goroutine.
func (s *DeliveryStore) SeenOrAdd(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, expiresAt := range s.deliveries {
		if now.After(expiresAt) {
			delete(s.deliveries, key)
		}
	}

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

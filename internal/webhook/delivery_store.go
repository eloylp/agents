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

package workflow

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrEventQueueFull = errors.New("event queue full")
	ErrQueueClosed    = errors.New("queue closed")
)

type DataChannels struct {
	eventQueue chan Event
	mu         sync.RWMutex
	closed     bool
}

func NewDataChannels(buffer int) *DataChannels {
	return &DataChannels{
		eventQueue: make(chan Event, buffer),
	}
}

// PushEvent enqueues an event without blocking. The select has three arms:
// context cancellation (caller is shutting down), successful enqueue, and the
// default case which fires immediately when the channel buffer is full.
// EnqueuedAt is stamped here so queue-wait time can be computed by the engine.
func (dc *DataChannels) PushEvent(ctx context.Context, ev Event) error {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	if dc.closed {
		return ErrQueueClosed
	}
	if ev.EnqueuedAt.IsZero() {
		ev.EnqueuedAt = time.Now()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case dc.eventQueue <- ev:
		return nil
	default:
		return ErrEventQueueFull
	}
}

func (dc *DataChannels) EventChan() <-chan Event {
	return dc.eventQueue
}

// QueueStat describes the current depth and capacity of the event queue.
type QueueStat struct {
	Buffered int
	Capacity int
}

// QueueStats returns the current depth and capacity of the event queue.
// Reading channel length and capacity is safe without holding the mutex because
// these are intrinsic properties of the channel value and the lock only guards
// the closed flag and send operations.
func (dc *DataChannels) QueueStats() QueueStat {
	return QueueStat{len(dc.eventQueue), cap(dc.eventQueue)}
}

// Close shuts down the event queue. The write lock ensures no in-flight Push
// call can race with channel closure. Subsequent Close calls are safe because
// the closed flag is checked first.
func (dc *DataChannels) Close() {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if dc.closed {
		return
	}
	dc.closed = true
	close(dc.eventQueue)
}

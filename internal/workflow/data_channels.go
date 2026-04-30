package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/eloylp/agents/internal/store"
)

var (
	ErrEventQueueFull = errors.New("event queue full")
	ErrQueueClosed    = errors.New("queue closed")
)

// DataChannels is the durable event queue used across the daemon.
// PushEvent persists the event into SQLite first, then signals workers
// through an in-memory channel of QueuedEvents. The DB is the source of
// truth; the channel is just a wake-up notification.
//
// On a clean shutdown the table is mostly empty (workers mark rows
// completed as they go). On a crash, rows whose completed_at is NULL
// are replayed at next startup so events sitting in the buffer don't
// vanish and runs that were interrupted mid-prompt re-execute.
type DataChannels struct {
	eventQueue chan QueuedEvent
	store      *store.Store
	mu         sync.RWMutex
	closed     bool
}

func NewDataChannels(buffer int, st *store.Store) *DataChannels {
	return &DataChannels{
		eventQueue: make(chan QueuedEvent, buffer),
		store:      st,
	}
}

// PushEvent persists ev to SQLite and notifies workers via the channel.
// Returns the new event_queue row id on success — useful to operators
// re-running an event from /queue/{id}/retry. On channel-full or context
// cancellation it deletes the just-inserted row and returns the error so
// the caller surfaces back-pressure (a 503 to GitHub on webhook arrival,
// etc.) — the row never lingers as a "phantom" the next startup would
// replay. EnqueuedAt is stamped here so queue-wait time can be computed
// by the engine.
func (dc *DataChannels) PushEvent(ctx context.Context, ev Event) (int64, error) {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	if dc.closed {
		return 0, ErrQueueClosed
	}
	if ev.EnqueuedAt.IsZero() {
		ev.EnqueuedAt = time.Now()
	}
	blob, err := json.Marshal(ev)
	if err != nil {
		return 0, fmt.Errorf("marshal event: %w", err)
	}
	id, err := dc.store.EnqueueEvent(string(blob))
	if err != nil {
		return 0, err
	}
	qe := QueuedEvent{ID: id, Event: ev}
	select {
	case <-ctx.Done():
		_ = dc.store.DeleteQueuedEvent(id)
		return 0, ctx.Err()
	case dc.eventQueue <- qe:
		return id, nil
	default:
		_ = dc.store.DeleteQueuedEvent(id)
		return 0, ErrEventQueueFull
	}
}

// ReplayQueued pushes a pre-persisted event onto the channel without
// going through the store. Used by the daemon's startup replay step
// where the row already exists in event_queue. Blocks if the channel
// buffer is full — startup wants every pending event delivered, even
// if it has to wait for workers to catch up.
func (dc *DataChannels) ReplayQueued(ctx context.Context, qe QueuedEvent) error {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	if dc.closed {
		return ErrQueueClosed
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case dc.eventQueue <- qe:
		return nil
	}
}

func (dc *DataChannels) EventChan() <-chan QueuedEvent {
	return dc.eventQueue
}

// Store returns the data-access store the channel persists through.
// Workers use it to mark events started/completed.
func (dc *DataChannels) Store() *store.Store { return dc.store }

// QueueStat describes the current depth and capacity of the event queue.
type QueueStat struct {
	Buffered int
	Capacity int
}

// QueueStats returns the current depth and capacity of the event queue.
// Reading channel length and capacity is safe without holding the mutex
// because these are intrinsic properties of the channel value and the
// lock only guards the closed flag and send operations.
func (dc *DataChannels) QueueStats() QueueStat {
	return QueueStat{len(dc.eventQueue), cap(dc.eventQueue)}
}

// Close shuts down the event queue. The write lock ensures no in-flight
// Push call can race with channel closure. Subsequent Close calls are
// safe because the closed flag is checked first.
func (dc *DataChannels) Close() {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if dc.closed {
		return
	}
	dc.closed = true
	close(dc.eventQueue)
}

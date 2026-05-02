package workflow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPushAfterCloseReturnsErrQueueClosed(t *testing.T) {
	t.Parallel()
	dc := NewDataChannels(4, newTempStore(t))
	dc.Close()

	if _, err := dc.PushEvent(context.Background(), Event{}); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("PushEvent after close: got %v, want ErrQueueClosed", err)
	}
}

func TestDoubleCloseDoesNotPanic(t *testing.T) {
	t.Parallel()
	dc := NewDataChannels(4, newTempStore(t))
	dc.Close()
	dc.Close() // must not panic
}

func TestConcurrentPushAndCloseDoesNotPanic(t *testing.T) {
	t.Parallel()
	dc := NewDataChannels(64, newTempStore(t))
	var wg sync.WaitGroup

	// Spawn writers that push continuously until they get ErrQueueClosed or
	// the channel buffer fills up.
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				_, err := dc.PushEvent(context.Background(), Event{})
				if err != nil {
					return
				}
			}
		}()
	}

	// Close while writers are still active — must not panic.
	dc.Close()
	wg.Wait()
}

// TestPushEventPersistsBeforeChannelSend verifies the durable-queue
// contract: every accepted event has a row in event_queue with a
// non-zero id by the time PushEvent returns. The dequeued QueuedEvent
// must carry that same id so workers can mark started/completed.
func TestPushEventPersistsBeforeChannelSend(t *testing.T) {
	t.Parallel()
	st := newTempStore(t)
	dc := NewDataChannels(4, st)

	ev := Event{Repo: RepoRef{FullName: "owner/repo", Enabled: true}, Kind: "issues.labeled", Number: 7}
	id, err := dc.PushEvent(context.Background(), ev)
	if err != nil {
		t.Fatalf("push: %v", err)
	}

	pending, err := st.PendingEventIDs()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %v, want exactly 1 id", pending)
	}
	if id != pending[0] {
		t.Errorf("PushEvent returned id %d, want persisted id %d", id, pending[0])
	}

	select {
	case qe := <-dc.EventChan():
		if qe.ID != pending[0] {
			t.Errorf("queued id = %d, want persisted id %d", qe.ID, pending[0])
		}
		if qe.Event.Kind != ev.Kind || qe.Event.Number != ev.Number {
			t.Errorf("dequeued event = %+v, want %+v", qe.Event, ev)
		}
		if qe.Event.EnqueuedAt.IsZero() {
			t.Error("EnqueuedAt is zero, want PushEvent to stamp it")
		}
	case <-time.After(time.Second):
		t.Fatal("expected QueuedEvent on channel, none arrived")
	}
}

// TestPushEventOnFullChannelDeletesRow verifies that a buffer-full
// rejection rolls back the SQLite insert so the next startup replay
// does not pick up an event the runtime never accepted.
func TestPushEventOnFullChannelDeletesRow(t *testing.T) {
	t.Parallel()
	st := newTempStore(t)
	dc := NewDataChannels(1, st)

	if _, err := dc.PushEvent(context.Background(), Event{Kind: "a"}); err != nil {
		t.Fatalf("first push: %v", err)
	}
	// Second push must hit the default branch and roll back.
	if _, err := dc.PushEvent(context.Background(), Event{Kind: "b"}); !errors.Is(err, ErrEventQueueFull) {
		t.Fatalf("second push err = %v, want ErrEventQueueFull", err)
	}

	pending, err := st.PendingEventIDs()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %v, want exactly the accepted event", pending)
	}
}

// TestReplayQueuedDeliversWithoutPersisting verifies the startup
// replay path: ReplayQueued must hand the pre-persisted QueuedEvent
// straight to a worker without inserting a duplicate row.
func TestReplayQueuedDeliversWithoutPersisting(t *testing.T) {
	t.Parallel()
	st := newTempStore(t)
	dc := NewDataChannels(2, st)

	id, err := st.EnqueueEvent(`{"kind":"issues.labeled"}`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	qe := QueuedEvent{ID: id, Event: Event{Kind: "issues.labeled"}}
	if err := dc.ReplayQueued(context.Background(), qe); err != nil {
		t.Fatalf("replay: %v", err)
	}

	select {
	case got := <-dc.EventChan():
		if got.ID != id {
			t.Errorf("replayed id = %d, want %d", got.ID, id)
		}
	case <-time.After(time.Second):
		t.Fatal("ReplayQueued did not deliver onto the channel")
	}

	// The pending list must still contain exactly the seeded row — no
	// extra row should have been inserted by the replay path.
	pending, err := st.PendingEventIDs()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0] != id {
		t.Fatalf("pending = %v, want [%d]", pending, id)
	}
}

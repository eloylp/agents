package workflow

import (
	"context"
	"sync"
	"testing"
)

func TestPushAfterCloseReturnsErrQueueClosed(t *testing.T) {
	t.Parallel()
	dc := NewDataChannels(4, 4)
	dc.Close()

	if err := dc.PushIssue(context.Background(), IssueRequest{}); err != ErrQueueClosed {
		t.Fatalf("PushIssue after close: got %v, want ErrQueueClosed", err)
	}
	if err := dc.PushPR(context.Background(), PRRequest{}); err != ErrQueueClosed {
		t.Fatalf("PushPR after close: got %v, want ErrQueueClosed", err)
	}
}

func TestDoubleCloseDoesNotPanic(t *testing.T) {
	t.Parallel()
	dc := NewDataChannels(4, 4)
	dc.Close()
	dc.Close() // must not panic
}

func TestConcurrentPushAndCloseDoesNotPanic(t *testing.T) {
	t.Parallel()
	dc := NewDataChannels(64, 64)
	var wg sync.WaitGroup

	// Spawn writers that push continuously until they get ErrQueueClosed or
	// the channel buffer fills up.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				err := dc.PushIssue(context.Background(), IssueRequest{})
				if err != nil {
					return
				}
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				err := dc.PushPR(context.Background(), PRRequest{})
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

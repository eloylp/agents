package workflow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

type stubProcessorHandler struct {
	mu    sync.Mutex
	calls int
}

func (s *stubProcessorHandler) HandleEvent(_ context.Context, _ Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return nil
}

// TestProcessorProcessingCtxReturnsDrainContextWithDeadline is the regression
// test for issue #36. It simulates the race window where the run ctx is already
// cancelled but setDrainCtx has not yet been called, and asserts that
// processingCtx blocks until the real drain context (with a live deadline) is
// installed, not an already-cancelled sentinel.
func TestProcessorProcessingCtxReturnsDrainContextWithDeadline(t *testing.T) {
	t.Parallel()
	dataChannels := NewDataChannels(1, newTempStore(t))
	processor := NewProcessor(dataChannels, &stubProcessorHandler{}, 1, time.Second, zerolog.Nop())

	// Simulate shutdown: run ctx is cancelled.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// processingCtx must block until setDrainCtx is called.
	var got context.Context
	done := make(chan struct{})
	go func() {
		defer close(done)
		got = processor.processingCtx(cancelledCtx)
	}()

	// Simulate the brief gap before Run installs the real drain context.
	time.Sleep(20 * time.Millisecond)

	// Confirm the goroutine is still blocked (has not yet returned).
	select {
	case <-done:
		t.Fatal("processingCtx returned before setDrainCtx was called; expected it to block")
	default:
	}

	// Install the real drain context, this should unblock processingCtx.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer drainCancel()
	processor.setDrainCtx(drainCtx)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("processingCtx did not unblock after setDrainCtx")
	}

	// The returned context must be the live drain context: not yet cancelled
	// and carrying the shutdown deadline.
	if got.Err() != nil {
		t.Errorf("processingCtx returned an already-cancelled context; want a live drain context")
	}
	if _, hasDeadline := got.Deadline(); !hasDeadline {
		t.Errorf("processingCtx returned a context without a deadline; want the drain context with shutdown deadline")
	}
}

func TestProcessorRunDrainsQueueOnCancellation(t *testing.T) {
	t.Parallel()
	dataChannels := NewDataChannels(4, newTempStore(t))
	handler := &stubProcessorHandler{}
	processor := NewProcessor(dataChannels, handler, 1, time.Second, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = processor.Run(ctx)
		close(done)
	}()

	if _, err := dataChannels.PushEvent(context.Background(), Event{Repo: RepoRef{FullName: "owner/repo"}, Kind: "issues.labeled", Number: 1}); err != nil {
		t.Fatalf("push issue event: %v", err)
	}
	if _, err := dataChannels.PushEvent(context.Background(), Event{Repo: RepoRef{FullName: "owner/repo"}, Kind: "pull_request.labeled", Number: 2}); err != nil {
		t.Fatalf("push pr event: %v", err)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("processor workers did not finish before timeout")
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.calls != 2 {
		t.Fatalf("expected 2 calls, got %d", handler.calls)
	}
}

// blockingProcessorHandler blocks until released, tracking peak concurrency.
type blockingProcessorHandler struct {
	mu         sync.Mutex
	calls      int
	peak       int
	active     int
	blockUntil chan struct{}
}

func newBlockingProcessorHandler() *blockingProcessorHandler {
	return &blockingProcessorHandler{blockUntil: make(chan struct{})}
}

func (b *blockingProcessorHandler) HandleEvent(_ context.Context, _ Event) error {
	b.mu.Lock()
	b.active++
	b.calls++
	if b.active > b.peak {
		b.peak = b.active
	}
	b.mu.Unlock()
	<-b.blockUntil
	b.mu.Lock()
	b.active--
	b.mu.Unlock()
	return nil
}

// TestProcessorWorkerPoolAllowsConcurrentProcessing verifies that with
// workers=N, up to N events can be handled concurrently rather than being
// serialized through a single goroutine.
func TestProcessorWorkerPoolAllowsConcurrentProcessing(t *testing.T) {
	t.Parallel()
	const workers = 3
	const events = workers // saturate the pool

	dataChannels := NewDataChannels(events*2, newTempStore(t))
	handler := newBlockingProcessorHandler()
	processor := NewProcessor(dataChannels, handler, workers, 5*time.Second, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = processor.Run(ctx)
		close(done)
	}()

	// Push enough events to fill all workers.
	for i := range events {
		if _, err := dataChannels.PushEvent(context.Background(), Event{
			Repo:   RepoRef{FullName: "owner/repo"},
			Kind:   "issues.labeled",
			Number: i + 1,
		}); err != nil {
			t.Fatalf("push event %d: %v", i, err)
		}
	}

	// Wait until all worker slots are occupied.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		handler.mu.Lock()
		c := handler.calls
		handler.mu.Unlock()
		if c >= workers {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	handler.mu.Lock()
	peak := handler.peak
	handler.mu.Unlock()

	if peak < workers {
		t.Errorf("expected peak concurrency >= %d, got %d", workers, peak)
	}

	// Release all blocked handlers and let the processor drain.
	close(handler.blockUntil)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("processor did not stop after cancel")
	}
}

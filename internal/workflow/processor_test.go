package workflow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

type stubProcessorHandler struct {
	mu         sync.Mutex
	issueCalls int
	prCalls    int
}

func (s *stubProcessorHandler) HandleIssueLabelEvent(_ context.Context, _ IssueRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issueCalls++
	return nil
}

func (s *stubProcessorHandler) HandlePullRequestLabelEvent(_ context.Context, _ PRRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prCalls++
	return nil
}

// TestProcessorProcessingCtxReturnsDrainContextWithDeadline is the regression
// test for issue #36. It simulates the race window where the run ctx is already
// cancelled but setDrainCtx has not yet been called, and asserts that
// processingCtx blocks until the real drain context (with a live deadline) is
// installed — not an already-cancelled sentinel.
func TestProcessorProcessingCtxReturnsDrainContextWithDeadline(t *testing.T) {
	t.Parallel()
	dataChannels := NewDataChannels(1, 1)
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

	// Install the real drain context — this should unblock processingCtx.
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

func TestProcessorRunDrainsQueuesOnCancellation(t *testing.T) {
	t.Parallel()
	dataChannels := NewDataChannels(4, 4)
	handler := &stubProcessorHandler{}
	processor := NewProcessor(dataChannels, handler, 1, time.Second, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = processor.Run(ctx)
		close(done)
	}()

	if err := dataChannels.PushIssue(context.Background(), IssueRequest{Repo: RepoRef{FullName: "owner/repo"}, Issue: Issue{Number: 1}, Label: "ai:refine"}); err != nil {
		t.Fatalf("push issue: %v", err)
	}
	if err := dataChannels.PushPR(context.Background(), PRRequest{Repo: RepoRef{FullName: "owner/repo"}, PR: PullRequest{Number: 2}, Label: "ai:review"}); err != nil {
		t.Fatalf("push pr: %v", err)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("processor workers did not finish before timeout")
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.issueCalls != 1 || handler.prCalls != 1 {
		t.Fatalf("expected 1 call each, got issue=%d pr=%d", handler.issueCalls, handler.prCalls)
	}
}

// blockingProcessorHandler blocks until released, tracking peak concurrency.
type blockingProcessorHandler struct {
	mu          sync.Mutex
	issueCalls  int
	prCalls     int
	issuePeak   int
	prPeak      int
	issueActive int
	prActive    int
	blockUntil  chan struct{}
}

func newBlockingProcessorHandler() *blockingProcessorHandler {
	return &blockingProcessorHandler{blockUntil: make(chan struct{})}
}

func (b *blockingProcessorHandler) HandleIssueLabelEvent(_ context.Context, _ IssueRequest) error {
	b.mu.Lock()
	b.issueActive++
	b.issueCalls++
	if b.issueActive > b.issuePeak {
		b.issuePeak = b.issueActive
	}
	b.mu.Unlock()
	<-b.blockUntil
	b.mu.Lock()
	b.issueActive--
	b.mu.Unlock()
	return nil
}

func (b *blockingProcessorHandler) HandlePullRequestLabelEvent(_ context.Context, _ PRRequest) error {
	b.mu.Lock()
	b.prActive++
	b.prCalls++
	if b.prActive > b.prPeak {
		b.prPeak = b.prActive
	}
	b.mu.Unlock()
	<-b.blockUntil
	b.mu.Lock()
	b.prActive--
	b.mu.Unlock()
	return nil
}

// TestProcessorWorkerPoolAllowsConcurrentProcessing verifies that with
// workers=N, up to N issue events and N PR events can be handled concurrently
// rather than being serialized through a single goroutine.
func TestProcessorWorkerPoolAllowsConcurrentProcessing(t *testing.T) {
	t.Parallel()
	const workers = 3
	const events = workers // saturate the pool

	dataChannels := NewDataChannels(events*2, events*2)
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
		if err := dataChannels.PushIssue(context.Background(), IssueRequest{
			Repo:  RepoRef{FullName: "owner/repo"},
			Issue: Issue{Number: i + 1},
			Label: "ai:refine",
		}); err != nil {
			t.Fatalf("push issue %d: %v", i, err)
		}
		if err := dataChannels.PushPR(context.Background(), PRRequest{
			Repo:  RepoRef{FullName: "owner/repo"},
			PR:    PullRequest{Number: i + 1},
			Label: "ai:review",
		}); err != nil {
			t.Fatalf("push pr %d: %v", i, err)
		}
	}

	// Wait until all worker slots are occupied.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		handler.mu.Lock()
		ic, pc := handler.issueCalls, handler.prCalls
		handler.mu.Unlock()
		if ic >= workers && pc >= workers {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	handler.mu.Lock()
	issuePeak := handler.issuePeak
	prPeak := handler.prPeak
	handler.mu.Unlock()

	if issuePeak < workers {
		t.Errorf("expected issue peak concurrency >= %d, got %d", workers, issuePeak)
	}
	if prPeak < workers {
		t.Errorf("expected PR peak concurrency >= %d, got %d", workers, prPeak)
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

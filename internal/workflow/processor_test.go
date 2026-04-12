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
	processor := NewProcessor(dataChannels, &stubProcessorHandler{}, time.Second, zerolog.Nop())

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
	dataChannels := NewDataChannels(4, 4)
	handler := &stubProcessorHandler{}
	processor := NewProcessor(dataChannels, handler, time.Second, zerolog.Nop())

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

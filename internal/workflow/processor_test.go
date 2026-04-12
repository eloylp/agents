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

// TestProcessorProcessingCtxDrainCtxNeverNil verifies that processingCtx
// always returns a non-nil context and never panics, even before the real drain
// context is installed by Run. This guards against the race window described in
// issue #36: between ctx cancellation and setDrainCtx being called in Run, a
// worker that dequeues an item must not receive a nil fallback.
func TestProcessorProcessingCtxDrainCtxNeverNil(t *testing.T) {
	t.Parallel()
	dataChannels := NewDataChannels(1, 1)
	processor := NewProcessor(dataChannels, &stubProcessorHandler{}, time.Second, zerolog.Nop())

	// Simulate the race window: run ctx is already cancelled but setDrainCtx
	// has not been called yet. processingCtx must still return a valid context.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	got := processor.processingCtx(cancelledCtx)
	if got == nil {
		t.Fatal("processingCtx returned nil; want a non-nil context")
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

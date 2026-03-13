package workflow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
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

func TestProcessorStartStopDrainsQueues(t *testing.T) {
	dataChannels := NewDataChannels(4, 4)
	handler := &stubProcessorHandler{}
	var wg sync.WaitGroup
	processor := NewProcessor(dataChannels, handler, &wg, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	channels := processor.Start(ctx)

	if err := channels.PushIssue(context.Background(), IssueRequest{Repo: config.RepoConfig{FullName: "owner/repo"}, Issue: Issue{Number: 1}, Label: "ai:refine"}); err != nil {
		t.Fatalf("push issue: %v", err)
	}
	if err := channels.PushPR(context.Background(), PRRequest{Repo: config.RepoConfig{FullName: "owner/repo"}, PR: PullRequest{Number: 2}, Label: "ai:review"}); err != nil {
		t.Fatalf("push pr: %v", err)
	}

	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	processor.Stop(stopCtx)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

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

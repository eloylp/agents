package workflow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/ai/testutil"
	"github.com/eloylp/agents/internal/config"
)

type stubRunner struct {
	calls int
	last  ai.Request
}

func (s *stubRunner) Run(_ context.Context, req ai.Request) (ai.Response, error) {
	s.calls++
	s.last = req
	return ai.Response{Artifacts: []ai.Artifact{{Type: "issue_comment", PartKey: "p", GitHubID: "1"}}}, nil
}

func TestHandleIssueLabelEventUsesPayloadLabel(t *testing.T) {
	runner := &stubRunner{}
	cfg := &config.Config{
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
			"codex":  {},
		},
		Agents: []config.AgentConfig{
			{Name: "architect", Skills: []string{"architect"}},
		},
		Processor: config.ProcessorConfig{MaxConcurrentAgents: 4},
	}
	promptStore := testutil.BuildPromptStore(t, []string{"architect"}, nil)
	engine := NewEngine(cfg, map[string]ai.Runner{"claude": runner, "codex": runner}, promptStore, zerolog.Nop())
	issue := Issue{
		Number: 10,
	}

	err := engine.HandleIssueLabelEvent(context.Background(), IssueRequest{
		Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
		Issue: issue,
		Label: "ai:refine:codex",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("expected one runner call, got %d", runner.calls)
	}
	if runner.last.Workflow != "issue_refine:codex" {
		t.Fatalf("expected event label backend codex, got %q", runner.last.Workflow)
	}
}

// TestHandleIssueLabelEventAutoBackend verifies that the "auto" token in a
// label resolves to the default configured backend instead of being treated as
// an unknown backend and silently dropped.
func TestHandleIssueLabelEventAutoBackend(t *testing.T) {
	runner := &stubRunner{}
	// Only claude is configured; "auto" must resolve to it.
	cfg := &config.Config{
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
		},
		Agents: []config.AgentConfig{
			{Name: "architect", Skills: []string{"architect"}},
		},
	}
	promptStore := testutil.BuildPromptStore(t, []string{"architect"}, nil)
	engine := NewEngine(cfg, map[string]ai.Runner{"claude": runner}, promptStore, zerolog.Nop())

	err := engine.HandleIssueLabelEvent(context.Background(), IssueRequest{
		Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
		Issue: Issue{Number: 1},
		Label: "ai:refine:auto",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("expected one runner call for auto backend, got %d", runner.calls)
	}
	if runner.last.Workflow != "issue_refine:claude" {
		t.Fatalf("auto backend should resolve to claude, got workflow %q", runner.last.Workflow)
	}
}

// concurrencyTrackingRunner records the peak number of concurrent Run calls.
type concurrencyTrackingRunner struct {
	mu      sync.Mutex
	current int
	peak    int
	// blockUntil is closed to unblock all in-flight Run calls simultaneously,
	// ensuring overlap is measurable.
	blockUntil chan struct{}
}

func newConcurrencyTrackingRunner() *concurrencyTrackingRunner {
	return &concurrencyTrackingRunner{blockUntil: make(chan struct{})}
}

func (r *concurrencyTrackingRunner) Run(_ context.Context, _ ai.Request) (ai.Response, error) {
	r.mu.Lock()
	r.current++
	if r.current > r.peak {
		r.peak = r.current
	}
	r.mu.Unlock()

	<-r.blockUntil // hold the slot until the test releases all callers

	r.mu.Lock()
	r.current--
	r.mu.Unlock()
	return ai.Response{}, nil
}

// TestHandlePRLabelEventRespectsConcurrencyLimit verifies that when
// agent=all fans out to more agents than MaxConcurrentAgents, at most
// MaxConcurrentAgents subprocesses run at any one time.
func TestHandlePRLabelEventRespectsConcurrencyLimit(t *testing.T) {
	t.Parallel()
	const limit = 2
	agents := []string{"architect", "scout", "coder", "reviewer"}

	tracker := newConcurrencyTrackingRunner()
	cfg := &config.Config{
		AIBackends: map[string]config.AIBackendConfig{"claude": {}},
		Agents: []config.AgentConfig{
			{Name: "architect", Skills: []string{"architect"}},
			{Name: "scout", Skills: []string{"scout"}},
			{Name: "coder", Skills: []string{"coder"}},
			{Name: "reviewer", Skills: []string{"reviewer"}},
		},
		Processor: config.ProcessorConfig{MaxConcurrentAgents: limit},
	}
	promptStore := testutil.BuildPromptStore(t, agents, nil)
	engine := NewEngine(cfg, map[string]ai.Runner{"claude": tracker}, promptStore, zerolog.Nop())

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = engine.HandlePullRequestLabelEvent(context.Background(), PRRequest{
			Repo:  RepoRef{FullName: "owner/repo"},
			PR:    PullRequest{Number: 1},
			Label: "ai:review",
		})
	}()

	// Give the fan-out time to fill up to the limit and block.
	time.Sleep(50 * time.Millisecond)

	tracker.mu.Lock()
	peakSoFar := tracker.peak
	tracker.mu.Unlock()

	if peakSoFar > limit {
		t.Errorf("peak concurrency %d exceeds limit %d before all agents were released", peakSoFar, limit)
	}

	// Release all blocked runners so the handler can complete.
	close(tracker.blockUntil)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("HandlePullRequestLabelEvent did not complete after releasing runners")
	}

	if tracker.peak > limit {
		t.Errorf("peak concurrency %d exceeds configured limit %d", tracker.peak, limit)
	}
}

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

func intPtr(v int) *int { return &v }

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
		Processor: config.ProcessorConfig{MaxConcurrentAgents: intPtr(4)},
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
	// started is sent to once each time a Run call begins, allowing tests to
	// synchronize on exactly N calls being in-flight simultaneously.
	started chan struct{}
	// blockUntil is closed to unblock all in-flight Run calls simultaneously,
	// ensuring overlap is measurable.
	blockUntil chan struct{}
}

func newConcurrencyTrackingRunner() *concurrencyTrackingRunner {
	return &concurrencyTrackingRunner{
		started:    make(chan struct{}, 16),
		blockUntil: make(chan struct{}),
	}
}

func (r *concurrencyTrackingRunner) Run(_ context.Context, _ ai.Request) (ai.Response, error) {
	r.mu.Lock()
	r.current++
	if r.current > r.peak {
		r.peak = r.current
	}
	r.mu.Unlock()

	r.started <- struct{}{} // notify test that one more Run is in-flight

	<-r.blockUntil // hold the slot until the test releases all callers

	r.mu.Lock()
	r.current--
	r.mu.Unlock()
	return ai.Response{}, nil
}

// TestHandlePRLabelEventRespectsConcurrencyLimit verifies that when
// agent=all fans out to more agents than MaxConcurrentAgents, the semaphore
// saturates at exactly the configured limit before any call is released.
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
		Processor: config.ProcessorConfig{MaxConcurrentAgents: intPtr(limit)},
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

	// Wait for exactly `limit` Run calls to start and block, proving the
	// semaphore has been fully saturated before we inspect peak.
	for i := 0; i < limit; i++ {
		select {
		case <-tracker.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for Run call %d/%d to start", i+1, limit)
		}
	}

	tracker.mu.Lock()
	peakAtSaturation := tracker.peak
	tracker.mu.Unlock()

	if peakAtSaturation != limit {
		t.Errorf("expected peak == limit (%d) when semaphore is saturated, got %d", limit, peakAtSaturation)
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

// TestNewEngineZeroMaxConcurrentAgentsFallsBackToDefault verifies that
// NewEngine applied to a zero-value Config (as test code often builds)
// does not create a semaphore with weight 0, which would block every
// Acquire call indefinitely.
func TestNewEngineZeroMaxConcurrentAgentsFallsBackToDefault(t *testing.T) {
	t.Parallel()
	// Config with MaxConcurrentAgents unset (zero value).
	cfg := &config.Config{
		AIBackends: map[string]config.AIBackendConfig{"claude": {}},
		Agents:     []config.AgentConfig{{Name: "architect", Skills: []string{"architect"}}},
	}
	runner := &stubRunner{}
	promptStore := testutil.BuildPromptStore(t, []string{"architect"}, nil)
	engine := NewEngine(cfg, map[string]ai.Runner{"claude": runner}, promptStore, zerolog.Nop())

	if engine.maxConcurrentAgents <= 0 {
		t.Fatalf("expected maxConcurrentAgents > 0, got %d", engine.maxConcurrentAgents)
	}

	// A PR-review fan-out must complete without deadlocking.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := engine.HandlePullRequestLabelEvent(ctx, PRRequest{
		Repo:  RepoRef{FullName: "owner/repo"},
		PR:    PullRequest{Number: 1},
		Label: "ai:review",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

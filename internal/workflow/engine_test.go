package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/ai/testutil"
	"github.com/eloylp/agents/internal/config"
)

func intPtr(v int) *int { return &v }

// stubRunner records all Run invocations and optionally delegates to runFn for
// error injection. It is safe for concurrent use from multiple goroutines.
type stubRunner struct {
	mu    sync.Mutex
	calls []ai.Request
	runFn func(ai.Request) error // if nil, every Run succeeds
}

func (s *stubRunner) Run(_ context.Context, req ai.Request) (ai.Response, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()
	if s.runFn != nil {
		if err := s.runFn(req); err != nil {
			return ai.Response{}, err
		}
	}
	return ai.Response{Artifacts: []ai.Artifact{{Type: "issue_comment", PartKey: "p", GitHubID: "1"}}}, nil
}

func (s *stubRunner) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *stubRunner) workflows() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	for i, c := range s.calls {
		out[i] = c.Workflow
	}
	return out
}

func TestHandleIssueLabelEventUsesPayloadLabel(t *testing.T) {
	t.Parallel()
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
	if runner.callCount() != 1 {
		t.Fatalf("expected one runner call, got %d", runner.callCount())
	}
	if wf := runner.workflows()[0]; wf != "issue_refine:codex" {
		t.Fatalf("expected event label backend codex, got %q", wf)
	}
}

func TestHandlePullRequestLabelEvent(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
			"codex":  {},
		},
		Agents: []config.AgentConfig{
			{Name: "architect", Skills: []string{"architect"}},
			{Name: "scout", Skills: []string{"scout"}},
		},
	}
	promptStore := testutil.BuildPromptStore(t, []string{"architect", "scout"}, nil)

	tests := []struct {
		name            string
		pr              PullRequest
		label           string
		runFn           func(ai.Request) error
		wantCalls       int
		wantWorkflows   []string // non-empty: all returned workflows must be in this set
		wantErrContains string
	}{
		{
			name:      "draft-pr-skipped",
			pr:        PullRequest{Number: 1, Draft: true},
			label:     "ai:review:claude:architect",
			wantCalls: 0,
		},
		{
			name:      "unrecognised-label-skipped",
			pr:        PullRequest{Number: 2},
			label:     "ci:lint",
			wantCalls: 0,
		},
		{
			name:      "unknown-backend-skipped",
			pr:        PullRequest{Number: 3},
			label:     "ai:review:gpt:architect",
			wantCalls: 0,
		},
		{
			name:      "unknown-agent-skipped",
			pr:        PullRequest{Number: 4},
			label:     "ai:review:claude:unknown",
			wantCalls: 0,
		},
		{
			name:          "specific-agent-one-call",
			pr:            PullRequest{Number: 5},
			label:         "ai:review:claude:architect",
			wantCalls:     1,
			wantWorkflows: []string{"pr_review:claude:architect"},
		},
		{
			name:          "all-agents-fan-out",
			pr:            PullRequest{Number: 6},
			label:         "ai:review:claude",
			wantCalls:     2,
			wantWorkflows: []string{"pr_review:claude:architect", "pr_review:claude:scout"},
		},
		{
			name:  "one-agent-fails-others-still-run",
			pr:    PullRequest{Number: 7},
			label: "ai:review:claude",
			runFn: func(req ai.Request) error {
				if strings.HasSuffix(req.Workflow, ":architect") {
					return errors.New("architect failed")
				}
				return nil
			},
			wantCalls:       2,
			wantErrContains: "architect failed",
		},
		{
			name:  "all-agents-fail-errors-joined",
			pr:    PullRequest{Number: 8},
			label: "ai:review:claude",
			runFn: func(_ ai.Request) error {
				return errors.New("runner down")
			},
			wantCalls:       2,
			wantErrContains: "runner down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := &stubRunner{runFn: tt.runFn}
			engine := NewEngine(cfg, map[string]ai.Runner{"claude": runner, "codex": runner}, promptStore, zerolog.Nop())

			err := engine.HandlePullRequestLabelEvent(context.Background(), PRRequest{
				Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
				PR:    tt.pr,
				Label: tt.label,
			})

			if tt.wantErrContains != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrContains)
				}
				if !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("expected error %q to contain %q", err.Error(), tt.wantErrContains)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got := runner.callCount(); got != tt.wantCalls {
				t.Fatalf("runner calls: got %d, want %d", got, tt.wantCalls)
			}

			if len(tt.wantWorkflows) > 0 {
				wfSet := make(map[string]struct{}, len(tt.wantWorkflows))
				for _, wf := range tt.wantWorkflows {
					wfSet[wf] = struct{}{}
				}
				for _, wf := range runner.workflows() {
					if _, ok := wfSet[wf]; !ok {
						t.Fatalf("unexpected workflow %q; want one of %v", wf, tt.wantWorkflows)
					}
				}
			}
		})
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

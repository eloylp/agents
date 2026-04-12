package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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

// countingRunner records how many times Run is called, atomically.
type countingRunner struct {
	calls atomic.Int64
	err   error
}

func (r *countingRunner) Run(_ context.Context, _ ai.Request) (ai.Response, error) {
	r.calls.Add(1)
	if r.err != nil {
		return ai.Response{}, r.err
	}
	return ai.Response{Artifacts: []ai.Artifact{{Type: "pr_comment", PartKey: "p", GitHubID: "1"}}}, nil
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
		name               string
		pr                 PullRequest
		label              string
		runFn              func(ai.Request) error
		wantCalls          int
		wantWorkflows      []string // non-empty: exact multiset of expected workflows (each entry must appear exactly once)
		wantErrContains    string
		wantAllErrContains []string // all substrings that must appear in the joined error
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
			runFn: func(req ai.Request) error {
				if strings.HasSuffix(req.Workflow, ":architect") {
					return errors.New("architect runner down")
				}
				return errors.New("scout runner down")
			},
			wantCalls:          2,
			wantAllErrContains: []string{"architect runner down", "scout runner down"},
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
			} else if len(tt.wantAllErrContains) > 0 {
				if err == nil {
					t.Fatalf("expected error containing all of %v, got nil", tt.wantAllErrContains)
				}
				for _, sub := range tt.wantAllErrContains {
					if !strings.Contains(err.Error(), sub) {
						t.Fatalf("expected error %q to contain %q", err.Error(), sub)
					}
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got := runner.callCount(); got != tt.wantCalls {
				t.Fatalf("runner calls: got %d, want %d", got, tt.wantCalls)
			}

			if len(tt.wantWorkflows) > 0 {
				// Build a count map for the expected multiset.
				wantCounts := make(map[string]int, len(tt.wantWorkflows))
				for _, wf := range tt.wantWorkflows {
					wantCounts[wf]++
				}
				// Build a count map for what was actually dispatched.
				gotCounts := make(map[string]int)
				for _, wf := range runner.workflows() {
					gotCounts[wf]++
				}
				for wf, want := range wantCounts {
					if got := gotCounts[wf]; got != want {
						t.Fatalf("workflow %q: dispatched %d time(s), want %d", wf, got, want)
					}
				}
				for wf, got := range gotCounts {
					if _, ok := wantCounts[wf]; !ok {
						t.Fatalf("unexpected workflow %q dispatched %d time(s)", wf, got)
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
	if got := runner.callCount(); got != 1 {
		t.Fatalf("expected one runner call for auto backend, got %d", got)
	}
	if wfs := runner.workflows(); wfs[0] != "issue_refine:claude" {
		t.Fatalf("auto backend should resolve to claude, got workflow %q", wfs[0])
	}
}

func TestHandlePullRequestLabelEventInvokesAllAgents(t *testing.T) {
	t.Parallel()
	runner := &countingRunner{}
	cfg := &config.Config{
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
		},
		Agents: []config.AgentConfig{
			{Name: "architect", Skills: []string{"architect"}},
			{Name: "reviewer", Skills: []string{"reviewer"}},
		},
	}
	promptStore := testutil.BuildPromptStore(t, []string{"architect", "reviewer"}, nil)
	engine := NewEngine(cfg, map[string]ai.Runner{"claude": runner}, promptStore, zerolog.Nop())

	err := engine.HandlePullRequestLabelEvent(context.Background(), PRRequest{
		Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
		PR:    PullRequest{Number: 5},
		Label: "ai:review",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := runner.calls.Load(); got != 2 {
		t.Fatalf("expected 2 runner calls (one per agent), got %d", got)
	}
}

func TestHandlePullRequestLabelEventCollectsAllAgentErrors(t *testing.T) {
	t.Parallel()
	runner := &countingRunner{err: errors.New("backend unavailable")}
	cfg := &config.Config{
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
		},
		Agents: []config.AgentConfig{
			{Name: "architect", Skills: []string{"architect"}},
			{Name: "reviewer", Skills: []string{"reviewer"}},
		},
	}
	promptStore := testutil.BuildPromptStore(t, []string{"architect", "reviewer"}, nil)
	engine := NewEngine(cfg, map[string]ai.Runner{"claude": runner}, promptStore, zerolog.Nop())

	err := engine.HandlePullRequestLabelEvent(context.Background(), PRRequest{
		Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
		PR:    PullRequest{Number: 5},
		Label: "ai:review",
	})
	if err == nil {
		t.Fatal("expected error from failing agents, got nil")
	}
	// Both agents should have been attempted despite the first one erroring.
	if got := runner.calls.Load(); got != 2 {
		t.Fatalf("expected 2 runner calls, got %d — one agent may have been skipped", got)
	}
	// Both agent-specific error prefixes must appear in the joined error.
	errStr := err.Error()
	for _, agent := range []string{"architect", "reviewer"} {
		wantSubstr := fmt.Sprintf("agent claude/%s:", agent)
		if !strings.Contains(errStr, wantSubstr) {
			t.Errorf("expected error to contain %q; got: %v", wantSubstr, err)
		}
	}
	if !errors.Is(err, runner.err) {
		t.Errorf("expected errors.Is match for runner.err; got %v", err)
	}
}

func TestNewEngineDefaultsMaxConcurrentAgentsWhenZeroValue(t *testing.T) {
	t.Parallel()
	// A zero-value config (as created in unit tests without config.Load) must
	// not deadlock: NewEngine must fall back to defaultMaxConcurrentAgents.
	cfg := &config.Config{}
	engine := NewEngine(cfg, nil, nil, zerolog.Nop())
	if engine.maxConcurrentAgents != defaultMaxConcurrentAgents {
		t.Fatalf("expected maxConcurrentAgents=%d for zero-value config, got %d",
			defaultMaxConcurrentAgents, engine.maxConcurrentAgents)
	}
}

// blockingRunner blocks until released, allowing control over in-flight goroutine count.
type blockingRunner struct {
	calls  atomic.Int64
	gate   chan struct{} // close to unblock all
	start  chan struct{} // receives a token for each Run that starts
}

func (r *blockingRunner) Run(_ context.Context, _ ai.Request) (ai.Response, error) {
	r.calls.Add(1)
	r.start <- struct{}{}
	<-r.gate
	return ai.Response{Artifacts: []ai.Artifact{{Type: "pr_comment", PartKey: "p", GitHubID: "1"}}}, nil
}

func TestHandlePullRequestLabelEventRespectsMaxConcurrentAgents(t *testing.T) {
	t.Parallel()

	const agentCount = 4
	const cap = 2 // semaphore cap — only 2 agents may run at once

	agents := make([]config.AgentConfig, agentCount)
	skills := make([]string, agentCount)
	for i := range agents {
		name := fmt.Sprintf("agent%d", i)
		agents[i] = config.AgentConfig{Name: name, Skills: []string{name}}
		skills[i] = name
	}

	capVal := cap
	cfg := &config.Config{
		AIBackends: map[string]config.AIBackendConfig{"claude": {}},
		Agents:     agents,
		Processor:  config.ProcessorConfig{MaxConcurrentAgents: &capVal},
	}
	promptStore := testutil.BuildPromptStore(t, skills, nil)

	runner := &blockingRunner{
		gate:  make(chan struct{}),
		start: make(chan struct{}, agentCount),
	}
	engine := NewEngine(cfg, map[string]ai.Runner{"claude": runner}, promptStore, zerolog.Nop())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = engine.HandlePullRequestLabelEvent(context.Background(), PRRequest{
			Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
			PR:    PullRequest{Number: 1},
			Label: "ai:review",
		})
	}()

	// Wait for exactly `cap` goroutines to start, confirming the semaphore is
	// saturated and the remaining agents are queued behind it.
	for i := 0; i < cap; i++ {
		<-runner.start
	}
	if got := runner.calls.Load(); got != int64(cap) {
		t.Fatalf("expected exactly %d concurrent runners with cap=%d, got %d", cap, cap, got)
	}

	// Unblock all runners and wait for the handler to finish.
	close(runner.gate)
	wg.Wait()

	if got := runner.calls.Load(); got != int64(agentCount) {
		t.Fatalf("expected all %d agents to run, got %d", agentCount, got)
	}
}

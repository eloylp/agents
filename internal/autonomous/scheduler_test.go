package autonomous

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
)

type stubRunner struct {
	mu        sync.Mutex
	calls     int
	workflows []string
}

func (s *stubRunner) Run(_ context.Context, req ai.Request) (ai.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.workflows = append(s.workflows, req.Workflow)
	return ai.Response{}, nil
}

// blockingRunner signals on ready when a run starts, then blocks until block is closed.
type blockingRunner struct {
	mu    sync.Mutex
	calls int
	ready chan struct{}
	block chan struct{}
}

func (b *blockingRunner) Run(_ context.Context, _ ai.Request) (ai.Response, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	b.ready <- struct{}{}
	<-b.block
	return ai.Response{}, nil
}

// baseCfg returns a minimal valid Config suitable for scheduler tests. Use
// `modify` to tailor the repo bindings.
func baseCfg(modify func(*config.Config)) *config.Config {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			AIBackends: map[string]config.AIBackendConfig{
				"claude": {Command: "claude"},
			},
			MemoryDir: "/tmp/agent-memory",
		},
		Skills: map[string]config.SkillDef{
			"architect": {Prompt: "Focus on architecture."},
		},
		Agents: []config.AgentDef{
			{Name: "reviewer", Backend: "claude", Skills: []string{"architect"}, Prompt: "Review PRs."},
		},
		Repos: []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []config.Binding{
					{Agent: "reviewer", Cron: "* * * * *"},
				},
			},
		},
	}
	if modify != nil {
		modify(cfg)
	}
	return cfg
}

func TestNewSchedulerEntryRegistration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mutate    func(*config.Config)
		wantCount int
	}{
		{
			name:      "cron binding registered",
			wantCount: 1,
		},
		{
			name:      "skips disabled repo",
			mutate:    func(c *config.Config) { c.Repos[0].Enabled = false },
			wantCount: 0,
		},
		{
			name: "skips disabled binding",
			mutate: func(c *config.Config) {
				f := false
				c.Repos[0].Use[0].Enabled = &f
			},
			wantCount: 0,
		},
		{
			name: "skips label-only binding",
			mutate: func(c *config.Config) {
				c.Repos[0].Use[0] = config.Binding{Agent: "reviewer", Labels: []string{"ai:review"}}
			},
			wantCount: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := NewScheduler(baseCfg(tc.mutate), map[string]ai.Runner{"claude": &stubRunner{}}, NewMemoryStore(t.TempDir()), zerolog.Nop())
			if err != nil {
				t.Fatalf("NewScheduler: %v", err)
			}
			if len(s.agentEntries) != tc.wantCount {
				t.Errorf("agentEntries = %d, want %d", len(s.agentEntries), tc.wantCount)
			}
		})
	}
}

func TestTriggerAgentRunsSynchronously(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	runner := &stubRunner{}
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err != nil {
		t.Fatalf("TriggerAgent: %v", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.calls != 1 {
		t.Errorf("expected 1 runner call, got %d", runner.calls)
	}
	if len(runner.workflows) == 0 || !strings.HasPrefix(runner.workflows[0], "autonomous:claude:reviewer") {
		t.Errorf("unexpected workflow tag: %v", runner.workflows)
	}
}

func TestTriggerAgentRejectsUnboundAgent(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(func(c *config.Config) {
		c.Agents = append(c.Agents, config.AgentDef{Name: "orphan", Backend: "claude", Prompt: "x"})
	})
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": &stubRunner{}}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	err = s.TriggerAgent(context.Background(), "orphan", "owner/repo")
	if err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Errorf("expected not-bound error, got %v", err)
	}
}

func TestTriggerAgentRejectsUnknownAgent(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": &stubRunner{}}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	err = s.TriggerAgent(context.Background(), "ghost", "owner/repo")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

func TestTriggerAgentRejectsDisabledRepo(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(func(c *config.Config) { c.Repos[0].Enabled = false })
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": &stubRunner{}}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	err = s.TriggerAgent(context.Background(), "reviewer", "owner/repo")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("expected disabled error, got %v", err)
	}
}

func TestResolveBackendAutoFallsBackToDefault(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(func(c *config.Config) { c.Agents[0].Backend = "auto" })
	runner := &stubRunner{}
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err != nil {
		t.Fatalf("TriggerAgent: %v", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.calls != 1 {
		t.Errorf("expected auto to resolve to claude and run once, got %d calls", runner.calls)
	}
}

func TestSchedulerSkipsJobWhenPreviousRunStillRunning(t *testing.T) {
	t.Parallel()
	ready := make(chan struct{}, 1)
	block := make(chan struct{})
	runner := &blockingRunner{ready: ready, block: block}
	cfg := baseCfg(nil)

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	wrappedJob := s.cron.Entry(s.agentEntries[0].cronID).WrappedJob

	done := make(chan struct{})
	go func() {
		defer close(done)
		wrappedJob.Run()
	}()
	<-ready
	// Second invocation must be skipped while the first is still running.
	wrappedJob.Run()
	close(block)
	<-done

	runner.mu.Lock()
	calls := runner.calls
	runner.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected 1 invocation (second skipped), got %d", calls)
	}
}

// promptCapturingRunner records the prompt from each Run call for inspection.
type promptCapturingRunner struct {
	mu      sync.Mutex
	prompts []string
}

func (r *promptCapturingRunner) Run(_ context.Context, req ai.Request) (ai.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prompts = append(r.prompts, req.Prompt)
	return ai.Response{}, nil
}

func TestSchedulerAllowPRsPromptPrefixing(t *testing.T) {
	t.Parallel()
	const noPRPrefix = "Do not open or create pull requests under any circumstances."
	tests := []struct {
		name     string
		allowPRs bool
		prompt   string
		wantNoPR bool
	}{
		{
			name:     "no-PR instruction prepended when allow_prs=false",
			allowPRs: false,
			prompt:   "Review PRs.",
			wantNoPR: true,
		},
		{
			name:     "no-PR instruction absent when allow_prs=true",
			allowPRs: true,
			prompt:   "Open a PR with the fix.",
			wantNoPR: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner := &promptCapturingRunner{}
			cfg := baseCfg(func(c *config.Config) {
				c.Agents = []config.AgentDef{
					{Name: "reviewer", Backend: "claude", Skills: []string{"architect"}, Prompt: tc.prompt, AllowPRs: tc.allowPRs},
				}
			})
			s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
			if err != nil {
				t.Fatalf("NewScheduler: %v", err)
			}
			if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err != nil {
				t.Fatalf("TriggerAgent: %v", err)
			}
			runner.mu.Lock()
			defer runner.mu.Unlock()
			if len(runner.prompts) != 1 {
				t.Fatalf("expected 1 prompt, got %d", len(runner.prompts))
			}
			hasNoPR := strings.Contains(runner.prompts[0], noPRPrefix)
			if hasNoPR != tc.wantNoPR {
				t.Errorf("no-PR prefix present=%v, want %v; prompt: %q", hasNoPR, tc.wantNoPR, runner.prompts[0])
			}
			if !strings.Contains(runner.prompts[0], tc.prompt) {
				t.Errorf("expected original prompt text %q to be present, got: %q", tc.prompt, runner.prompts[0])
			}
		})
	}
}

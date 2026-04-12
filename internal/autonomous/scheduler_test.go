package autonomous

import (
	"context"
	"os"
	"path/filepath"
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

func buildTestPromptStore(t *testing.T, dir string) *ai.PromptStore {
	t.Helper()
	writeIssuePrompt(t, dir)
	writeAutonomousBase(t, dir)
	guidancePath := writeGuidance(t, dir, "architect")
	skills := []ai.SkillGuidance{{Name: "architect", PromptFile: guidancePath}}
	prAgents := []ai.AgentSkills{{Name: "architect", Skills: []string{"architect"}}}
	autoAgents := []ai.AgentSkills{{Name: "architect", Skills: []string{"architect"}}}
	issueBase := ai.PromptSource{PromptFile: filepath.Join(dir, "issue_refinement_prompts", "PROMPT.md")}
	prBase := ai.PromptSource{Prompt: "{{.AgentHeading}} {{template \"agent_guidance\" .}}"}
	autoBase := ai.PromptSource{PromptFile: filepath.Join(dir, "autonomous", "base", "PROMPT.md")}
	prompts, err := ai.NewPromptStore(issueBase, prBase, autoBase, skills, prAgents, autoAgents)
	if err != nil {
		t.Fatalf("prompt store: %v", err)
	}
	return prompts
}

func TestSchedulerRunsAutonomousTasks(t *testing.T) {
	dir := t.TempDir()
	prompts := buildTestPromptStore(t, dir)
	cfg := &config.Config{
		AgentsDir: dir,
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
		},
		Repos: []config.RepoConfig{
			{FullName: "owner/repo", Enabled: true},
		},
		AutonomousAgents: []config.AutonomousRepoConfig{
			{
				Repo:    "owner/repo",
				Enabled: true,
				Agents: []config.AutonomousAgentConfig{
					{Name: "architect", Description: "desc", Cron: "* * * * *", Skills: []string{"architect"},
						Tasks: []config.TaskConfig{
							{Name: "issues", Prompt: "scan issues"},
							{Name: "code", Prompt: "inspect code"},
						}},
				},
			},
		},
	}
	memory := NewMemoryStore(dir)
	runner := &stubRunner{}
	scheduler, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, prompts, memory, zerolog.Nop())
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}

	scheduler.runAgent("owner/repo", cfg.AutonomousAgents[0].Agents[0])()

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.calls != 2 {
		t.Fatalf("expected two autonomous tasks, got %d", runner.calls)
	}
}

func TestSchedulerSkipsDisabledRepo(t *testing.T) {
	dir := t.TempDir()
	prompts := buildTestPromptStore(t, dir)
	cfg := &config.Config{
		AgentsDir: dir,
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
		},
		Repos: []config.RepoConfig{
			{FullName: "owner/repo", Enabled: false},
		},
		AutonomousAgents: []config.AutonomousRepoConfig{
			{
				Repo:    "owner/repo",
				Enabled: true,
				Agents: []config.AutonomousAgentConfig{
					{Name: "architect", Description: "desc", Cron: "* * * * *", Skills: []string{"architect"},
						Tasks: []config.TaskConfig{{Name: "issues", Prompt: "scan"}}},
				},
			},
		},
	}
	memory := NewMemoryStore(dir)
	runner := &stubRunner{}
	scheduler, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, prompts, memory, zerolog.Nop())
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}
	if got := len(scheduler.cron.Entries()); got != 0 {
		t.Fatalf("expected no scheduled entries, got %d", got)
	}
}

func TestSchedulerRejectsInvalidCron(t *testing.T) {
	dir := t.TempDir()
	prompts := buildTestPromptStore(t, dir)
	cfg := &config.Config{
		AgentsDir: dir,
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
		},
		Repos: []config.RepoConfig{
			{FullName: "owner/repo", Enabled: true},
		},
		AutonomousAgents: []config.AutonomousRepoConfig{
			{
				Repo:    "owner/repo",
				Enabled: true,
				Agents: []config.AutonomousAgentConfig{
					{Name: "architect", Description: "desc", Cron: "invalid", Skills: []string{"architect"},
						Tasks: []config.TaskConfig{{Name: "issues", Prompt: "scan"}}},
				},
			},
		},
	}
	memory := NewMemoryStore(dir)
	runner := &stubRunner{}
	if _, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, prompts, memory, zerolog.Nop()); err == nil {
		t.Fatalf("expected cron parse error")
	}
}

func TestRunAgentSkipsWhenSchedulerContextCancelled(t *testing.T) {
	dir := t.TempDir()
	prompts := buildTestPromptStore(t, dir)
	agentCfg := config.AutonomousAgentConfig{
		Name: "architect", Description: "desc",
		Skills: []string{"architect"},
		Tasks: []config.TaskConfig{
			{Name: "issues", Prompt: "scan issues"},
			{Name: "code", Prompt: "inspect code"},
		},
	}
	cfg := &config.Config{
		AgentsDir: dir,
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
		},
		Repos: []config.RepoConfig{
			{FullName: "owner/repo", Enabled: true},
		},
		AutonomousAgents: []config.AutonomousRepoConfig{
			{
				Repo:    "owner/repo",
				Enabled: true,
				Agents:  []config.AutonomousAgentConfig{{Name: "architect", Description: "desc", Cron: "* * * * *", Skills: []string{"architect"}, Tasks: agentCfg.Tasks}},
			},
		},
	}
	memory := NewMemoryStore(dir)
	runner := &stubRunner{}
	scheduler, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, prompts, memory, zerolog.Nop())
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	cancel()
	scheduler.setRunCtx(runCtx)

	scheduler.runAgent("owner/repo", agentCfg)()

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.calls != 0 {
		t.Fatalf("expected no autonomous tasks after context cancellation, got %d", runner.calls)
	}
}

func TestSchedulerRunAgentUsesExplicitConfiguredBackend(t *testing.T) {
	dir := t.TempDir()
	prompts := buildTestPromptStore(t, dir)
	agentCfg := config.AutonomousAgentConfig{
		Name: "architect", Backend: "codex",
		Skills: []string{"architect"},
		Tasks: []config.TaskConfig{
			{Name: "issues", Prompt: "scan"},
			{Name: "code", Prompt: "inspect"},
		},
	}
	cfg := &config.Config{
		AgentsDir: dir,
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
			"codex":  {},
		},
	}
	claudeRunner := &stubRunner{}
	codexRunner := &stubRunner{}
	scheduler, err := NewScheduler(
		cfg,
		map[string]ai.Runner{"claude": claudeRunner, "codex": codexRunner},
		prompts,
		NewMemoryStore(dir),
		zerolog.Nop(),
	)
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}

	scheduler.runAgent("owner/repo", agentCfg)()

	claudeRunner.mu.Lock()
	claudeCalls := claudeRunner.calls
	claudeRunner.mu.Unlock()
	if claudeCalls != 0 {
		t.Fatalf("expected claude to not run, got %d calls", claudeCalls)
	}
	codexRunner.mu.Lock()
	defer codexRunner.mu.Unlock()
	if codexRunner.calls != 2 {
		t.Fatalf("expected codex to run two tasks, got %d", codexRunner.calls)
	}
	for _, wf := range codexRunner.workflows {
		if !strings.HasPrefix(wf, "autonomous:codex:") {
			t.Fatalf("expected codex workflow prefix, got %q", wf)
		}
	}
}

func TestSchedulerRunAgentAutoFallsBackToDefaultConfiguredBackend(t *testing.T) {
	dir := t.TempDir()
	prompts := buildTestPromptStore(t, dir)
	agentCfg := config.AutonomousAgentConfig{
		Name: "architect", Backend: "auto",
		Skills: []string{"architect"},
		Tasks: []config.TaskConfig{
			{Name: "issues", Prompt: "scan"},
			{Name: "code", Prompt: "inspect"},
		},
	}
	cfg := &config.Config{
		AgentsDir: dir,
		AIBackends: map[string]config.AIBackendConfig{
			"codex": {},
		},
	}
	codexRunner := &stubRunner{}
	scheduler, err := NewScheduler(
		cfg,
		map[string]ai.Runner{"codex": codexRunner},
		prompts,
		NewMemoryStore(dir),
		zerolog.Nop(),
	)
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}

	scheduler.runAgent("owner/repo", agentCfg)()

	codexRunner.mu.Lock()
	defer codexRunner.mu.Unlock()
	if codexRunner.calls != 2 {
		t.Fatalf("expected codex fallback to run two tasks, got %d", codexRunner.calls)
	}
}

func buildTestConfig(t *testing.T, dir string) *config.Config {
	t.Helper()
	return &config.Config{
		AgentsDir: dir,
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
		},
		Repos: []config.RepoConfig{
			{FullName: "owner/repo", Enabled: true},
		},
		AutonomousAgents: []config.AutonomousRepoConfig{
			{
				Repo:    "owner/repo",
				Enabled: true,
				Agents: []config.AutonomousAgentConfig{
					{Name: "architect", Description: "desc", Cron: "* * * * *", Skills: []string{"architect"},
						Tasks: []config.TaskConfig{
							{Name: "issues", Prompt: "scan issues"},
							{Name: "code", Prompt: "inspect code"},
						}},
				},
			},
		},
	}
}

func TestSchedulerAgentStatusesBeforeFirstRun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	prompts := buildTestPromptStore(t, dir)
	cfg := &config.Config{
		AgentsDir: dir,
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
		},
		Repos: []config.RepoConfig{
			{FullName: "owner/repo", Enabled: true},
		},
		AutonomousAgents: []config.AutonomousRepoConfig{
			{
				Repo:    "owner/repo",
				Enabled: true,
				Agents: []config.AutonomousAgentConfig{
					{Name: "architect", Description: "desc", Cron: "0 9 * * *", Skills: []string{"architect"},
						Tasks: []config.TaskConfig{{Name: "scan", Prompt: "scan issues"}}},
				},
			},
		},
	}
	scheduler, err := NewScheduler(cfg, map[string]ai.Runner{"claude": &stubRunner{}}, prompts, NewMemoryStore(dir), zerolog.Nop())
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}

	statuses := scheduler.AgentStatuses()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 agent status, got %d", len(statuses))
	}
	if statuses[0].Name != "architect" {
		t.Errorf("expected agent name architect, got %q", statuses[0].Name)
	}
	if statuses[0].Repo != "owner/repo" {
		t.Errorf("expected repo owner/repo, got %q", statuses[0].Repo)
	}
	if statuses[0].LastRun != nil {
		t.Errorf("expected last_run nil before first run, got %v", statuses[0].LastRun)
	}
	if statuses[0].LastStatus != "" {
		t.Errorf("expected empty last_status before first run, got %q", statuses[0].LastStatus)
	}
	if statuses[0].NextRun.IsZero() {
		t.Errorf("expected non-zero next_run")
	}
}

func TestSchedulerAgentStatusesAfterRun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	prompts := buildTestPromptStore(t, dir)
	agentCfg := config.AutonomousAgentConfig{
		Name:    "architect",
		Cron:    "0 9 * * *",
		Backend: "claude",
		Skills:  []string{"architect"},
		Tasks:   []config.TaskConfig{{Name: "scan", Prompt: "scan issues"}},
	}
	cfg := &config.Config{
		AgentsDir: dir,
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
		},
		Repos: []config.RepoConfig{
			{FullName: "owner/repo", Enabled: true},
		},
		AutonomousAgents: []config.AutonomousRepoConfig{
			{
				Repo:    "owner/repo",
				Enabled: true,
				Agents:  []config.AutonomousAgentConfig{agentCfg},
			},
		},
	}
	scheduler, err := NewScheduler(cfg, map[string]ai.Runner{"claude": &stubRunner{}}, prompts, NewMemoryStore(dir), zerolog.Nop())
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}

	scheduler.runAgent("owner/repo", agentCfg)()

	statuses := scheduler.AgentStatuses()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 agent status, got %d", len(statuses))
	}
	if statuses[0].LastStatus != "success" {
		t.Errorf("expected last_status success, got %q", statuses[0].LastStatus)
	}
	if statuses[0].LastRun == nil {
		t.Errorf("expected last_run to be set after a run")
	}
}

func TestTriggerAgentRunsTasksSynchronously(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	prompts := buildTestPromptStore(t, dir)
	cfg := buildTestConfig(t, dir)
	memory := NewMemoryStore(dir)
	runner := &stubRunner{}
	scheduler, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, prompts, memory, zerolog.Nop())
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}

	if err := scheduler.TriggerAgent(context.Background(), "architect", "owner/repo"); err != nil {
		t.Fatalf("TriggerAgent: %v", err)
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.calls != 2 {
		t.Fatalf("expected two tasks, got %d", runner.calls)
	}
}

func TestTriggerAgentReturnsErrorForUnknownAgent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	prompts := buildTestPromptStore(t, dir)
	cfg := buildTestConfig(t, dir)
	memory := NewMemoryStore(dir)
	runner := &stubRunner{}
	scheduler, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, prompts, memory, zerolog.Nop())
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}

	if err := scheduler.TriggerAgent(context.Background(), "unknown-agent", "owner/repo"); err == nil {
		t.Fatal("expected error for unknown agent, got nil")
	}
}

func TestTriggerAgentReturnsErrorForUnknownRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	prompts := buildTestPromptStore(t, dir)
	cfg := buildTestConfig(t, dir)
	memory := NewMemoryStore(dir)
	runner := &stubRunner{}
	scheduler, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, prompts, memory, zerolog.Nop())
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}

	if err := scheduler.TriggerAgent(context.Background(), "architect", "nobody/nothere"); err == nil {
		t.Fatal("expected error for unknown repo, got nil")
	}
}

func TestTriggerAgentReturnsErrorForDisabledRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	prompts := buildTestPromptStore(t, dir)
	cfg := &config.Config{
		AgentsDir: dir,
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
		},
		Repos: []config.RepoConfig{
			{FullName: "owner/repo", Enabled: false},
		},
		AutonomousAgents: []config.AutonomousRepoConfig{
			{
				Repo:    "owner/repo",
				Enabled: true,
				Agents: []config.AutonomousAgentConfig{
					{Name: "architect", Description: "desc", Cron: "* * * * *", Skills: []string{"architect"},
						Tasks: []config.TaskConfig{{Name: "issues", Prompt: "scan"}}},
				},
			},
		},
	}
	memory := NewMemoryStore(dir)
	runner := &stubRunner{}
	scheduler, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, prompts, memory, zerolog.Nop())
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}

	if err := scheduler.TriggerAgent(context.Background(), "architect", "owner/repo"); err == nil {
		t.Fatal("expected error for disabled repo, got nil")
	}
}

func TestSchedulerSkipsJobWhenPreviousRunStillRunning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	prompts := buildTestPromptStore(t, dir)

	ready := make(chan struct{}, 1)
	block := make(chan struct{})
	runner := &blockingRunner{ready: ready, block: block}

	cfg := &config.Config{
		AgentsDir: dir,
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
		},
		Repos: []config.RepoConfig{
			{FullName: "owner/repo", Enabled: true},
		},
		AutonomousAgents: []config.AutonomousRepoConfig{
			{
				Repo:    "owner/repo",
				Enabled: true,
				Agents: []config.AutonomousAgentConfig{
					{Name: "architect", Description: "desc", Cron: "* * * * *", Skills: []string{"architect"},
						Tasks: []config.TaskConfig{{Name: "scan", Prompt: "scan"}}},
				},
			},
		},
	}
	scheduler, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, prompts, NewMemoryStore(dir), zerolog.Nop())
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}

	wrappedJob := scheduler.cron.Entry(scheduler.agentEntries[0].cronID).WrappedJob

	// Start first run in background — it blocks until we close block.
	done := make(chan struct{})
	go func() {
		defer close(done)
		wrappedJob.Run()
	}()

	// Wait until the first run has started so the skip-guard token is held.
	<-ready

	// This second invocation should be skipped (not queued) by SkipIfStillRunning.
	wrappedJob.Run()

	// Unblock the first run and wait for it to finish.
	close(block)
	<-done

	runner.mu.Lock()
	calls := runner.calls
	runner.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected exactly 1 runner invocation (second should be skipped), got %d", calls)
	}
}

func writeIssuePrompt(t *testing.T, dir string) {
	t.Helper()
	issueDir := filepath.Join(dir, "issue_refinement_prompts")
	if err := os.MkdirAll(issueDir, 0o755); err != nil {
		t.Fatalf("mkdir issue prompt dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(issueDir, "PROMPT.md"), []byte("{{.Repo}} #{{.Number}}"), 0o644); err != nil {
		t.Fatalf("write issue prompt: %v", err)
	}
}

func writeAutonomousBase(t *testing.T, dir string) {
	t.Helper()
	autoBase := filepath.Join(dir, "autonomous", "base")
	if err := os.MkdirAll(autoBase, 0o755); err != nil {
		t.Fatalf("mkdir auto base: %v", err)
	}
	if err := os.WriteFile(filepath.Join(autoBase, "PROMPT.md"), []byte("{{.Task}} {{.MemoryPath}} {{template \"agent_guidance\" .}}"), 0o644); err != nil {
		t.Fatalf("write auto base: %v", err)
	}
}

func writeGuidance(t *testing.T, dir string, skill string) string {
	t.Helper()
	guidanceDir := filepath.Join(dir, "guidance")
	if err := os.MkdirAll(guidanceDir, 0o755); err != nil {
		t.Fatalf("mkdir guidance: %v", err)
	}
	path := filepath.Join(guidanceDir, skill+".md")
	if err := os.WriteFile(path, []byte(skill), 0o644); err != nil {
		t.Fatalf("write guidance: %v", err)
	}
	return path
}

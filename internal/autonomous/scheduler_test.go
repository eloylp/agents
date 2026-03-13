package autonomous

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
)

type stubRunner struct {
	calls int
}

func (s *stubRunner) Run(_ context.Context, _ ai.Request) (ai.Response, error) {
	s.calls++
	return ai.Response{}, nil
}

func buildTestPromptStore(t *testing.T, dir string) *ai.PromptStore {
	t.Helper()
	writeIssuePrompt(t, dir)
	writeAutonomousBase(t, dir)
	guidancePath := writeGuidance(t, dir, "architect")
	agents := []ai.AgentGuidance{{Name: "architect", PromptFile: guidancePath}}
	issueBase := ai.PromptSource{PromptFile: filepath.Join(dir, "issue_refinement_prompts", "PROMPT.md")}
	prBase := ai.PromptSource{Prompt: "{{.AgentHeading}} {{template \"agent_guidance\" .}}"}
	autoBase := ai.PromptSource{PromptFile: filepath.Join(dir, "autonomous", "base", "PROMPT.md")}
	prompts, err := ai.NewPromptStore(issueBase, prBase, autoBase, agents, []string{"architect"})
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
					{Name: "architect", Description: "desc", Cron: "* * * * *"},
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

	scheduler.runAgent("owner/repo", config.AutonomousAgentConfig{Name: "architect", Description: "desc"})()

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
					{Name: "architect", Description: "desc", Cron: "* * * * *"},
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
					{Name: "architect", Description: "desc", Cron: "invalid"},
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

func writeGuidance(t *testing.T, dir string, agent string) string {
	t.Helper()
	guidanceDir := filepath.Join(dir, "guidance")
	if err := os.MkdirAll(guidanceDir, 0o755); err != nil {
		t.Fatalf("mkdir guidance: %v", err)
	}
	path := filepath.Join(guidanceDir, agent+".md")
	if err := os.WriteFile(path, []byte("{{define \"agent_guidance\"}}"+agent+"{{end}}"), 0o644); err != nil {
		t.Fatalf("write guidance: %v", err)
	}
	return path
}

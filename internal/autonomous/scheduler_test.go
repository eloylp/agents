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

func TestSchedulerRunsAutonomousTasks(t *testing.T) {
	dir := t.TempDir()
	autoDir := filepath.Join(dir, "autonomous", "architect")
	if err := os.MkdirAll(autoDir, 0o755); err != nil {
		t.Fatalf("mkdir autonomous prompt dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(autoDir, "PROMPT.md"), []byte("{{.Task}} {{.MemoryPath}}"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	prompts, err := ai.NewPromptStore(dir)
	if err != nil {
		t.Fatalf("prompt store: %v", err)
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

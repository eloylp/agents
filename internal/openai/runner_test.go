package openai

import (
	"context"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
)

func TestRunnerNoopMode(t *testing.T) {
	runner := NewRunner(config.OpenAIConfig{
		Mode: "noop",
	}, zerolog.Nop())

	resp, err := runner.Run(context.Background(), ai.Request{
		Workflow:    "issue_refine",
		Repo:        "owner/repo",
		Number:      1,
		Fingerprint: "fp",
		Prompt:      "prompt",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(resp.Artifacts) != 0 {
		t.Fatalf("expected no artifacts in noop mode")
	}
}

func TestRunnerCommandModeRequiresCommand(t *testing.T) {
	runner := NewRunner(config.OpenAIConfig{
		Mode: "command",
	}, zerolog.Nop())

	_, err := runner.Run(context.Background(), ai.Request{
		Workflow:    "issue_refine",
		Repo:        "owner/repo",
		Number:      1,
		Fingerprint: "fp",
		Prompt:      "prompt",
	})
	if err == nil {
		t.Fatalf("expected error for missing command")
	}
	if !strings.Contains(err.Error(), "openai command is required when mode=command") {
		t.Fatalf("unexpected error: %v", err)
	}
}

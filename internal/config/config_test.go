package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRequiresSupportedBackendNames(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  unsupported:
    mode: noop
repos:
  - full_name: "owner/repo"
    enabled: true
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatalf("expected load to fail for unsupported ai backend name")
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
autonomous_agents:
  - repo: "owner/repo"
    enabled: true
    agents:
      - name: "architect"
        cron: "* * * * *"
        skills: [architect]
        tasks:
          - name: "issues"
            prompt: "scan issues"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	backend := cfg.AIBackends["claude"]
	if backend.TimeoutSeconds == nil || *backend.TimeoutSeconds != defaultAITimeoutSeconds {
		got := 0
		if backend.TimeoutSeconds != nil {
			got = *backend.TimeoutSeconds
		}
		t.Fatalf("expected timeout default %d, got %d", defaultAITimeoutSeconds, got)
	}
	if cfg.Processor.IssueQueueBuffer != defaultIssueQueueBufferSize {
		t.Fatalf("expected issue queue buffer default %d, got %d", defaultIssueQueueBufferSize, cfg.Processor.IssueQueueBuffer)
	}
	if cfg.Processor.PRQueueBuffer != defaultPRQueueBufferSize {
		t.Fatalf("expected pr queue buffer default %d, got %d", defaultPRQueueBufferSize, cfg.Processor.PRQueueBuffer)
	}
	if cfg.Processor.Workers == nil || *cfg.Processor.Workers != defaultProcessorWorkers {
		got := 0
		if cfg.Processor.Workers != nil {
			got = *cfg.Processor.Workers
		}
		t.Fatalf("expected processor workers default %d, got %d", defaultProcessorWorkers, got)
	}
	if cfg.Processor.MaxConcurrentAgents == nil || *cfg.Processor.MaxConcurrentAgents != defaultMaxConcurrentAgents {
		got := 0
		if cfg.Processor.MaxConcurrentAgents != nil {
			got = *cfg.Processor.MaxConcurrentAgents
		}
		t.Fatalf("expected max_concurrent_agents default %d, got %d", defaultMaxConcurrentAgents, got)
	}
	if cfg.HTTP.ShutdownTimeoutSeconds != defaultHTTPShutdownSeconds {
		t.Fatalf("expected shutdown timeout default %d, got %d", defaultHTTPShutdownSeconds, cfg.HTTP.ShutdownTimeoutSeconds)
	}
	if cfg.AgentsDir != defaultAgentsDir {
		t.Fatalf("expected default agents dir %q, got %q", defaultAgentsDir, cfg.AgentsDir)
	}
	if cfg.MemoryDir != defaultMemoryDir {
		t.Fatalf("expected default memory dir %q, got %q", defaultMemoryDir, cfg.MemoryDir)
	}
	if cfg.Prompts.IssueRefinement.PromptFile != defaultIssueRefinementPromptFile {
		t.Fatalf("expected default issue prompt file %q, got %q", defaultIssueRefinementPromptFile, cfg.Prompts.IssueRefinement.PromptFile)
	}
	if cfg.Prompts.PRReview.PromptFile != defaultPRReviewPromptFile {
		t.Fatalf("expected default pr prompt file %q, got %q", defaultPRReviewPromptFile, cfg.Prompts.PRReview.PromptFile)
	}
	if cfg.Prompts.Autonomous.PromptFile != defaultAutonomousPromptFile {
		t.Fatalf("expected default auto prompt file %q, got %q", defaultAutonomousPromptFile, cfg.Prompts.Autonomous.PromptFile)
	}
	if len(cfg.AutonomousAgents) != 1 || len(cfg.AutonomousAgents[0].Agents) != 1 {
		t.Fatalf("expected one autonomous agent configured")
	}
	if got := cfg.AutonomousAgents[0].Agents[0].Backend; got != "auto" {
		t.Fatalf("expected autonomous backend default auto, got %q", got)
	}
}

func TestDefaultConfiguredBackend(t *testing.T) {
	cfg := Config{AIBackends: map[string]AIBackendConfig{"codex": {}, "claude": {}}}
	if got := cfg.DefaultConfiguredBackend(); got != "claude" {
		t.Fatalf("expected claude, got %q", got)
	}
	cfg = Config{AIBackends: map[string]AIBackendConfig{"codex": {}}}
	if got := cfg.DefaultConfiguredBackend(); got != "codex" {
		t.Fatalf("expected codex, got %q", got)
	}
	cfg = Config{}
	if got := cfg.DefaultConfiguredBackend(); got != "" {
		t.Fatalf("expected empty default backend, got %q", got)
	}
}

func TestResolveBackend(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		AIBackends: map[string]AIBackendConfig{
			"claude": {},
			"codex":  {},
		},
	}
	tests := []struct {
		name  string
		raw   string
		want  string
	}{
		{"empty falls back to default", "", "claude"},
		{"auto falls back to default", "auto", "claude"},
		{"AUTO case-insensitive", "AUTO", "claude"},
		{"explicit claude", "claude", "claude"},
		{"explicit codex", "codex", "codex"},
		{"uppercase CLAUDE normalised", "CLAUDE", "claude"},
		{"whitespace-padded token", "  claude  ", "claude"},
		{"unknown backend returns empty", "gpt", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := cfg.ResolveBackend(tt.raw); got != tt.want {
				t.Fatalf("ResolveBackend(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestSkillValidation(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	tests := []struct {
		name    string
		content string
	}{
		{
			name: "duplicate skill name",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
  - name: architect
    prompt: "duplicate"
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
`,
		},
		{
			name: "skill missing both prompt and prompt_file",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
`,
		},
		{
			name: "skill has both prompt and prompt_file",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "inline"
    prompt_file: "architect.md"
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestAgentValidation(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	tests := []struct {
		name    string
		content string
	}{
		{
			name: "duplicate agent name",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [architect]
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
`,
		},
		{
			name: "agent missing skills",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
repos:
  - full_name: "owner/repo"
    enabled: true
`,
		},
		{
			name: "agent references unknown skill",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [nonexistent]
repos:
  - full_name: "owner/repo"
    enabled: true
`,
		},
		{
			name: "autonomous references unknown skill",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
autonomous_agents:
  - repo: "owner/repo"
    enabled: true
    agents:
      - name: "sweep"
        cron: "* * * * *"
        skills: [nonexistent]
        tasks:
          - name: "issues"
            prompt: "scan issues"
`,
		},
		{
			name: "autonomous agent backend invalid",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
autonomous_agents:
  - repo: "owner/repo"
    enabled: true
    agents:
      - name: "architect"
        cron: "* * * * *"
        backend: "gpt4"
        skills: [architect]
        tasks:
          - name: "issues"
            prompt: "scan issues"
`,
		},
		{
			name: "autonomous agent missing tasks",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
autonomous_agents:
  - repo: "owner/repo"
    enabled: true
    agents:
      - name: "architect"
        cron: "* * * * *"
        skills: [architect]
`,
		},
		{
			name: "autonomous agent task missing prompt",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
autonomous_agents:
  - repo: "owner/repo"
    enabled: true
    agents:
      - name: "architect"
        cron: "* * * * *"
        skills: [architect]
        tasks:
          - name: "issues"
`,
		},
		{
			name: "autonomous agent task has both prompt and prompt_file",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
autonomous_agents:
  - repo: "owner/repo"
    enabled: true
    agents:
      - name: "architect"
        cron: "* * * * *"
        skills: [architect]
        tasks:
          - name: "issues"
            prompt: "inline"
            prompt_file: "tasks/issues.md"
`,
		},
		{
			name: "prompts has both prompt and prompt_file",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [architect]
prompts:
  issue_refinement:
    prompt_file: "issue.md"
    prompt: "inline issue"
repos:
  - full_name: "owner/repo"
    enabled: true
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestAgentValidAccepted(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
  - name: security
    prompt: |
      Focus on authentication, authorization,
      and input validation.
agents:
  - name: architect
    skills: [architect]
  - name: security
    skills: [security]
repos:
  - full_name: "owner/repo"
    enabled: true
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
	if len(cfg.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(cfg.Agents))
	}
	if _, ok := cfg.AgentByName("architect"); !ok {
		t.Fatalf("expected to find architect agent")
	}
	if _, ok := cfg.AgentByName("nonexistent"); ok {
		t.Fatalf("expected nonexistent agent to not be found")
	}
	names := cfg.AgentNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 agent names, got %d", len(names))
	}

	if !cfg.HasAgent("architect") {
		t.Fatal("HasAgent: expected true for existing agent")
	}
	if cfg.HasAgent("nonexistent") {
		t.Fatal("HasAgent: expected false for unknown agent")
	}
}

func TestCodexBackendArgsInConfig(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "correct args exec and skip-git-repo-check",
			args:    []string{"exec", "--skip-git-repo-check"},
			wantErr: false,
		},
		{
			name:    "wrong args -p only",
			args:    []string{"-p"},
			wantErr: false, // config load doesn't validate args content, just that config parses
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			argsYAML := ""
			for _, a := range tt.args {
				argsYAML += "\n      - " + `"` + a + `"`
			}
			content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  codex:
    mode: command
    command: codex
    args:` + argsYAML + `
repos:
  - full_name: "owner/repo"
    enabled: true
`
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			cfg, err := Load(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil {
				got := cfg.AIBackends["codex"].Args
				if len(got) != len(tt.args) {
					t.Fatalf("expected args %v, got %v", tt.args, got)
				}
				for i, a := range tt.args {
					if got[i] != a {
						t.Fatalf("arg[%d]: expected %q, got %q", i, a, got[i])
					}
				}
			}
		})
	}
}

// TestCodexExampleConfigArgs verifies the recommended codex args in config.example.yaml
// include exec, --skip-git-repo-check, and --dangerously-bypass-approvals-and-sandbox.
// This is a regression test for issues #61 and #64: it loads the actual example file so
// any future drift from the required args will be caught by CI.
func TestCodexExampleConfigArgs(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "secret")

	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	codex := cfg.AIBackends["codex"]
	wantArgs := []string{"exec", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox"}
	if len(codex.Args) != len(wantArgs) {
		t.Fatalf("expected codex args %v, got %v", wantArgs, codex.Args)
	}
	for i, want := range wantArgs {
		if codex.Args[i] != want {
			t.Fatalf("codex arg[%d]: expected %q, got %q", i, want, codex.Args[i])
		}
	}
}

func TestAutonomousValidation(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
autonomous_agents:
  - repo: ""
    enabled: true
    agents:
      - name: ""
        cron: ""
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("expected validation error for autonomous agents")
	}
}

func TestAutonomousAgentNameNormalized(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
autonomous_agents:
  - repo: "owner/repo"
    enabled: true
    agents:
      - name: "Scout"
        cron: "* * * * *"
        skills: [architect]
        tasks:
          - name: "scan"
            prompt: "scan issues"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("expected Load to succeed for mixed-case autonomous agent name, got: %v", err)
	}
	if got := cfg.AutonomousAgents[0].Agents[0].Name; got != "scout" {
		t.Errorf("expected name normalized to %q, got %q", "scout", got)
	}
}

func TestLoadRejectsCommandModeWithEmptyCommand(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: command
    # command field intentionally omitted
    args: ["-p", "--dangerously-skip-permissions"]
repos:
  - full_name: "owner/repo"
    enabled: true
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected load to fail when mode=command and command is empty")
	}
}

func TestLoadAcceptsCommandModeWithNonEmptyCommand(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: command
    command: claude
    args: ["-p", "--dangerously-skip-permissions"]
repos:
  - full_name: "owner/repo"
    enabled: true
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err != nil {
		t.Fatalf("unexpected error for valid command mode config: %v", err)
	}
}

func TestLoadRejectsCommandModeWithWhitespaceOnlyCommand(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: command
    command: "   "
    args: ["-p", "--dangerously-skip-permissions"]
repos:
  - full_name: "owner/repo"
    enabled: true
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected load to fail when mode=command and command is whitespace-only")
	}
}

func TestLoadNormalizesWhitespacePaddedCommand(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: command
    command: "  claude  "
    args: ["-p", "--dangerously-skip-permissions"]
repos:
  - full_name: "owner/repo"
    enabled: true
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error for padded command: %v", err)
	}
	if got := cfg.AIBackends["claude"].Command; got != "claude" {
		t.Errorf("expected normalized command %q, got %q", "claude", got)
	}
}

func TestLoadRejectsUnsupportedBackendMode(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	cases := []struct {
		name string
		mode string
	}{
		{"typo", "cmd"},
		{"unknown", "subprocess"},
		{"empty-like", "nOop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: ` + tc.mode + `
repos:
  - full_name: "owner/repo"
    enabled: true
`
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected load to fail for mode=%q", tc.mode)
			}
		})
	}
}

func TestExplicitZeroBackendFieldsHonoured(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	tests := []struct {
		name          string
		yaml          string
		wantTimeout   int
		wantMaxPrompt int
	}{
		{
			name: "explicit zero timeout_seconds disables subprocess timeout",
			yaml: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
    timeout_seconds: 0
repos:
  - full_name: "owner/repo"
    enabled: true
`,
			wantTimeout:   0,
			wantMaxPrompt: defaultMaxPromptChars,
		},
		{
			name: "explicit zero max_prompt_chars disables prompt truncation",
			yaml: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
    max_prompt_chars: 0
repos:
  - full_name: "owner/repo"
    enabled: true
`,
			wantTimeout:   defaultAITimeoutSeconds,
			wantMaxPrompt: 0,
		},
		{
			name: "both explicit zeros honoured",
			yaml: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
    timeout_seconds: 0
    max_prompt_chars: 0
repos:
  - full_name: "owner/repo"
    enabled: true
`,
			wantTimeout:   0,
			wantMaxPrompt: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("unexpected load error: %v", err)
			}
			backend := cfg.AIBackends["claude"]
			if backend.TimeoutSeconds == nil {
				t.Fatal("TimeoutSeconds is nil, expected a pointer to an int")
			}
			if *backend.TimeoutSeconds != tt.wantTimeout {
				t.Errorf("TimeoutSeconds: got %d, want %d", *backend.TimeoutSeconds, tt.wantTimeout)
			}
			if backend.MaxPromptChars == nil {
				t.Fatal("MaxPromptChars is nil, expected a pointer to an int")
			}
			if *backend.MaxPromptChars != tt.wantMaxPrompt {
				t.Errorf("MaxPromptChars: got %d, want %d", *backend.MaxPromptChars, tt.wantMaxPrompt)
			}
		})
	}
}

func TestLoadRejectsNegativeBackendFields(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	tests := []struct {
		name       string
		yaml       string
		wantErrMsg string
	}{
		{
			name: "negative timeout_seconds rejected",
			yaml: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
    timeout_seconds: -1
repos:
  - full_name: "owner/repo"
`,
			wantErrMsg: `config: ai backend "claude" has negative timeout_seconds -1 (use 0 to disable the timeout)`,
		},
		{
			name: "negative max_prompt_chars rejected",
			yaml: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
    max_prompt_chars: -10
repos:
  - full_name: "owner/repo"
`,
			wantErrMsg: `config: ai backend "claude" has negative max_prompt_chars -10 (use 0 to disable truncation)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if err.Error() != tt.wantErrMsg {
				t.Errorf("error message:\n  got:  %q\n  want: %q", err.Error(), tt.wantErrMsg)
			}
		})
	}
}

func TestLoadAcceptsSupportedBackendModes(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	cases := []struct {
		name string
		mode string
	}{
		{"noop", "noop"},
		{"command", "command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			var commandLine string
			if tc.mode == "command" {
				commandLine = "\n    command: claude"
			}
			content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: ` + tc.mode + commandLine + `
repos:
  - full_name: "owner/repo"
    enabled: true
`
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if _, err := Load(path); err != nil {
				t.Fatalf("unexpected error for mode=%q: %v", tc.mode, err)
			}
		})
	}
}

func TestTaskConfigResolve(t *testing.T) {
	t.Parallel()

	t.Run("inline prompt returned directly", func(t *testing.T) {
		t.Parallel()
		tc := TaskConfig{Name: "scan", Prompt: "scan all issues"}
		got, err := tc.Resolve("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "scan all issues" {
			t.Fatalf("expected %q, got %q", "scan all issues", got)
		}
	})

	t.Run("prompt_file with relative path joined to baseDir", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte("file prompt"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		tc := TaskConfig{Name: "scan", PromptFile: "task.md"}
		got, err := tc.Resolve(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "file prompt" {
			t.Fatalf("expected %q, got %q", "file prompt", got)
		}
	})

	t.Run("prompt_file with absolute path ignores baseDir", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		absPath := filepath.Join(dir, "abs_task.md")
		if err := os.WriteFile(absPath, []byte("absolute prompt"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		tc := TaskConfig{Name: "scan", PromptFile: absPath}
		got, err := tc.Resolve("/some/other/dir")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "absolute prompt" {
			t.Fatalf("expected %q, got %q", "absolute prompt", got)
		}
	})

	t.Run("prompt_file missing returns error", func(t *testing.T) {
		t.Parallel()
		tc := TaskConfig{Name: "scan", PromptFile: "nonexistent.md"}
		if _, err := tc.Resolve(t.TempDir()); err == nil {
			t.Fatal("expected error for missing file")
		}
	})
}

func TestTaskConfigPromptFileAccepted(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	dir := t.TempDir()
	promptFile := filepath.Join(dir, "issues.md")
	if err := os.WriteFile(promptFile, []byte("scan all issues"), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	cfgPath := filepath.Join(dir, "config.yaml")
	content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
autonomous_agents:
  - repo: "owner/repo"
    enabled: true
    agents:
      - name: "sweep"
        cron: "* * * * *"
        skills: [architect]
        tasks:
          - name: "issues"
            prompt_file: "issues.md"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
	task := cfg.AutonomousAgents[0].Agents[0].Tasks[0]
	if task.PromptFile != "issues.md" {
		t.Fatalf("expected PromptFile %q, got %q", "issues.md", task.PromptFile)
	}
	if task.Prompt != "" {
		t.Fatalf("expected empty inline Prompt, got %q", task.Prompt)
	}
}

func TestLoadRejectsDuplicateAutonomousAgentNames(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	tests := []struct {
		name       string
		content    string
		wantErr    bool
		wantErrMsg string
	}{
		{
			name: "exact duplicate names rejected",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
autonomous_agents:
  - repo: "owner/repo"
    enabled: true
    agents:
      - name: "scout"
        cron: "* * * * *"
        skills: [architect]
        tasks:
          - name: "scan"
            prompt: "scan issues"
      - name: "scout"
        cron: "* * * * *"
        skills: [architect]
        tasks:
          - name: "scan"
            prompt: "scan issues"
`,
			wantErr:    true,
			wantErrMsg: `config: duplicate autonomous agent name "scout" for repo owner/repo`,
		},
		{
			name: "case-only duplicates collapse after normalization and are rejected",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
autonomous_agents:
  - repo: "owner/repo"
    enabled: true
    agents:
      - name: "Scout"
        cron: "* * * * *"
        skills: [architect]
        tasks:
          - name: "scan"
            prompt: "scan issues"
      - name: "scout"
        cron: "* * * * *"
        skills: [architect]
        tasks:
          - name: "scan"
            prompt: "scan issues"
`,
			wantErr:    true,
			wantErrMsg: `config: duplicate autonomous agent name "scout" for repo owner/repo`,
		},
		{
			name: "distinct names in same repo accepted",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
skills:
  - name: architect
    prompt: "focus on architecture"
agents:
  - name: architect
    skills: [architect]
repos:
  - full_name: "owner/repo"
    enabled: true
autonomous_agents:
  - repo: "owner/repo"
    enabled: true
    agents:
      - name: "scout"
        cron: "* * * * *"
        skills: [architect]
        tasks:
          - name: "scan"
            prompt: "scan issues"
      - name: "architect"
        cron: "* * * * *"
        skills: [architect]
        tasks:
          - name: "review"
            prompt: "review issues"
`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			_, err := Load(path)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if tt.wantErr && err != nil && tt.wantErrMsg != "" && err.Error() != tt.wantErrMsg {
				t.Fatalf("expected error %q, got %q", tt.wantErrMsg, err.Error())
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected valid config, got: %v", err)
			}
		})
	}
}

func TestValidateReposAllDisabled(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	tests := []struct {
		name       string
		content    string
		wantErr    bool
		wantErrMsg string
	}{
		{
			name: "all repos disabled rejects at startup",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
repos:
  - full_name: "owner/repo"
    enabled: false
  - full_name: "owner/other"
    enabled: false
`,
			wantErr:    true,
			wantErrMsg: "config: at least one repo must have enabled: true",
		},
		{
			name: "single enabled repo is accepted",
			content: `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
repos:
  - full_name: "owner/repo"
    enabled: false
  - full_name: "owner/other"
    enabled: true
`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			_, err := Load(path)
			if tt.wantErr && err == nil {
				t.Fatalf("expected validation error for all-disabled repos, got nil")
			}
			if tt.wantErr && err != nil && tt.wantErrMsg != "" && err.Error() != tt.wantErrMsg {
				t.Fatalf("expected error %q, got %q", tt.wantErrMsg, err.Error())
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected valid config, got: %v", err)
			}
		})
	}
}

func TestLoadRejectsInvalidMaxConcurrentAgents(t *testing.T) {
	baseYAML := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
  claude:
    mode: noop
repos:
  - full_name: "owner/repo"
processor:
  max_concurrent_agents: %d
`
	for _, invalidValue := range []int{0, -1} {
		invalidValue := invalidValue
		t.Run(fmt.Sprintf("value=%d", invalidValue), func(t *testing.T) {
			t.Setenv("WEBHOOK_SECRET", "secret")
			path := filepath.Join(t.TempDir(), "config.yaml")
			content := fmt.Sprintf(baseYAML, invalidValue)
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected Load to fail for max_concurrent_agents=%d, but it succeeded", invalidValue)
			}
			if !strings.Contains(err.Error(), "max_concurrent_agents") {
				t.Errorf("expected error to mention max_concurrent_agents, got: %v", err)
			}
		})
	}
}

func TestProcessorWorkersValidation(t *testing.T) {
	t.Parallel()
	const baseYAML = `
http:
  webhook_secret: secret
ai_backends:
  claude:
    mode: command
    command: claude
repos:
  - full_name: owner/repo
    enabled: true
processor:
  workers: %d
`
	for _, workers := range []int{0, -1} {
		workers := workers
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			t.Parallel()
			yaml := fmt.Sprintf(baseYAML, workers)
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error for workers=%d, got nil", workers)
			}
			if !strings.Contains(err.Error(), "processor.workers") {
				t.Fatalf("expected error mentioning processor.workers, got: %v", err)
			}
		})
	}
}

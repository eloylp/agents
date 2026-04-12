package config

import (
	"os"
	"path/filepath"
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
	if backend.TimeoutSeconds != defaultAITimeoutSeconds {
		t.Fatalf("expected timeout default %d, got %d", defaultAITimeoutSeconds, backend.TimeoutSeconds)
	}
	if cfg.Processor.IssueQueueBuffer != defaultIssueQueueBufferSize {
		t.Fatalf("expected issue queue buffer default %d, got %d", defaultIssueQueueBufferSize, cfg.Processor.IssueQueueBuffer)
	}
	if cfg.Processor.PRQueueBuffer != defaultPRQueueBufferSize {
		t.Fatalf("expected pr queue buffer default %d, got %d", defaultPRQueueBufferSize, cfg.Processor.PRQueueBuffer)
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
autonomous_agents:
  - repo: ""
    enabled: true
    agents:
      - name: ""
        cron: ""
  - repo: "owner/repo"
    enabled: true
    agents:
      - name: "UpperCase"
        cron: "* * * * *"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("expected validation error for autonomous agents")
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

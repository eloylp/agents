package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	runtimeexec "github.com/eloylp/agents/internal/runtime"
	"github.com/rs/zerolog"
)

type fakeContainerRunner struct {
	spec runtimeexec.ContainerSpec
	code int
	err  error
}

func (f *fakeContainerRunner) EnsureImage(context.Context, string) error { return nil }

func (f *fakeContainerRunner) Run(_ context.Context, spec runtimeexec.ContainerSpec) (runtimeexec.ExitStatus, error) {
	f.spec = spec
	if spec.Stdout != nil {
		_, _ = spec.Stdout.Write([]byte(`{"summary":"container ok","artifacts":[],"memory":"","dispatch":[]}` + "\n"))
	}
	return runtimeexec.ExitStatus{Code: f.code}, f.err
}

type silentContainerRunner struct {
	spec runtimeexec.ContainerSpec
}

func (f *silentContainerRunner) EnsureImage(context.Context, string) error { return nil }

func (f *silentContainerRunner) Run(_ context.Context, spec runtimeexec.ContainerSpec) (runtimeexec.ExitStatus, error) {
	f.spec = spec
	return runtimeexec.ExitStatus{}, nil
}

type timeoutContainerRunner struct {
	spec        runtimeexec.ContainerSpec
	writeJSON   bool
	writeSignal chan struct{}
}

func (f *timeoutContainerRunner) EnsureImage(context.Context, string) error { return nil }

func (f *timeoutContainerRunner) Run(ctx context.Context, spec runtimeexec.ContainerSpec) (runtimeexec.ExitStatus, error) {
	f.spec = spec
	if f.writeJSON && spec.Stdout != nil {
		_, _ = spec.Stdout.Write([]byte(`{"summary":"partial checkpoint","artifacts":[],"memory":"","dispatch":[]}` + "\n"))
	}
	if f.writeSignal != nil {
		close(f.writeSignal)
	}
	<-ctx.Done()
	return runtimeexec.ExitStatus{}, ctx.Err()
}

func TestBuildCommandEnvDaemonNumber(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		number        int
		wantNumberVar bool
	}{
		{
			name:          "numbered-workflow-sets-AI_DAEMON_NUMBER",
			number:        42,
			wantNumberVar: true,
		},
		{
			name:          "autonomous-workflow-omits-AI_DAEMON_NUMBER",
			number:        0,
			wantNumberVar: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := buildCommandEnv(Request{
				Workflow: "test-workflow",
				Repo:     "owner/repo",
				Number:   tc.number,
			}, nil)
			hasNumber := slices.ContainsFunc(env, func(e string) bool {
				return strings.HasPrefix(e, "AI_DAEMON_NUMBER=")
			})
			if hasNumber != tc.wantNumberVar {
				t.Errorf("AI_DAEMON_NUMBER present=%v, want present=%v (env=%v)", hasNumber, tc.wantNumberVar, env)
			}
			if tc.wantNumberVar {
				want := "AI_DAEMON_NUMBER=42"
				if !slices.Contains(env, want) {
					t.Errorf("expected %q in env, got %v", want, env)
				}
			}
		})
	}
}

func TestBuildCommandEnvBackendOverride(t *testing.T) {
	// No t.Parallel(), t.Setenv mutates the process env and can't coexist
	// with parallel tests that read os.Environ.

	// Per-backend env is appended after the allowlist + AI_DAEMON_* vars.
	// When the same key appears in both the inherited env (via allowlist)
	// and the backend override, exec.Command uses the last occurrence, so
	// the backend override wins, as documented.
	t.Setenv("ANTHROPIC_API_KEY", "hosted-key")

	env := buildCommandEnv(
		Request{Workflow: "w", Repo: "o/r", Number: 0},
		map[string]string{
			"ANTHROPIC_API_KEY":  "proxy-key",
			"ANTHROPIC_BASE_URL": "http://localhost:8080",
			"ANTHROPIC_MODEL":    "qwen",
		},
	)

	// Every configured key must be present once as an override value.
	for _, want := range []string{
		"ANTHROPIC_API_KEY=proxy-key",
		"ANTHROPIC_BASE_URL=http://localhost:8080",
		"ANTHROPIC_MODEL=qwen",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("expected %q in env, got %v", want, env)
		}
	}

	// The override for ANTHROPIC_API_KEY must appear AFTER the allowlist
	// entry so exec picks the override.
	keyIndices := []int{}
	for i, entry := range env {
		if strings.HasPrefix(entry, "ANTHROPIC_API_KEY=") {
			keyIndices = append(keyIndices, i)
		}
	}
	if len(keyIndices) != 2 {
		t.Fatalf("expected 2 ANTHROPIC_API_KEY entries (allowlist + override), got %d: %v", len(keyIndices), env)
	}
	if env[keyIndices[1]] != "ANTHROPIC_API_KEY=proxy-key" {
		t.Errorf("last ANTHROPIC_API_KEY entry must be the override; got %q", env[keyIndices[1]])
	}
}

func TestContainerCommandRunnerUsesRuntimeAndParsesOutput(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")

	fake := &fakeContainerRunner{}
	r := NewContainerCommandRunner(
		"claude", "claude", map[string]string{"ANTHROPIC_BASE_URL": "http://proxy"},
		10, 4000, fake, "ghcr.io/example/runner:test",
		runtimeexec.ContainerSpec{},
		zerolog.Nop(),
	)

	var lines [][]byte
	got, err := r.Run(context.Background(), Request{
		Workflow: "claude:coder",
		Repo:     "owner/repo",
		Number:   7,
		System:   "system",
		User:     "user",
		OnLine: func(line []byte) {
			lines = append(lines, append([]byte(nil), line...))
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Summary != "container ok" {
		t.Fatalf("Summary = %q, want container ok", got.Summary)
	}
	if fake.spec.Image != "ghcr.io/example/runner:test" {
		t.Fatalf("container image = %q", fake.spec.Image)
	}
	if fake.spec.WorkingDir != "/workspace" {
		t.Fatalf("WorkingDir = %q, want /workspace", fake.spec.WorkingDir)
	}
	if len(fake.spec.Command) < 5 || fake.spec.Command[0] != "/bin/sh" {
		t.Fatalf("command = %v, want shell entrypoint", fake.spec.Command)
	}
	if !slices.Contains(fake.spec.Command, "claude") {
		t.Fatalf("command = %v, want claude payload", fake.spec.Command)
	}
	for _, want := range []string{
		"AI_DAEMON_WORKFLOW=claude:coder",
		"AI_DAEMON_REPO=owner/repo",
		"AI_DAEMON_NUMBER=7",
		"HOME=/tmp/agents-run/home",
		"XDG_CONFIG_HOME=/tmp/agents-run/config",
		"ANTHROPIC_BASE_URL=http://proxy",
	} {
		if !slices.Contains(fake.spec.Env, want) {
			t.Fatalf("expected env %q in %v", want, fake.spec.Env)
		}
	}
	if len(lines) != 1 || string(lines[0]) == "" {
		t.Fatalf("OnLine lines = %q, want one JSON line", lines)
	}
}

func TestContainerCommandRunnerMaterializesClaudeMCPConfig(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "token-for-test")

	fake := &fakeContainerRunner{}
	r := NewContainerCommandRunner(
		"claude", "claude", nil,
		10, 4000, fake, "ghcr.io/example/runner:test",
		runtimeexec.ContainerSpec{},
		zerolog.Nop(),
	)
	if _, err := r.Run(context.Background(), Request{System: "system", User: "user"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.spec.Command) < 8 || fake.spec.Command[0] != "/bin/sh" {
		t.Fatalf("command = %v, want shell entrypoint", fake.spec.Command)
	}
	if !slices.Contains(fake.spec.Command, "--mcp-config") || !slices.Contains(fake.spec.Command, "/tmp/agents-run/claude-mcp.json") {
		t.Fatalf("command = %v, want claude --mcp-config", fake.spec.Command)
	}
}

func TestContainerCommandRunnerMaterializesCodexHome(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("GITHUB_TOKEN", "token-for-test")

	fake := &fakeContainerRunner{}
	r := NewContainerCommandRunner(
		"codex", "codex", nil,
		10, 4000, fake, "ghcr.io/example/runner:test",
		runtimeexec.ContainerSpec{},
		zerolog.Nop(),
	)
	if _, err := r.Run(context.Background(), Request{System: "system", User: "user"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.spec.Command) < 5 || fake.spec.Command[0] != "/bin/sh" {
		t.Fatalf("command = %v, want shell entrypoint", fake.spec.Command)
	}
	if !slices.Contains(fake.spec.Env, "CODEX_HOME=/tmp/agents-run/codex") {
		t.Fatalf("env = %v, want CODEX_HOME", fake.spec.Env)
	}
	if !slices.Contains(fake.spec.Command, "--output-schema") || !slices.Contains(fake.spec.Command, "/tmp/agents-run/response-schema.json") {
		t.Fatalf("command = %v, want container-visible output schema", fake.spec.Command)
	}
	if !slices.Contains(fake.spec.Env, "AGENTS_RESPONSE_SCHEMA="+ResponseSchemaString()) {
		t.Fatalf("env missing embedded response schema")
	}
	if !strings.Contains(fake.spec.Command[2], `printf '%s' "$AGENTS_RESPONSE_SCHEMA" > /tmp/agents-run/response-schema.json`) {
		t.Fatalf("setup script = %q, want container-side response schema materialization", fake.spec.Command[2])
	}
}

func TestContainerCommandRunnerOverridesHostHomeEnv(t *testing.T) {
	t.Setenv("HOME", "/host/home")
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "/host/config")

	fake := &fakeContainerRunner{}
	r := NewContainerCommandRunner(
		"openai_compatible", "custom-cli", nil,
		10, 4000, fake, "ghcr.io/example/runner:test",
		runtimeexec.ContainerSpec{},
		zerolog.Nop(),
	)
	if _, err := r.Run(context.Background(), Request{System: "system", User: "user"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, blocked := range []string{
		"HOME=/host/home",
		"XDG_CONFIG_HOME=/host/config",
	} {
		if slices.Contains(fake.spec.Env, blocked) {
			t.Fatalf("env = %v, should not contain stale %q", fake.spec.Env, blocked)
		}
	}
	for _, want := range []string{
		"HOME=/tmp/agents-run/home",
		"TMPDIR=/tmp/agents-run",
		"XDG_CONFIG_HOME=/tmp/agents-run/config",
	} {
		if !slices.Contains(fake.spec.Env, want) {
			t.Fatalf("env = %v, want %q", fake.spec.Env, want)
		}
	}
	if len(fake.spec.Command) < 5 || fake.spec.Command[0] != "/bin/sh" {
		t.Fatalf("command = %v, want shell entrypoint", fake.spec.Command)
	}
}

func TestExtractJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "pure json",
			input: `{"summary":"ok","artifacts":[]}`,
			want:  `{"summary":"ok","artifacts":[]}`,
		},
		{
			name:  "text before json",
			input: "The review was submitted successfully.\n\n{\"summary\":\"ok\",\"artifacts\":[]}",
			want:  `{"summary":"ok","artifacts":[]}`,
		},
		{
			name:  "text before and after json with trailing newline",
			input: "Some preamble text.\n{\"summary\":\"ok\",\"artifacts\":[]}\n",
			want:  `{"summary":"ok","artifacts":[]}`,
		},
		{
			name:  "nested braces",
			input: "Hello\n{\"a\":{\"b\":{\"c\":1}}}",
			want:  `{"a":{"b":{"c":1}}}`,
		},
		{
			name:  "braces inside strings",
			input: `preamble {"key":"value with } and { inside"}`,
			want:  `{"key":"value with } and { inside"}`,
		},
		{
			// Escaped backslash immediately before closing quote: the JSON value
			// "path\\" ends with 0x5c 0x5c 0x22 in the byte stream.  The old
			// backward scanner misread the final '"' as escaped (the preceding
			// byte is '\'), so it walked past the true object boundary.
			name:  "string ending with escaped backslash",
			input: `{"path":"C:\\Users\\foo\\"}`,
			want:  `{"path":"C:\\Users\\foo\\"}`,
		},
		{
			name:  "text before json with escaped backslash in value",
			input: "Preamble.\n" + `{"path":"C:\\Users\\foo\\"}`,
			want:  `{"path":"C:\\Users\\foo\\"}`,
		},
		{
			// When the output contains multiple top-level JSON objects the last
			// one is returned (matches original backward-scan contract).
			name:  "multiple top-level objects returns last",
			input: `{"a":1} some text {"b":2}`,
			want:  `{"b":2}`,
		},
		{
			// A top-level JSON array whose only element is an object: the scanner
			// locates the '{' inside the array and returns that object.  This pins
			// the documented contract, future changes that alter this behaviour
			// should update this test intentionally.
			name:  "top-level array containing object returns the inner object",
			input: `[{"a":1}]`,
			want:  `{"a":1}`,
		},
		{
			// A top-level array followed by a top-level object returns the object.
			name:  "top-level array then object returns object",
			input: `[1,2,3] {"summary":"ok"}`,
			want:  `{"summary":"ok"}`,
		},
		{
			name:    "no json",
			input:   "just plain text",
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := extractJSON([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("got %q, want %q", string(got), tt.want)
			}
		})
	}
}

func TestResponseWithDispatchRoundTrips(t *testing.T) {
	t.Parallel()
	src := Response{
		Summary: "ok",
		Artifacts: []Artifact{
			{Type: "comment", PartKey: "body", GitHubID: "123"},
		},
		Dispatch: []DispatchRequest{
			{Agent: "pr-reviewer", Number: 42, Reason: "ready for review"},
		},
	}
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Response
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Dispatch) != 1 {
		t.Fatalf("dispatch length: got %d, want 1", len(got.Dispatch))
	}
	d := got.Dispatch[0]
	if d.Agent != "pr-reviewer" || d.Number != 42 || d.Reason != "ready for review" {
		t.Errorf("dispatch mismatch: %+v", d)
	}
}

func TestResponseDispatchOmittedWhenEmpty(t *testing.T) {
	t.Parallel()
	r := Response{Summary: "ok"}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "dispatch") {
		t.Errorf("dispatch should be omitted when empty; got: %s", string(data))
	}
}

// TestCommandRunnerEmptyStdoutIsError verifies that a backend command that
// exits zero but produces no output is treated as a failed run rather than a
// silent success with an empty Response.
func TestCommandRunnerEmptyStdoutIsError(t *testing.T) {
	t.Parallel()
	fake := &silentContainerRunner{}
	r := NewContainerCommandRunner(
		"test", "test-cli", nil,
		10, 4000, fake, "ghcr.io/example/runner:test",
		runtimeexec.ContainerSpec{},
		zerolog.Nop(),
	)
	_, err := r.Run(context.Background(), Request{Workflow: "wf", Repo: "owner/repo"})
	if err == nil {
		t.Fatal("expected error for empty stdout, got nil")
	}
	if !strings.Contains(err.Error(), "empty response") {
		t.Errorf("expected 'empty response' in error, got: %v", err)
	}
}

func TestCommandRunnerTimeoutWithPartialOutputReturnsErrorAndResponse(t *testing.T) {
	t.Parallel()
	fake := &timeoutContainerRunner{writeJSON: true, writeSignal: make(chan struct{})}
	r := NewContainerCommandRunner(
		"codex", "codex", nil,
		1, 4000, fake, "ghcr.io/example/runner:test",
		runtimeexec.ContainerSpec{},
		zerolog.Nop(),
	)
	got, err := r.Run(context.Background(), Request{Workflow: "wf", Repo: "owner/repo"})
	if err == nil {
		t.Fatal("Run error = nil, want timeout error")
	}
	var interrupted CommandInterruptedError
	if !errors.As(err, &interrupted) {
		t.Fatalf("Run error = %T %v, want CommandInterruptedError", err, err)
	}
	if interrupted.Kind != "timeout" {
		t.Fatalf("interruption kind = %q, want timeout", interrupted.Kind)
	}
	if got.Summary != "partial checkpoint" {
		t.Fatalf("summary = %q, want partial checkpoint", got.Summary)
	}
}

// TestExtractStructuredOutputFallbackHandlesStreamJSON verifies that when the
// full stdout cannot be parsed as a single JSON envelope (as happens with
// --output-format stream-json output), the fallback path locates the final
// result event line and correctly extracts the structured_output from it.
func TestExtractStructuredOutputFallbackHandlesStreamJSON(t *testing.T) {
	t.Parallel()

	structuredPayload := `{"summary":"all done","artifacts":[],"memory":"","dispatch":[]}`
	streamOut := `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"ls"}}]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"file.txt"}]}}` + "\n" +
		`{"type":"result","subtype":"success","structured_output":` + structuredPayload + `}` + "\n"

	// Full stdout is JSONL, extractStructuredOutput on the whole thing fails,
	// falls through to extractJSON → last object → extractStructuredOutput succeeds.
	full := []byte(streamOut)

	// Step 1: full stdout is NOT a single-JSON envelope.
	if _, ok := extractStructuredOutput(full); ok {
		t.Fatal("expected extractStructuredOutput to fail on JSONL stdout, but it succeeded")
	}

	// Step 2: extractJSON returns the last JSON object (result event).
	lastJSON, err := extractJSON(full)
	if err != nil {
		t.Fatalf("extractJSON failed: %v", err)
	}

	// Step 3: extractStructuredOutput on the result event succeeds.
	parsed, ok := extractStructuredOutput(lastJSON)
	if !ok {
		t.Fatalf("extractStructuredOutput failed on result event: %s", string(lastJSON))
	}

	var resp Response
	if err := json.Unmarshal(parsed, &resp); err != nil {
		t.Fatalf("unmarshal structured_output: %v", err)
	}
	if resp.Summary != "all done" {
		t.Errorf("summary = %q, want %q", resp.Summary, "all done")
	}
}

func TestExtractUsageAnthropicShape(t *testing.T) {
	t.Parallel()
	stdout := []byte(`{"type":"result","structured_output":{"summary":"x"},"usage":{"input_tokens":1234,"output_tokens":567,"cache_creation_input_tokens":100,"cache_read_input_tokens":2000}}`)
	got := extractUsage(stdout)
	want := Usage{InputTokens: 1234, OutputTokens: 567, CacheReadTokens: 2000, CacheWriteTokens: 100}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestExtractUsageOpenAIShape(t *testing.T) {
	t.Parallel()
	stdout := []byte(`{"type":"result","structured_output":{"summary":"x"},"usage":{"prompt_tokens":800,"completion_tokens":120,"total_tokens":920}}`)
	got := extractUsage(stdout)
	want := Usage{InputTokens: 800, OutputTokens: 120}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestExtractUsageMissingReturnsZero(t *testing.T) {
	t.Parallel()
	if got := extractUsage([]byte(`{"type":"result","structured_output":{"summary":"x"}}`)); got != (Usage{}) {
		t.Fatalf("expected zero Usage, got %+v", got)
	}
	if got := extractUsage([]byte(`not json at all`)); got != (Usage{}) {
		t.Fatalf("expected zero Usage on non-JSON, got %+v", got)
	}
}

// TestBuildDeliveryClaudeUsesAppendSystemPrompt verifies that the claude
// backend routes system content through --append-system-prompt and leaves user
// content for stdin, preserving Claude Code's default tool stack.
func TestBuildDeliveryClaudeUsesAppendSystemPrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		backendName string
		system      string
		user        string
		wantFlag    bool // whether --append-system-prompt flag is expected
	}{
		{
			name:        "claude-routes-system-via-flag",
			backendName: "claude",
			system:      "You are a reviewer.",
			user:        "Review PR #5.",
			wantFlag:    true,
		},
		{
			name:        "claude-local-also-uses-flag",
			backendName: "claude_local",
			system:      "System guidance.",
			user:        "Runtime context.",
			wantFlag:    true,
		},
		{
			name:        "codex-concatenates-on-stdin",
			backendName: "codex",
			system:      "System guidance.",
			user:        "Runtime context.",
			wantFlag:    false,
		},
		{
			name:        "unknown-backend-concatenates",
			backendName: "openai_compatible",
			system:      "System guidance.",
			user:        "Runtime context.",
			wantFlag:    false,
		},
		{
			name:        "claude-empty-system-no-flag",
			backendName: "claude",
			system:      "",
			user:        "Only user content.",
			wantFlag:    false, // no flag when system is empty
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newCommandRunner(tc.backendName, "test-cli", nil, 10, 0, zerolog.Nop())
			args, stdin := r.buildDelivery(Request{System: tc.system, User: tc.user})

			hasFlag := slices.Contains(args, "--append-system-prompt")
			if hasFlag != tc.wantFlag {
				t.Errorf("--append-system-prompt present=%v, want=%v (args=%v)", hasFlag, tc.wantFlag, args)
			}

			if tc.wantFlag {
				// System must be the value immediately after the flag.
				i := slices.Index(args, "--append-system-prompt")
				if i < 0 || i+1 >= len(args) {
					t.Fatalf("--append-system-prompt has no following value in args=%v", args)
				}
				if args[i+1] != tc.system {
					t.Errorf("--append-system-prompt value = %q, want %q", args[i+1], tc.system)
				}
				// User content goes on stdin (maxPromptChars=0 means no truncation).
				if stdin != tc.user {
					t.Errorf("stdin = %q, want user content %q", stdin, tc.user)
				}
			} else {
				// Non-claude (or empty system): combined content on stdin.
				if tc.system != "" {
					if !strings.Contains(stdin, tc.system) {
						t.Errorf("stdin missing system content; stdin=%q", stdin)
					}
					if !strings.Contains(stdin, tc.user) {
						t.Errorf("stdin missing user content; stdin=%q", stdin)
					}
				} else {
					if stdin != tc.user {
						t.Errorf("stdin = %q, want user content %q", stdin, tc.user)
					}
				}
			}
		})
	}
}

// TestBuildDeliveryRespectsTotalBudget verifies that System+User together never
// exceed maxPromptChars on the Claude backend, matching the codex flat-prompt
// truncation boundary.
func TestBuildDeliveryRespectsTotalBudget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		backendName   string
		system        string
		user          string
		maxChars      int
		wantStdin     string
		wantSystemArg string // expected --append-system-prompt value; "" means not checked
	}{
		{
			// system=5 runes, sep=2, budget=8 → user gets 1 rune
			name:        "claude-user-truncated-to-remaining-headroom",
			backendName: "claude",
			system:      "abcde",
			user:        "0123456789",
			maxChars:    8,
			wantStdin:   "0",
		},
		{
			// system exactly fills budget: user must be empty
			name:        "claude-system-fills-budget-user-empty",
			backendName: "claude",
			system:      "abcdefgh",
			user:        "extra",
			maxChars:    8,
			wantStdin:   "",
		},
		{
			// system exceeds budget: system is truncated, user is empty
			name:          "claude-system-exceeds-budget-system-truncated",
			backendName:   "claude",
			system:        "abcdefghi",
			user:          "x",
			maxChars:      8,
			wantStdin:     "",
			wantSystemArg: "abcdefgh",
		},
		{
			// Truncation falls within the "\n\n" separator: system(5) + sep(2) > budget(6).
			// Claude must pass the truncated combined prefix ("abcde\n") as the system
			// arg and produce empty stdin, matching codex which sends "abcde\n".
			name:          "claude-truncation-in-separator",
			backendName:   "claude",
			system:        "abcde",
			user:          "x",
			maxChars:      6,
			wantStdin:     "",
			wantSystemArg: "abcde\n",
		},
		{
			// unlimited (maxChars=0): no truncation on either part
			name:        "claude-unlimited-no-truncation",
			backendName: "claude",
			system:      "abcde",
			user:        "0123456789",
			maxChars:    0,
			wantStdin:   "0123456789",
		},
		{
			// codex concatenation path: system + "\n\n" + user, truncated to budget
			name:        "codex-concatenation-truncated",
			backendName: "codex",
			system:      "abcde",
			user:        "0123456789",
			maxChars:    8,
			wantStdin:   "abcde\n\n0",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newCommandRunner(tc.backendName, "test-cli", nil, 10, tc.maxChars, zerolog.Nop())
			args, stdin := r.buildDelivery(Request{System: tc.system, User: tc.user})
			if stdin != tc.wantStdin {
				t.Errorf("stdin = %q, want %q", stdin, tc.wantStdin)
			}
			if tc.wantSystemArg != "" {
				found := ""
				if i := slices.Index(args, "--append-system-prompt"); i >= 0 && i+1 < len(args) {
					found = args[i+1]
				}
				if found != tc.wantSystemArg {
					t.Errorf("--append-system-prompt = %q, want %q", found, tc.wantSystemArg)
				}
			}
		})
	}
}

// TestBuildDeliveryClaudeAndCodexSameTruncationBoundary asserts that for the
// same request, Claude and codex deliver exactly the same combined logical
// prompt (System+"\n\n"+User) up to maxPromptChars runes.
func TestBuildDeliveryClaudeAndCodexSameTruncationBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		system   string
		user     string
		maxChars int
	}{
		{
			name:     "basic-truncation",
			system:   "abcde",
			user:     "0123456789",
			maxChars: 8, // system=5 + sep=2 + user=1 = 8
		},
		{
			name:     "system-fills-budget",
			system:   "abcdefgh",
			user:     "extra",
			maxChars: 8,
		},
		{
			name:     "system-exceeds-budget",
			system:   "abcdefghi",
			user:     "x",
			maxChars: 8,
		},
		{
			// Truncation falls inside the "\n\n" separator (budget=6, system=5,
			// sep would start at 5 and end at 6 but budget cuts after 6th rune).
			// Claude must deliver the same 6 runes as codex even though the cut
			// falls in non-semantic separator whitespace.
			name:     "truncation-in-separator",
			system:   "abcde",
			user:     "x",
			maxChars: 6,
		},
		{
			name:     "no-truncation-needed",
			system:   "abc",
			user:     "xyz",
			maxChars: 100,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			claude := newCommandRunner("claude", "test-cli", nil, 10, tc.maxChars, zerolog.Nop())
			codex := newCommandRunner("codex", "test-cli", nil, 10, tc.maxChars, zerolog.Nop())

			claudeArgs, claudeStdin := claude.buildDelivery(Request{System: tc.system, User: tc.user})
			_, codexStdin := codex.buildDelivery(Request{System: tc.system, User: tc.user})

			// Reconstruct the logical combined prompt from the claude delivery.
			claudeSystemArg := ""
			if i := slices.Index(claudeArgs, "--append-system-prompt"); i >= 0 && i+1 < len(claudeArgs) {
				claudeSystemArg = claudeArgs[i+1]
			}
			var claudeCombined string
			if claudeStdin != "" {
				claudeCombined = claudeSystemArg + "\n\n" + claudeStdin
			} else {
				claudeCombined = claudeSystemArg
			}

			if claudeCombined != codexStdin {
				t.Errorf("claude combined=%q, codex stdin=%q; want identical logical prompts", claudeCombined, codexStdin)
			}
		})
	}
}

// TestCombineSystemUser verifies the separator rule: "\n\n" is only inserted
// when both parts are non-empty.
func TestCombineSystemUser(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		system string
		user   string
		want   string
	}{
		{name: "both-present", system: "sys", user: "usr", want: "sys\n\nusr"},
		{name: "system-only", system: "sys", user: "", want: "sys"},
		{name: "user-only", system: "", user: "usr", want: "usr"},
		{name: "both-empty", system: "", user: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := combineSystemUser(tc.system, tc.user)
			if got != tc.want {
				t.Errorf("combineSystemUser(%q, %q) = %q, want %q", tc.system, tc.user, got, tc.want)
			}
		})
	}
}

func TestParseClaudeSteps(t *testing.T) {
	t.Parallel()

	// toTimedLines converts raw JSONL bytes to []timedLine with a fixed base
	// time, simulating lineCapture.addLine but with a controllable clock.
	toTimedLines := func(data []byte, base time.Time, step time.Duration) []timedLine {
		var tls []timedLine
		for raw := range bytes.Lines(data) {
			raw = bytes.TrimSuffix(raw, []byte("\n"))
			if len(raw) == 0 {
				continue
			}
			line := append(bytes.Clone(raw), '\n')
			tls = append(tls, timedLine{data: line, at: base})
			base = base.Add(step)
		}
		return tls
	}

	// streamJSON builds a minimal stream-json JSONL output with the given
	// assistant→user event pairs. The final line is a result event.
	streamJSON := func(pairs [][2]string, finalOutput string) []byte {
		// pairs[i][0] = tool_name, pairs[i][1] = tool_use_id
		var lines []string
		for i, p := range pairs {
			name, id := p[0], p[1]
			lines = append(lines,
				`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"`+id+`","name":"`+name+`","input":{"arg":"val`+string(rune('0'+i))+`"}}]}}`,
				`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"`+id+`","content":"result`+string(rune('0'+i))+`"}]}}`,
			)
		}
		lines = append(lines, `{"type":"result","subtype":"success","structured_output":`+finalOutput+`}`)
		out := ""
		for _, l := range lines {
			out += l + "\n"
		}
		return []byte(out)
	}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		input        []byte
		lineStep     time.Duration
		wantNames    []string
		wantMinDurMs int64 // minimum DurationMs for the first step (0 = don't check)
	}{
		{
			name:      "empty input returns nil",
			input:     []byte(""),
			wantNames: nil,
		},
		{
			name:      "single tool call",
			input:     streamJSON([][2]string{{"Bash", "toolu_01"}}, `{"summary":"ok","artifacts":[]}`),
			wantNames: []string{"Bash"},
		},
		{
			name: "multiple tool calls",
			input: streamJSON([][2]string{
				{"Bash", "toolu_01"},
				{"Read", "toolu_02"},
				{"Write", "toolu_03"},
			}, `{"summary":"done","artifacts":[]}`),
			wantNames: []string{"Bash", "Read", "Write"},
		},
		{
			name:      "result-only output, no tool events",
			input:     []byte(`{"type":"result","subtype":"success","structured_output":{"summary":"ok","artifacts":[]}}` + "\n"),
			wantNames: nil,
		},
		{
			// Each line arrives 500 ms apart. tool_use is line 0, tool_result is
			// line 1 → DurationMs must be ≥ 500.
			name:         "duration reflects per-line timestamps",
			input:        streamJSON([][2]string{{"Bash", "toolu_01"}}, `{"summary":"ok","artifacts":[]}`),
			lineStep:     500 * time.Millisecond,
			wantNames:    []string{"Bash"},
			wantMinDurMs: 500,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			step := tc.lineStep
			if step == 0 {
				step = 0 // zero means all lines share the same timestamp
			}
			lines := toTimedLines(tc.input, base, step)
			steps := parseClaudeSteps(lines)
			if len(steps) != len(tc.wantNames) {
				t.Fatalf("got %d steps, want %d: %+v", len(steps), len(tc.wantNames), steps)
			}
			for i, s := range steps {
				if s.ToolName != tc.wantNames[i] {
					t.Errorf("step %d: tool_name = %q, want %q", i, s.ToolName, tc.wantNames[i])
				}
				if s.InputSummary == "" {
					t.Errorf("step %d: input_summary should not be empty", i)
				}
				if s.OutputSummary == "" {
					t.Errorf("step %d: output_summary should not be empty", i)
				}
			}
			if tc.wantMinDurMs > 0 && len(steps) > 0 {
				if steps[0].DurationMs < tc.wantMinDurMs {
					t.Errorf("step 0: DurationMs = %d, want >= %d", steps[0].DurationMs, tc.wantMinDurMs)
				}
			}
			for i, s := range steps {
				if s.Kind != StepKindTool {
					t.Errorf("step %d: Kind = %q, want %q (tool-only fixture)", i, s.Kind, StepKindTool)
				}
			}
		})
	}
}

// TestParseClaudeStepsThinking covers the kind="thinking" path: text content
// blocks emitted by the assistant between tool calls, persisted as their own
// steps so the Traces detail page can replay reasoning alongside tool use.
func TestParseClaudeStepsThinking(t *testing.T) {
	t.Parallel()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	toLines := func(raw string) []timedLine {
		return timedLines(raw, base)
	}

	t.Run("single text block becomes one thinking step", func(t *testing.T) {
		t.Parallel()
		raw := `{"type":"assistant","message":{"content":[{"type":"text","text":"Let me check the file structure first."}]}}`
		steps := parseClaudeSteps(toLines(raw))
		if len(steps) != 1 {
			t.Fatalf("got %d steps, want 1: %+v", len(steps), steps)
		}
		s := steps[0]
		if s.Kind != StepKindThinking {
			t.Errorf("Kind = %q, want %q", s.Kind, StepKindThinking)
		}
		if s.ToolName != "" {
			t.Errorf("ToolName = %q, want empty", s.ToolName)
		}
		if s.InputSummary != "Let me check the file structure first." {
			t.Errorf("InputSummary = %q, want full text", s.InputSummary)
		}
		if s.OutputSummary != "" {
			t.Errorf("OutputSummary = %q, want empty", s.OutputSummary)
		}
	})

	t.Run("text then tool_use emits thinking then tool in order", func(t *testing.T) {
		t.Parallel()
		raw := `{"type":"assistant","message":{"content":[{"type":"text","text":"I need to read the README."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01","name":"Read","input":{"path":"README.md"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"# Project"}]}}`
		steps := parseClaudeSteps(toLines(raw))
		if len(steps) != 2 {
			t.Fatalf("got %d steps, want 2: %+v", len(steps), steps)
		}
		if steps[0].Kind != StepKindThinking {
			t.Errorf("step 0: Kind = %q, want %q", steps[0].Kind, StepKindThinking)
		}
		if steps[1].Kind != StepKindTool {
			t.Errorf("step 1: Kind = %q, want %q", steps[1].Kind, StepKindTool)
		}
		if steps[1].ToolName != "Read" {
			t.Errorf("step 1: ToolName = %q, want Read", steps[1].ToolName)
		}
	})

	t.Run("multiple text blocks in one assistant event become separate thinking steps", func(t *testing.T) {
		t.Parallel()
		raw := `{"type":"assistant","message":{"content":[{"type":"text","text":"First thought."},{"type":"text","text":"Second thought."}]}}`
		steps := parseClaudeSteps(toLines(raw))
		if len(steps) != 2 {
			t.Fatalf("got %d steps, want 2: %+v", len(steps), steps)
		}
		for i, want := range []string{"First thought.", "Second thought."} {
			if steps[i].Kind != StepKindThinking {
				t.Errorf("step %d: Kind = %q, want %q", i, steps[i].Kind, StepKindThinking)
			}
			if steps[i].InputSummary != want {
				t.Errorf("step %d: InputSummary = %q, want %q", i, steps[i].InputSummary, want)
			}
		}
	})

	t.Run("empty or whitespace-only text blocks are skipped", func(t *testing.T) {
		t.Parallel()
		raw := `{"type":"assistant","message":{"content":[{"type":"text","text":"  "},{"type":"text","text":""}]}}`
		steps := parseClaudeSteps(toLines(raw))
		if len(steps) != 0 {
			t.Errorf("got %d steps, want 0: %+v", len(steps), steps)
		}
	})

	t.Run("oversized thinking text is truncated with marker", func(t *testing.T) {
		t.Parallel()
		// Build a text block well over 64 KB.
		big := strings.Repeat("x", 70*1024)
		raw := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + big + `"}]}}`
		steps := parseClaudeSteps(toLines(raw))
		if len(steps) != 1 {
			t.Fatalf("got %d steps, want 1", len(steps))
		}
		s := steps[0]
		if !strings.Contains(s.InputSummary, "[truncated,") {
			t.Errorf("expected truncation marker in InputSummary, got tail: %q", tail(s.InputSummary, 80))
		}
		if len(s.InputSummary) > stepContentMaxBytes+200 {
			t.Errorf("InputSummary too long: %d bytes (cap+marker should be near %d)", len(s.InputSummary), stepContentMaxBytes)
		}
	})

	t.Run("oversized tool input is truncated with marker", func(t *testing.T) {
		t.Parallel()
		// 70 KB string baked into the tool_use input args.
		big := strings.Repeat("a", 70*1024)
		raw := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"cmd":"` + big + `"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"ok"}]}}`
		steps := parseClaudeSteps(toLines(raw))
		if len(steps) != 1 {
			t.Fatalf("got %d steps, want 1", len(steps))
		}
		if !strings.Contains(steps[0].InputSummary, "[truncated,") {
			t.Errorf("expected truncation marker in tool InputSummary")
		}
	})
}

// TestParseCodexSteps covers the codex --json JSONL parser. Fixtures are
// captured from real `codex exec --json` runs against the production
// container; the shape (item.started / item.completed events with
// item.type = agent_message | command_execution | mcp_tool_call) is
// stable for the codex 0.124+ family.
func TestParseCodexSteps(t *testing.T) {
	t.Parallel()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	toLines := func(raw string) []timedLine {
		return timedLines(raw, base)
	}

	t.Run("agent_message becomes thinking step", func(t *testing.T) {
		t.Parallel()
		raw := `{"type":"thread.started","thread_id":"x"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"The first 3 prime numbers are 2, 3, 5."}}
{"type":"turn.completed","usage":{"input_tokens":17000,"output_tokens":50}}`
		steps := parseCodexSteps(toLines(raw))
		if len(steps) != 1 {
			t.Fatalf("got %d steps, want 1: %+v", len(steps), steps)
		}
		s := steps[0]
		if s.Kind != StepKindThinking {
			t.Errorf("Kind = %q, want %q", s.Kind, StepKindThinking)
		}
		if s.InputSummary != "The first 3 prime numbers are 2, 3, 5." {
			t.Errorf("InputSummary = %q", s.InputSummary)
		}
	})

	t.Run("command_execution emits tool step with input/output", func(t *testing.T) {
		t.Parallel()
		raw := `{"type":"thread.started","thread_id":"x"}
{"type":"turn.started"}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"/bin/bash -lc 'ls /etc | head -3'","aggregated_output":"","status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"/bin/bash -lc 'ls /etc | head -3'","aggregated_output":"alpine-release\napk\nbash\n","exit_code":0,"status":"completed"}}
{"type":"turn.completed","usage":{}}`
		steps := parseCodexSteps(toLines(raw))
		if len(steps) != 1 {
			t.Fatalf("got %d steps, want 1: %+v", len(steps), steps)
		}
		s := steps[0]
		if s.Kind != StepKindTool {
			t.Errorf("Kind = %q, want %q", s.Kind, StepKindTool)
		}
		if s.ToolName != "bash" {
			t.Errorf("ToolName = %q, want bash", s.ToolName)
		}
		if !strings.Contains(s.InputSummary, "ls /etc") {
			t.Errorf("InputSummary = %q, want command", s.InputSummary)
		}
		if !strings.Contains(s.OutputSummary, "alpine-release") {
			t.Errorf("OutputSummary = %q, want command output", s.OutputSummary)
		}
	})

	t.Run("interleaved thinking and tool preserve order", func(t *testing.T) {
		t.Parallel()
		raw := `{"type":"thread.started","thread_id":"x"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Running the command."}}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"ls","aggregated_output":"","status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"ls","aggregated_output":"foo\nbar","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Found foo and bar."}}
{"type":"turn.completed","usage":{}}`
		steps := parseCodexSteps(toLines(raw))
		if len(steps) != 3 {
			t.Fatalf("got %d steps, want 3: %+v", len(steps), steps)
		}
		want := []string{StepKindThinking, StepKindTool, StepKindThinking}
		for i, k := range want {
			if steps[i].Kind != k {
				t.Errorf("step %d: Kind = %q, want %q", i, steps[i].Kind, k)
			}
		}
	})

	t.Run("mcp_tool_call uses generic tool fallback", func(t *testing.T) {
		t.Parallel()
		raw := `{"type":"item.completed","item":{"id":"item_3","type":"mcp_tool_call","name":"create_issue","server":"github","arguments":{"title":"x"},"output":{"id":42}}}`
		steps := parseCodexSteps(toLines(raw))
		if len(steps) != 1 {
			t.Fatalf("got %d steps, want 1: %+v", len(steps), steps)
		}
		s := steps[0]
		if s.Kind != StepKindTool {
			t.Errorf("Kind = %q, want %q", s.Kind, StepKindTool)
		}
		if s.ToolName != "github.create_issue" {
			t.Errorf("ToolName = %q, want github.create_issue", s.ToolName)
		}
		if !strings.Contains(s.InputSummary, "title") {
			t.Errorf("InputSummary missing arguments: %q", s.InputSummary)
		}
		if !strings.Contains(s.OutputSummary, "42") {
			t.Errorf("OutputSummary missing output: %q", s.OutputSummary)
		}
	})

	t.Run("mcp_tool_call with tool+result fields persists correctly", func(t *testing.T) {
		t.Parallel()
		// Real codex shape from the live SSE feed: identifier on `tool`,
		// output on `result`. The earlier persistence path looked only at
		// `name` and `output`, dropping these items silently. Regression
		// test for that bug.
		raw := `{"type":"item.completed","item":{"id":"item_8","type":"mcp_tool_call","server":"github","tool":"issue_read","arguments":{"owner":"eloylp","repo":"agents","issue_number":411},"result":"ok","status":"completed"}}`
		steps := parseCodexSteps(toLines(raw))
		if len(steps) != 1 {
			t.Fatalf("got %d steps, want 1: %+v", len(steps), steps)
		}
		s := steps[0]
		if s.ToolName != "github.issue_read" {
			t.Errorf("ToolName = %q, want github.issue_read", s.ToolName)
		}
		if !strings.Contains(s.InputSummary, "issue_number") {
			t.Errorf("InputSummary missing arguments: %q", s.InputSummary)
		}
		if s.OutputSummary != "ok" {
			t.Errorf("OutputSummary = %q, want ok (from result field)", s.OutputSummary)
		}
	})

	t.Run("empty agent_message is skipped", func(t *testing.T) {
		t.Parallel()
		raw := `{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"  "}}`
		steps := parseCodexSteps(toLines(raw))
		if len(steps) != 0 {
			t.Errorf("got %d steps, want 0: %+v", len(steps), steps)
		}
	})

	t.Run("turn.started and thread.started are ignored", func(t *testing.T) {
		t.Parallel()
		raw := `{"type":"thread.started","thread_id":"x"}
{"type":"turn.started"}
{"type":"turn.completed","usage":{}}`
		steps := parseCodexSteps(toLines(raw))
		if len(steps) != 0 {
			t.Errorf("got %d steps, want 0: %+v", len(steps), steps)
		}
	})

	t.Run("oversized command output truncated with marker", func(t *testing.T) {
		t.Parallel()
		big := strings.Repeat("x", 70*1024)
		raw := `{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"cat big","aggregated_output":"` + big + `","exit_code":0,"status":"completed"}}`
		steps := parseCodexSteps(toLines(raw))
		if len(steps) != 1 {
			t.Fatalf("got %d steps, want 1", len(steps))
		}
		if !strings.Contains(steps[0].OutputSummary, "[truncated,") {
			t.Errorf("expected truncation marker in OutputSummary")
		}
	})
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

func timedLines(raw string, at time.Time) []timedLine {
	var tls []timedLine
	for line := range strings.SplitSeq(strings.TrimRight(raw, "\n"), "\n") {
		if line == "" {
			continue
		}
		tls = append(tls, timedLine{data: []byte(line + "\n"), at: at})
	}
	return tls
}

func TestExtractToolResultText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain string", input: `"hello world"`, want: "hello world"},
		{name: "empty string", input: `""`, want: ""},
		{name: "text block array", input: `[{"type":"text","text":"foo"},{"type":"text","text":"bar"}]`, want: "foo\nbar"},
		{name: "mixed block array", input: `[{"type":"image"},{"type":"text","text":"only this"}]`, want: "only this"},
		{name: "empty raw", input: ``, want: ""},
		{name: "null", input: `null`, want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractToolResultText(json.RawMessage(tc.input))
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHasBackendPrefix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		beName  string
		command string
		prefix  string
		want    bool
	}{
		{name: "exact name match", beName: "claude", command: "", prefix: "claude", want: true},
		{name: "name with spaces", beName: "  claude-prod  ", command: "", prefix: "claude", want: true},
		{name: "name uppercase", beName: "Claude", command: "", prefix: "claude", want: true},
		{name: "command basename match", beName: "", command: "/usr/local/bin/claude", prefix: "claude", want: true},
		{name: "command basename uppercase", beName: "", command: "/usr/bin/Claude", prefix: "claude", want: true},
		{name: "command with spaces", beName: "", command: "  /usr/bin/claude  ", prefix: "claude", want: true},
		{name: "no match", beName: "openai", command: "/usr/bin/gpt", prefix: "claude", want: false},
		{name: "codex via name", beName: "codex-fast", command: "", prefix: "codex", want: true},
		{name: "codex via command", beName: "", command: "/opt/bin/codex", prefix: "codex", want: true},
		{name: "empty name and command", beName: "", command: "", prefix: "claude", want: false},
		{name: "substring only, not a prefix", beName: "anthropic-claude", command: "/usr/bin/gpt", prefix: "claude", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &CommandRunner{backendName: tc.beName, command: tc.command}
			if got := r.hasBackendPrefix(tc.prefix); got != tc.want {
				t.Errorf("hasBackendPrefix(%q) with name=%q command=%q = %v, want %v",
					tc.prefix, tc.beName, tc.command, got, tc.want)
			}
		})
	}
}

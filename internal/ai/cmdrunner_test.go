package ai

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestBuildCommandEnvDaemonNumber(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		number         int
		wantNumberVar  bool
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
	// No t.Parallel() — t.Setenv mutates the process env and can't coexist
	// with parallel tests that read os.Environ.

	// Per-backend env is appended after the allowlist + AI_DAEMON_* vars.
	// When the same key appears in both the inherited env (via allowlist)
	// and the backend override, exec.Command uses the last occurrence, so
	// the backend override wins — as documented.
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
			// the documented contract — future changes that alter this behaviour
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
	// "true" exits 0 with no stdout — the canonical empty-output case.
	r := NewCommandRunner("test", "command", "true", nil, nil, 10, 4000, "", zerolog.Nop())
	_, err := r.Run(context.Background(), Request{Workflow: "wf", Repo: "owner/repo"})
	if err == nil {
		t.Fatal("expected error for empty stdout, got nil")
	}
	if !strings.Contains(err.Error(), "empty response") {
		t.Errorf("expected 'empty response' in error, got: %v", err)
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
		staticArgs  []string
		system      string
		user        string
		wantFlag    bool // whether --append-system-prompt flag is expected
	}{
		{
			name:        "claude-routes-system-via-flag",
			backendName: "claude",
			staticArgs:  []string{"--dangerously-skip-permissions"},
			system:      "You are a reviewer.",
			user:        "Review PR #5.",
			wantFlag:    true,
		},
		{
			name:        "claude-local-also-uses-flag",
			backendName: "claude_local",
			staticArgs:  nil,
			system:      "System guidance.",
			user:        "Runtime context.",
			wantFlag:    true,
		},
		{
			name:        "codex-concatenates-on-stdin",
			backendName: "codex",
			staticArgs:  []string{"exec"},
			system:      "System guidance.",
			user:        "Runtime context.",
			wantFlag:    false,
		},
		{
			name:        "unknown-backend-concatenates",
			backendName: "openai_compatible",
			staticArgs:  nil,
			system:      "System guidance.",
			user:        "Runtime context.",
			wantFlag:    false,
		},
		{
			name:        "claude-empty-system-no-flag",
			backendName: "claude",
			staticArgs:  nil,
			system:      "",
			user:        "Only user content.",
			wantFlag:    false, // no flag when system is empty
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := NewCommandRunner(tc.backendName, "command", "true", tc.staticArgs, nil, 10, 0, "", zerolog.Nop())
			args, stdin := r.buildDelivery(Request{System: tc.system, User: tc.user})

			hasFlag := slices.Contains(args, "--append-system-prompt")
			if hasFlag != tc.wantFlag {
				t.Errorf("--append-system-prompt present=%v, want=%v (args=%v)", hasFlag, tc.wantFlag, args)
			}

			if tc.wantFlag {
				// System must be the value immediately after the flag.
				for i, a := range args {
					if a == "--append-system-prompt" {
						if i+1 >= len(args) {
							t.Fatalf("--append-system-prompt has no following value in args=%v", args)
						}
						if args[i+1] != tc.system {
							t.Errorf("--append-system-prompt value = %q, want %q", args[i+1], tc.system)
						}
						break
					}
				}
				// User content goes on stdin.
				if stdin != tc.user {
					t.Errorf("stdin = %q, want user content %q", stdin, tc.user)
				}
				// Static args must still be present.
				for _, sa := range tc.staticArgs {
					if !slices.Contains(args, sa) {
						t.Errorf("static arg %q missing from args=%v", sa, args)
					}
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

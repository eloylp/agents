package ai

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

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

	// Full stdout is JSONL — extractStructuredOutput on the whole thing fails,
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
				// User content goes on stdin (maxPromptChars=0 means no truncation).
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
			// arg and produce empty stdin — matching codex which sends "abcde\n".
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
			r := NewCommandRunner(tc.backendName, "command", "true", nil, nil, 10, tc.maxChars, "", zerolog.Nop())
			args, stdin := r.buildDelivery(Request{System: tc.system, User: tc.user})
			if stdin != tc.wantStdin {
				t.Errorf("stdin = %q, want %q", stdin, tc.wantStdin)
			}
			if tc.wantSystemArg != "" {
				found := ""
				for i, a := range args {
					if a == "--append-system-prompt" && i+1 < len(args) {
						found = args[i+1]
						break
					}
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
			claude := NewCommandRunner("claude", "command", "true", nil, nil, 10, tc.maxChars, "", zerolog.Nop())
			codex := NewCommandRunner("codex", "command", "true", nil, nil, 10, tc.maxChars, "", zerolog.Nop())

			claudeArgs, claudeStdin := claude.buildDelivery(Request{System: tc.system, User: tc.user})
			_, codexStdin := codex.buildDelivery(Request{System: tc.system, User: tc.user})

			// Reconstruct the logical combined prompt from the claude delivery.
			claudeSystemArg := ""
			for i, a := range claudeArgs {
				if a == "--append-system-prompt" && i+1 < len(claudeArgs) {
					claudeSystemArg = claudeArgs[i+1]
					break
				}
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

// TestPromptMetaReflectsDeliveredPrompt verifies that prompt_hash and
// prompt_chars are computed from the post-truncation logical combined prompt,
// and that Length is measured in runes (not bytes).
func TestPromptMetaReflectsDeliveredPrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		system     string
		user       string
		maxChars   int
		wantLen    int    // expected rune count
		wantPrompt string // the exact string that should be hashed
	}{
		{
			name:       "truncated-combined",
			system:     "abcde",
			user:       "0123456789",
			maxChars:   8,
			wantPrompt: "abcde\n\n0",
			wantLen:    8,
		},
		{
			name:       "fits-within-budget",
			system:     "abc",
			user:       "xyz",
			maxChars:   100,
			wantPrompt: "abc\n\nxyz",
			wantLen:    8,
		},
		{
			name:       "unlimited",
			system:     "abc",
			user:       "xyz",
			maxChars:   0,
			wantPrompt: "abc\n\nxyz",
			wantLen:    8,
		},
		{
			name:       "system-only-truncated",
			system:     "abcdefgh",
			user:       "",
			maxChars:   5,
			wantPrompt: "abcde",
			wantLen:    5,
		},
		{
			name:       "user-only-truncated",
			system:     "",
			user:       "0123456789",
			maxChars:   4,
			wantPrompt: "0123",
			wantLen:    4,
		},
		{
			// Non-ASCII: "é" is 2 bytes but 1 rune. Budget=1 keeps 1 rune,
			// so Length must be 1, not 2.
			name:       "multibyte-unicode-rune-count",
			system:     "",
			user:       "é",
			maxChars:   1,
			wantPrompt: "é",
			wantLen:    1,
		},
		{
			// Budget=0 means no truncation; "é" is 1 rune.
			name:       "multibyte-unicode-unlimited",
			system:     "",
			user:       "é",
			maxChars:   0,
			wantPrompt: "é",
			wantLen:    1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := NewCommandRunner("codex", "noop", "", nil, nil, 10, tc.maxChars, "", zerolog.Nop())
			combined := truncateString(combineSystemUser(tc.system, tc.user), tc.maxChars)
			meta := r.promptMeta(combined)

			if meta.Length != tc.wantLen {
				t.Errorf("Length = %d, want %d (prompt=%q)", meta.Length, tc.wantLen, combined)
			}
			if combined != tc.wantPrompt {
				t.Errorf("combined = %q, want %q", combined, tc.wantPrompt)
			}
			// Verify Length matches actual rune count of the delivered prompt.
			if meta.Length != utf8.RuneCountInString(combined) {
				t.Errorf("Length %d != utf8.RuneCountInString(%q)=%d", meta.Length, combined, utf8.RuneCountInString(combined))
			}
		})
	}
}

func TestParseClaudeSteps(t *testing.T) {
	t.Parallel()

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

	tests := []struct {
		name      string
		input     []byte
		wantNames []string
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
			name:      "result-only output — no tool events",
			input:     []byte(`{"type":"result","subtype":"success","structured_output":{"summary":"ok","artifacts":[]}}` + "\n"),
			wantNames: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			steps := parseClaudeSteps(tc.input)
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
		})
	}
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

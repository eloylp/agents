package ai

import (
	"slices"
	"testing"
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
			})
			hasNumber := slices.ContainsFunc(env, func(e string) bool {
				return len(e) >= len("AI_DAEMON_NUMBER=") && e[:len("AI_DAEMON_NUMBER=")] == "AI_DAEMON_NUMBER="
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

func TestExtractJSON(t *testing.T) {
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

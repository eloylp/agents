package ai

import (
	"slices"
	"strings"
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

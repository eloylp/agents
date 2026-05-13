package backends

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/fleet"
	runtimeexec "github.com/eloylp/agents/internal/runtime"
)

func TestParseGitHubMCPStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		output     string
		wantFound  bool
		wantOnline bool
	}{
		{
			name: "github connected with other unauthenticated servers",
			output: `claude.ai Google Drive: https://drivemcp.googleapis.com/mcp/v1 - ! Needs authentication
claude.ai Gmail: https://gmailmcp.googleapis.com/mcp/v1 - ! Needs authentication
github: https://api.githubcopilot.com/mcp (HTTP) - ✓ Connected`,
			wantFound:  true,
			wantOnline: true,
		},
		{
			name:       "github configured but needs auth",
			output:     `github: https://api.githubcopilot.com/mcp (HTTP) - ! Needs authentication`,
			wantFound:  true,
			wantOnline: false,
		},
		{
			name:       "github disconnected",
			output:     `github: https://api.githubcopilot.com/mcp (HTTP) - not connected`,
			wantFound:  true,
			wantOnline: false,
		},
		{
			name:       "github listed without status",
			output:     `github: https://api.githubcopilot.com/mcp (HTTP)`,
			wantFound:  true,
			wantOnline: false,
		},
		{
			name: "codex table output enabled bearer token",
			output: `Name    Url                                 Bearer Token Env Var  Status   Auth
github  https://api.githubcopilot.com/mcp/  GITHUB_TOKEN          enabled  Bearer token`,
			wantFound:  true,
			wantOnline: true,
		},
		{
			name:       "github not configured",
			output:     `youtrack: https://keldai.youtrack.cloud/mcp (HTTP) - ✓ Connected`,
			wantFound:  false,
			wantOnline: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			found, online := parseGitHubMCPStatus(tc.output)
			if found != tc.wantFound || online != tc.wantOnline {
				t.Fatalf("parseGitHubMCPStatus() = (%v, %v), want (%v, %v)", found, online, tc.wantFound, tc.wantOnline)
			}
		})
	}
}

func TestParseModelsCodexDebugCatalog(t *testing.T) {
	t.Parallel()

	raw := `{"models":[{"slug":"gpt-5.4","display_name":"gpt-5.4"},{"slug":"gpt-5.3-codex","display_name":"gpt-5.3-codex"}]}`
	got := parseModels(raw)
	if len(got) != 2 {
		t.Fatalf("parseModels() length = %d, want 2 (got=%v)", len(got), got)
	}
	if got[0] != "gpt-5.3-codex" || got[1] != "gpt-5.4" {
		t.Fatalf("parseModels() = %v, want [gpt-5.3-codex gpt-5.4]", got)
	}
}

func TestParseModelsClaudeMarkdownTable(t *testing.T) {
	t.Parallel()
	raw := `Current Claude models (as of my knowledge cutoff, August 2025):

| Model | ID |
|---|---|
| Claude Opus 4.7 | ` + "`claude-opus-4-7`" + ` |
| Claude Sonnet 4.6 | ` + "`claude-sonnet-4-6`" + ` |
| Claude Haiku 4.5 | ` + "`claude-haiku-4-5-20251001`" + ` |

You're currently talking to **Claude Sonnet 4.6**.

For building AI applications, default to the latest capable model.`

	got := parseModels(raw)
	want := []string{"claude-haiku-4-5-20251001", "claude-opus-4-7", "claude-sonnet-4-6"}
	if len(got) != len(want) {
		t.Fatalf("parseModels() length = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseModels()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDiagnoseGitHubCLIInRuntimeUsesRunnerContainer(t *testing.T) {
	oldGH, hadGH := os.LookupEnv("GH_TOKEN")
	t.Cleanup(func() {
		if hadGH {
			_ = os.Setenv("GH_TOKEN", oldGH)
		} else {
			_ = os.Unsetenv("GH_TOKEN")
		}
	})
	_ = os.Unsetenv("GH_TOKEN")
	t.Setenv("GITHUB_TOKEN", "test-token")

	var specs []runtimeexec.ContainerSpec
	runner := fakeRuntimeRunner{run: func(spec runtimeexec.ContainerSpec) (int, string, string, error) {
		specs = append(specs, spec)
		script := strings.Join(spec.Command, " ")
		switch {
		case strings.Contains(script, "command -v 'gh'"):
			return 0, "/usr/bin/gh\n", "", nil
		case strings.Contains(script, "'/usr/bin/gh' '--version'"):
			return 0, "gh version 2.71.0\n", "", nil
		case strings.Contains(script, "'/usr/bin/gh' 'auth' 'status' '--hostname' 'github.com'"):
			return 0, "Logged in to github.com\n", "", nil
		default:
			t.Fatalf("unexpected runner command: %v", spec.Command)
			return 1, "", "", nil
		}
	}}

	status := diagnoseGitHubCLIInRuntime(context.Background(), runner, fleet.RuntimeSettings{RunnerImage: "runner:test"})
	if !status.Detected {
		t.Fatal("Detected = false, want true")
	}
	if !status.Authenticated {
		t.Fatal("Authenticated = false, want true")
	}
	if !status.Healthy {
		t.Fatal("Healthy = false, want true")
	}
	if status.Command != "/usr/bin/gh" {
		t.Fatalf("Command = %q, want /usr/bin/gh", status.Command)
	}
	if len(specs) != 3 {
		t.Fatalf("runner calls = %d, want 3", len(specs))
	}
	for _, spec := range specs {
		if spec.Image != "runner:test" {
			t.Fatalf("Image = %q, want runner:test", spec.Image)
		}
		if !envContains(spec.Env, "GH_TOKEN=test-token") {
			t.Fatalf("Env missing GH_TOKEN fallback: %v", spec.Env)
		}
	}
}

type fakeRuntimeRunner struct {
	run func(runtimeexec.ContainerSpec) (code int, stdout string, stderr string, err error)
}

func (f fakeRuntimeRunner) EnsureImage(context.Context, string) error {
	return nil
}

func (f fakeRuntimeRunner) Run(_ context.Context, spec runtimeexec.ContainerSpec) (runtimeexec.ExitStatus, error) {
	code, stdout, stderr, err := f.run(spec)
	if spec.Stdout != nil && stdout != "" {
		_, _ = io.WriteString(spec.Stdout, stdout)
	}
	if spec.Stderr != nil && stderr != "" {
		_, _ = io.WriteString(spec.Stderr, stderr)
	}
	return runtimeexec.ExitStatus{Code: code}, err
}

func envContains(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

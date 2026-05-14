package runtime

import (
	"slices"
	"strings"
	"testing"
)

func TestWrapBackendCommandMaterializesClaudeMCP(t *testing.T) {
	t.Parallel()

	command, env := WrapBackendCommand("claude", []string{"claude", "-p"}, []string{"GITHUB_TOKEN=gh-token"}, BackendSetupOptions{})
	if !slices.Contains(command, "--mcp-config") || !slices.Contains(command, RunnerClaudeMCPPath) {
		t.Fatalf("command = %v, want claude --mcp-config %s", command, RunnerClaudeMCPPath)
	}
	if !envContains(env, "GH_TOKEN=gh-token") {
		t.Fatalf("env = %v, want GH_TOKEN fallback", env)
	}
	if !strings.Contains(command[2], RunnerClaudeMCPPath) || !strings.Contains(command[2], claudeProjectMCPConfig) {
		t.Fatalf("setup script = %q, want claude MCP config materialization", command[2])
	}
}

func TestWrapBackendCommandMaterializesCodexAuthAndSchema(t *testing.T) {
	t.Parallel()

	command, env := WrapBackendCommand("codex", []string{"codex", "exec"}, []string{"OPENAI_API_KEY=sk-test"}, BackendSetupOptions{ResponseSchema: `{"type":"object"}`})
	if !envContains(env, ResponseSchemaEnv+`={"type":"object"}`) {
		t.Fatalf("env = %v, want response schema env", env)
	}
	if !envContains(env, "AGENTS_BACKEND_COMMAND=codex") {
		t.Fatalf("env = %v, want backend command env", env)
	}
	if !strings.Contains(command[2], RunnerResponseSchema) || !strings.Contains(command[2], "$codex_cmd\" login --with-api-key") {
		t.Fatalf("setup script = %q, want codex schema and login materialization", command[2])
	}
}

func envContains(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

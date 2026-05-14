package runtime

import "strings"

const (
	RunnerWorkspaceDir     = "/workspace"
	RunnerTempMount        = "/tmp/agents-run"
	RunnerHomeDir          = RunnerTempMount + "/home"
	RunnerXDGConfigDir     = RunnerTempMount + "/config"
	RunnerXDGCacheDir      = RunnerTempMount + "/cache"
	RunnerXDGDataDir       = RunnerTempMount + "/data"
	RunnerCodexHomeDir     = RunnerTempMount + "/codex"
	RunnerClaudeMCPPath    = RunnerTempMount + "/claude-mcp.json"
	RunnerResponseSchema   = RunnerTempMount + "/response-schema.json"
	ResponseSchemaEnv      = "AGENTS_RESPONSE_SCHEMA"
	claudeProjectMCPConfig = RunnerWorkspaceDir + "/.mcp.json"
)

type BackendSetupOptions struct {
	ResponseSchema string
	BackendCommand string
}

func PrepareBackendEnv(env []string, opts BackendSetupOptions) []string {
	out := setEnvValues(env,
		"HOME", RunnerHomeDir,
		"TMPDIR", RunnerTempMount,
		"XDG_CONFIG_HOME", RunnerXDGConfigDir,
		"XDG_CACHE_HOME", RunnerXDGCacheDir,
		"XDG_DATA_HOME", RunnerXDGDataDir,
		"CODEX_HOME", RunnerCodexHomeDir,
	)
	if opts.ResponseSchema != "" {
		out = setEnvValues(out, ResponseSchemaEnv, opts.ResponseSchema)
	}
	if opts.BackendCommand != "" {
		out = setEnvValues(out, "AGENTS_BACKEND_COMMAND", opts.BackendCommand)
	}
	if getEnv(out, "GITHUB_TOKEN") != "" && getEnv(out, "GH_TOKEN") == "" {
		out = append(out, "GH_TOKEN="+getEnv(out, "GITHUB_TOKEN"))
	}
	return out
}

func BackendSetupScript(backendName string, opts BackendSetupOptions) string {
	script := baseContainerSetup
	switch {
	case strings.HasPrefix(backendName, "claude"):
		script += claudeContainerSetup
	case strings.HasPrefix(backendName, "codex"):
		script += codexContainerSetup
	}
	return script
}

func WrapBackendCommand(backendName string, command []string, env []string, opts BackendSetupOptions) ([]string, []string) {
	if len(command) > 0 && opts.BackendCommand == "" {
		opts.BackendCommand = command[0]
	}
	env = PrepareBackendEnv(env, opts)
	if len(command) == 0 {
		return command, env
	}
	if strings.HasPrefix(backendName, "claude") && getEnv(env, "GITHUB_TOKEN") != "" && !hasArg(command[1:], "--mcp-config") {
		command = append(command[:1], append([]string{"--mcp-config", RunnerClaudeMCPPath}, command[1:]...)...)
	}
	return shellEntrypoint(command, BackendSetupScript(backendName, opts)), env
}

func shellEntrypoint(command []string, setup string) []string {
	out := []string{"/bin/sh", "-lc", setup + `
cmd=$1
shift
exec "$cmd" "$@"
`, "agents-runner"}
	return append(out, command...)
}

const baseContainerSetup = `
set -eu
mkdir -p "$HOME" "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME" "$XDG_DATA_HOME" "$CODEX_HOME" "` + RunnerWorkspaceDir + `"
`

const claudeContainerSetup = `
if [ -n "${GITHUB_TOKEN:-}" ]; then
  printf '{"mcpServers":{"github":{"type":"http","url":"https://api.githubcopilot.com/mcp/","headers":{"Authorization":"Bearer %s"}}}}\n' "$GITHUB_TOKEN" > ` + RunnerClaudeMCPPath + `
  cp ` + RunnerClaudeMCPPath + ` ` + claudeProjectMCPConfig + `
fi
`

const codexContainerSetup = `
if [ -n "${AGENTS_RESPONSE_SCHEMA:-}" ]; then
  printf '%s' "$AGENTS_RESPONSE_SCHEMA" > ` + RunnerResponseSchema + `
fi
if [ -n "${GITHUB_TOKEN:-}" ]; then
  cat > "$CODEX_HOME/config.toml" <<'TOML'
[mcp_servers.github]
url = "https://api.githubcopilot.com/mcp/"
bearer_token_env_var = "GITHUB_TOKEN"
TOML
fi
codex_cmd="${AGENTS_BACKEND_COMMAND:-codex}"
if [ -n "${OPENAI_API_KEY:-}" ]; then
  printf '%s' "$OPENAI_API_KEY" | "$codex_cmd" login --with-api-key >/dev/null || echo "codex login failed" >&2
elif [ -n "${CODEX_ACCESS_TOKEN:-}" ]; then
  printf '%s' "$CODEX_ACCESS_TOKEN" | "$codex_cmd" login --with-access-token >/dev/null || echo "codex login failed" >&2
fi
`

func hasArg(args []string, arg string) bool {
	for _, a := range args {
		if a == arg {
			return true
		}
	}
	return false
}

func setEnvValues(env []string, kvs ...string) []string {
	if len(kvs)%2 != 0 {
		panic("setEnvValues requires key/value pairs")
	}
	keys := make(map[string]string, len(kvs)/2)
	for i := 0; i < len(kvs); i += 2 {
		keys[kvs[i]] = kvs[i+1]
	}
	out := env[:0]
	for _, entry := range env {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if _, replace := keys[key]; replace {
			continue
		}
		out = append(out, entry)
	}
	for i := 0; i < len(kvs); i += 2 {
		out = append(out, kvs[i]+"="+kvs[i+1])
	}
	return out
}

func getEnv(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

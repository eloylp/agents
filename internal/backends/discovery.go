package backends

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eloylp/agents/internal/fleet"
	runtimeexec "github.com/eloylp/agents/internal/runtime"
	"github.com/eloylp/agents/internal/store"
	"github.com/rs/zerolog"
)

const (
	ClaudeName      = "claude"
	CodexName       = "codex"
	ClaudeLocalName = "claude_local"

	diagnosticHome       = runtimeexec.RunnerHomeDir
	diagnosticConfigHome = runtimeexec.RunnerXDGConfigDir
	diagnosticCodexHome  = runtimeexec.RunnerCodexHomeDir
)

var (
	builtinBackendNames    = []string{ClaudeName, CodexName}
	backtickModelIDPattern = regexp.MustCompile("`([a-zA-Z][a-zA-Z0-9._-]+)`")
)

// ToolStatus captures diagnostics for one supporting CLI/tool available to
// agent subprocesses. GitHub CLI is special: it must be both installed and
// authenticated because agents use it as the fallback when GitHub MCP is not
// enough for a complex local checkout/test/push loop.
type ToolStatus struct {
	Name          string `json:"name"`
	Detected      bool   `json:"detected"`
	Command       string `json:"command,omitempty"`
	Version       string `json:"version,omitempty"`
	Authenticated bool   `json:"authenticated,omitempty"`
	Healthy       bool   `json:"healthy"`
	Detail        string `json:"detail,omitempty"`
}

// BackendStatus captures diagnostics for one backend.
type BackendStatus struct {
	Name          string   `json:"name"`
	Detected      bool     `json:"detected"`
	Command       string   `json:"command,omitempty"`
	Version       string   `json:"version,omitempty"`
	Models        []string `json:"models,omitempty"`
	Healthy       bool     `json:"healthy"`
	HealthDetail  string   `json:"health_detail,omitempty"`
	LocalModelURL string   `json:"local_model_url,omitempty"`
}

// Diagnostics is the full backend-and-tool discovery snapshot. Backends cover
// AI CLIs. Tools cover supporting CLIs available inside agent runs, including
// the authenticated GitHub CLI fallback.
type Diagnostics struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Backends    []BackendStatus `json:"backends"`
	Tools       []ToolStatus    `json:"tools"`
	Runtime     *RuntimeStatus  `json:"runtime,omitempty"`
	// GitHubCLI is kept as a compatibility alias for older UI/client code that
	// read the pre-tools diagnostic field directly.
	GitHubCLI *ToolStatus `json:"github_cli,omitempty"`
}

type RuntimeStatus struct {
	RunnerImage     string `json:"runner_image"`
	DockerAvailable bool   `json:"docker_available"`
	ImageAvailable  bool   `json:"image_available"`
	Healthy         bool   `json:"healthy"`
	Detail          string `json:"detail,omitempty"`
}

// AutoDiscoverIfBackendsMissing runs discovery and persists results only
// when the backends table is currently empty.
func AutoDiscoverIfBackendsMissing(ctx context.Context, st *store.Store) (bool, Diagnostics, error) {
	existing, err := st.ReadBackends()
	if err != nil {
		return false, Diagnostics{}, err
	}
	if len(existing) > 0 {
		return false, Diagnostics{}, nil
	}
	diag, err := DiscoverAndPersist(ctx, st)
	if err != nil {
		return false, Diagnostics{}, err
	}
	return true, diag, nil
}

// DiscoverAndPersist runs diagnostics and writes discovered backend
// metadata to the store (upsert semantics).
func DiscoverAndPersist(ctx context.Context, st *store.Store) (Diagnostics, error) {
	existing, err := st.ReadBackends()
	if err != nil {
		return Diagnostics{}, err
	}
	settings, err := st.ReadRuntimeSettings()
	if err != nil {
		return Diagnostics{}, err
	}
	diag := RunDiagnosticsWithRuntime(ctx, existing, settings)
	if err := persistDiagnostics(st, existing, diag); err != nil {
		return Diagnostics{}, err
	}
	return diag, nil
}

func RunDiagnosticsWithRuntime(ctx context.Context, existing map[string]fleet.Backend, settings fleet.RuntimeSettings) Diagnostics {
	diag := Diagnostics{GeneratedAt: time.Now().UTC()}
	fleet.NormalizeRuntimeSettings(&settings)
	status := RuntimeStatus{RunnerImage: settings.RunnerImage}
	dockerRunner, err := runtimeexec.NewDocker(zerolog.Nop())
	if err != nil {
		status.Detail = "docker unavailable: " + err.Error()
		diag.Runtime = &status
		fillRuntimeUnavailable(&diag, existing, status.Detail)
		return diag
	}
	if err := dockerRunner.EnsureImage(ctx, settings.RunnerImage); err != nil {
		runtimeDiag := dockerRunner.Diagnose(ctx, settings.RunnerImage)
		status.DockerAvailable = runtimeDiag.DockerAvailable
		status.ImageAvailable = runtimeDiag.ImageAvailable
		status.Detail = err.Error()
		diag.Runtime = &status
		fillRuntimeUnavailable(&diag, existing, status.Detail)
		return diag
	}
	runtimeDiag := dockerRunner.Diagnose(ctx, settings.RunnerImage)
	status.DockerAvailable = runtimeDiag.DockerAvailable
	status.ImageAvailable = runtimeDiag.ImageAvailable
	status.Healthy = runtimeDiag.DockerAvailable && runtimeDiag.ImageAvailable
	status.Detail = runtimeDiag.Detail
	diag.Runtime = &status
	if status.Healthy {
		fillRuntimeDiagnostics(ctx, &diag, dockerRunner, existing, settings)
	} else {
		fillRuntimeUnavailable(&diag, existing, status.Detail)
	}
	return diag
}

func fillRuntimeDiagnostics(ctx context.Context, diag *Diagnostics, runner runtimeexec.Runner, existing map[string]fleet.Backend, settings fleet.RuntimeSettings) {
	type backendTarget struct {
		name             string
		commandName      string
		preferredCommand string
		localURL         string
	}
	targets := make([]backendTarget, 0, len(existing)+len(builtinBackendNames))
	for _, name := range builtinBackendNames {
		cfg := existing[name]
		targets = append(targets, backendTarget{
			name:             name,
			commandName:      name,
			preferredCommand: cfg.Command,
		})
	}
	for name, cfg := range existing {
		if slices.Contains(builtinBackendNames, name) || strings.TrimSpace(cfg.LocalModelURL) == "" {
			continue
		}
		targets = append(targets, backendTarget{
			name:             name,
			commandName:      ClaudeName,
			preferredCommand: cfg.Command,
			localURL:         cfg.LocalModelURL,
		})
	}

	backendsOut := make([]BackendStatus, 0, len(targets))
	var outMu sync.Mutex
	var toolsOut []ToolStatus

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		toolsOut = diagnoseToolsInRuntime(ctx, runner, settings)
	}()
	for _, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status := diagnoseBackendInRuntime(ctx, runner, settings, target.name, target.commandName, target.preferredCommand, target.localURL)
			outMu.Lock()
			backendsOut = append(backendsOut, status)
			outMu.Unlock()
		}()
	}
	wg.Wait()

	diag.Backends = backendsOut
	slices.SortFunc(diag.Backends, func(a, b BackendStatus) int {
		return strings.Compare(a.Name, b.Name)
	})
	diag.Tools = toolsOut
	for i := range diag.Tools {
		if diag.Tools[i].Name == "github_cli" {
			gh := diag.Tools[i]
			diag.GitHubCLI = &gh
			break
		}
	}
}

func fillRuntimeUnavailable(diag *Diagnostics, existing map[string]fleet.Backend, detail string) {
	diag.Backends = unavailableBackendStatuses(existing, detail)
	diag.Tools = unavailableToolStatuses(detail)
	for i := range diag.Tools {
		if diag.Tools[i].Name == "github_cli" {
			gh := diag.Tools[i]
			diag.GitHubCLI = &gh
			break
		}
	}
}

func diagnoseToolsInRuntime(ctx context.Context, runner runtimeexec.Runner, settings fleet.RuntimeSettings) []ToolStatus {
	tools := []ToolStatus{
		diagnoseGitHubCLIInRuntime(ctx, runner, settings),
		diagnoseVersionedToolInRuntime(ctx, runner, settings, "git", "git", []string{"--version"}),
		diagnoseVersionedToolInRuntime(ctx, runner, settings, "go", "go", []string{"version"}),
		diagnoseVersionedToolInRuntime(ctx, runner, settings, "rustc", "rustc", []string{"--version"}),
		diagnoseVersionedToolInRuntime(ctx, runner, settings, "cargo", "cargo", []string{"--version"}),
		diagnoseVersionedToolInRuntime(ctx, runner, settings, "node", "node", []string{"--version"}),
		diagnoseVersionedToolInRuntime(ctx, runner, settings, "npm", "npm", []string{"--version"}),
		diagnoseVersionedToolInRuntime(ctx, runner, settings, "typescript", "tsc", []string{"--version"}),
	}
	slices.SortFunc(tools, func(a, b ToolStatus) int {
		return strings.Compare(a.Name, b.Name)
	})
	return tools
}

func diagnoseVersionedToolInRuntime(ctx context.Context, runner runtimeexec.Runner, settings fleet.RuntimeSettings, name, command string, args []string) ToolStatus {
	path, detected := resolveCommandInRuntime(ctx, runner, settings, command, "")
	status := ToolStatus{
		Name:     name,
		Detected: detected,
		Command:  path,
	}
	if !detected {
		status.Detail = command + " binary not found in runner image"
		return status
	}
	stdout, stderr, err := runToolCommandInRuntime(ctx, runner, settings, "", path, args, nil)
	status.Version = firstNonEmptyLine(stdout, stderr)
	status.Healthy = err == nil
	if err != nil {
		status.Detail = "runner version check failed: " + firstNonEmptyLine(stderr, stdout, err.Error())
	} else if status.Version != "" {
		status.Detail = "version: " + status.Version
	} else {
		status.Detail = "version: ok"
	}
	return status
}

func diagnoseGitHubCLIInRuntime(ctx context.Context, runner runtimeexec.Runner, settings fleet.RuntimeSettings) ToolStatus {
	path, detected := resolveCommandInRuntime(ctx, runner, settings, "gh", "")
	status := ToolStatus{
		Name:     "github_cli",
		Detected: detected,
		Command:  path,
	}
	if !detected {
		status.Detail = "gh binary not found in runner image"
		return status
	}

	versionOut, versionErr, versionRunErr := runToolCommandInRuntime(ctx, runner, settings, "", path, []string{"--version"}, nil)
	status.Version = firstNonEmptyLine(versionOut, versionErr)

	authOut, authErr, authRunErr := runToolCommandInRuntime(ctx, runner, settings, "", path, []string{"auth", "status", "--hostname", "github.com"}, nil)
	authDetail := firstNonEmptyLine(authOut, authErr)
	if authDetail == "" {
		authDetail = firstNonEmptyLine(authRunErrString(authRunErr))
	}
	status.Authenticated = authRunErr == nil
	status.Healthy = versionRunErr == nil && authRunErr == nil

	details := make([]string, 0, 2)
	if versionRunErr == nil {
		if status.Version != "" {
			details = append(details, "version: "+status.Version)
		} else {
			details = append(details, "version: ok")
		}
	} else {
		details = append(details, "runner version check failed: "+firstNonEmptyLine(versionErr, versionOut, versionRunErr.Error()))
	}
	if status.Authenticated {
		details = append(details, "auth: authenticated")
	} else if authDetail != "" {
		details = append(details, "auth failed: "+authDetail)
	} else {
		details = append(details, "auth failed")
	}
	status.Detail = strings.Join(details, " | ")
	return status
}

func diagnoseBackendInRuntime(ctx context.Context, runner runtimeexec.Runner, settings fleet.RuntimeSettings, backendName, commandName, preferredCommand, localURL string) BackendStatus {
	path, detected := resolveCommandInRuntime(ctx, runner, settings, commandName, preferredCommand)
	status := BackendStatus{
		Name:          backendName,
		Detected:      detected,
		Command:       path,
		LocalModelURL: localURL,
	}
	if !detected {
		status.Healthy = false
		status.HealthDetail = fmt.Sprintf("%s binary not found in runner image", commandName)
		return status
	}

	env := map[string]string{}
	if localURL != "" {
		env["ANTHROPIC_BASE_URL"] = localURL
	}

	versionOut, versionErr, versionRunErr := runToolCommandInRuntime(ctx, runner, settings, backendName, path, []string{"--version"}, env)
	versionOK := versionRunErr == nil
	status.Version = firstNonEmptyLine(versionOut, versionErr)

	models, modelsDetail := discoverModelsInRuntime(ctx, runner, settings, commandName, path, env)
	status.Models = models

	mcpDetail := checkGitHubMCPInRuntime(ctx, runner, settings, backendName, path, env)
	status.Healthy = versionOK

	details := make([]string, 0, 3)
	if versionOK {
		if status.Version != "" {
			details = append(details, "version: "+status.Version)
		} else {
			details = append(details, "version: ok")
		}
	} else {
		details = append(details, "runner version check failed: "+firstNonEmptyLine(versionErr, versionOut))
	}
	if mcpDetail != "" {
		details = append(details, mcpDetail)
	}
	if modelsDetail != "" {
		details = append(details, modelsDetail)
	}
	status.HealthDetail = strings.Join(details, " | ")
	return status
}

func discoverModelsInRuntime(ctx context.Context, runner runtimeexec.Runner, settings fleet.RuntimeSettings, backendName, command string, env map[string]string) ([]string, string) {
	commands := modelCommands(backendName)
	if len(commands) == 0 {
		return nil, "models discovery not supported"
	}
	failures := make([]string, 0, len(commands))
	for _, args := range commands {
		stdout, stderr, err := runToolCommandInRuntime(ctx, runner, settings, backendName, command, args, env)
		if err != nil {
			detail := firstNonEmptyLine(stderr, stdout, err.Error())
			if detail == "" {
				detail = "command failed"
			}
			failures = append(failures, fmt.Sprintf("%s: %s", strings.Join(args, " "), detail))
			continue
		}
		models := parseModels(stdout)
		if len(models) == 0 {
			failures = append(failures, fmt.Sprintf("%s: no models in output", strings.Join(args, " ")))
			continue
		}
		return models, fmt.Sprintf("models: %d discovered", len(models))
	}
	if len(failures) == 0 {
		return nil, "models discovery failed"
	}
	return nil, "models discovery failed: " + failures[0]
}

func checkGitHubMCPInRuntime(ctx context.Context, runner runtimeexec.Runner, settings fleet.RuntimeSettings, backendName string, command string, env map[string]string) string {
	stdout, stderr, err := runToolCommandInRuntime(ctx, runner, settings, backendName, command, []string{"mcp", "list"}, env)
	if err != nil {
		detail := firstNonEmptyLine(stderr, stdout)
		if detail == "" {
			detail = err.Error()
		}
		return "mcp check failed in runner: " + detail
	}
	hasGitHub, connected := parseGitHubMCPStatus(stdout + "\n" + stderr)
	if connected {
		return "github MCP: connected"
	}
	if hasGitHub {
		return "github MCP: found but disconnected"
	}
	return "github MCP: not configured"
}

func authRunErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func persistDiagnostics(st *store.Store, existing map[string]fleet.Backend, diag Diagnostics) error {
	for _, b := range diag.Backends {
		prev, hadPrev := existing[b.Name]
		if !b.Detected && !hadPrev {
			if !slices.Contains(builtinBackendNames, b.Name) {
				// No newly detected command and no prior record to update.
				continue
			}
			// Keep built-in backend rows available even when runner diagnostics
			// cannot prove availability yet. The configured runner image remains
			// the authority on whether those commands are healthy.
			b.Command = b.Name
		}

		next := prev
		if b.Command != "" {
			next.Command = b.Command
		}
		next.Version = b.Version
		next.Models = b.Models
		next.Healthy = b.Healthy
		next.HealthDetail = b.HealthDetail
		next.LocalModelURL = b.LocalModelURL
		fleet.ApplyBackendDefaults(&next)
		fleet.NormalizeBackend(&next)

		if err := st.UpsertBackend(b.Name, next); err != nil {
			return fmt.Errorf("persist backend %s: %w", b.Name, err)
		}
	}
	return nil
}

func modelCommands(name string) [][]string {
	switch {
	case strings.HasPrefix(name, "claude"):
		return [][]string{
			{"models", "list"},
		}
	case strings.HasPrefix(name, "codex"):
		return [][]string{
			{"debug", "models"},
			{"models", "list", "--json"},
			{"models", "list"},
			{"models"},
		}
	default:
		return [][]string{
			{"models", "list"},
		}
	}
}

func parseGitHubMCPStatus(output string) (hasGitHub bool, connected bool) {
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" || !strings.Contains(line, "github") {
			continue
		}
		hasGitHub = true

		disconnected := strings.Contains(line, "not connected") ||
			strings.Contains(line, "disconnected") ||
			strings.Contains(line, "needs authentication")
		if disconnected {
			continue
		}
		if strings.Contains(line, "connected") {
			return true, true
		}
		// codex mcp list (table output) marks healthy entries as:
		// "... github ... status enabled ... auth bearer token"
		if strings.Contains(line, "enabled") && strings.Contains(line, "bearer token") {
			return true, true
		}
	}
	return hasGitHub, false
}

func parseModels(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err == nil {
		return dedupeSorted(list)
	}

	var objects []map[string]any
	if err := json.Unmarshal([]byte(raw), &objects); err == nil {
		names := make([]string, 0, len(objects))
		for _, obj := range objects {
			for _, k := range []string{"id", "name", "model"} {
				if v, ok := obj[k].(string); ok && strings.TrimSpace(v) != "" {
					names = append(names, strings.TrimSpace(v))
					break
				}
			}
		}
		return dedupeSorted(names)
	}

	var wrapped struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapped); err == nil && len(wrapped.Data) > 0 {
		names := make([]string, 0, len(wrapped.Data))
		for _, it := range wrapped.Data {
			if strings.TrimSpace(it.ID) != "" {
				names = append(names, strings.TrimSpace(it.ID))
				continue
			}
			if strings.TrimSpace(it.Name) != "" {
				names = append(names, strings.TrimSpace(it.Name))
			}
		}
		return dedupeSorted(names)
	}

	var codexCatalog struct {
		Models []struct {
			Slug  string `json:"slug"`
			ID    string `json:"id"`
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(raw), &codexCatalog); err == nil && len(codexCatalog.Models) > 0 {
		names := make([]string, 0, len(codexCatalog.Models))
		for _, it := range codexCatalog.Models {
			for _, v := range []string{it.Slug, it.ID, it.Name, it.Model} {
				if strings.TrimSpace(v) == "" {
					continue
				}
				names = append(names, strings.TrimSpace(v))
				break
			}
		}
		return dedupeSorted(names)
	}

	// Extract backtick-enclosed model IDs (e.g. `claude-opus-4-7` from markdown tables).
	var backtickModels []string
	for _, match := range backtickModelIDPattern.FindAllStringSubmatch(raw, -1) {
		backtickModels = append(backtickModels, match[1])
	}
	if len(backtickModels) > 0 {
		return dedupeSorted(backtickModels)
	}

	// Plain-text fallback: one model per line, skip headers and decorators.
	var names []string
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "|") || strings.HasPrefix(line, "*") || strings.HasPrefix(line, "#") {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "model") || strings.HasPrefix(lower, "available") || strings.HasPrefix(lower, "current") || strings.HasPrefix(lower, "you") || strings.HasPrefix(lower, "for ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		names = append(names, fields[0])
	}
	return dedupeSorted(names)
}

func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		seen[s] = struct{}{}
	}
	return slices.Sorted(maps.Keys(seen))
}

func resolveCommandInRuntime(ctx context.Context, runner runtimeexec.Runner, settings fleet.RuntimeSettings, defaultName, preferred string) (string, bool) {
	preferred = strings.TrimSpace(preferred)
	if preferred != "" {
		if path, ok := commandPathInRuntime(ctx, runner, settings, preferred); ok {
			return path, true
		}
	}
	return commandPathInRuntime(ctx, runner, settings, defaultName)
}

func commandPathInRuntime(ctx context.Context, runner runtimeexec.Runner, settings fleet.RuntimeSettings, command string) (string, bool) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", false
	}
	stdout, _, err := runShellInRuntime(ctx, runner, settings, "", "", "command -v "+shellQuote(command), nil)
	if err != nil {
		return "", false
	}
	path := firstNonEmptyLine(stdout)
	if path == "" {
		return "", false
	}
	return path, true
}

func firstNonEmptyLine(values ...string) string {
	for _, v := range values {
		for line := range strings.SplitSeq(strings.TrimSpace(v), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				return line
			}
		}
	}
	return ""
}

func runToolCommandInRuntime(ctx context.Context, runner runtimeexec.Runner, settings fleet.RuntimeSettings, backendName string, command string, args []string, env map[string]string) (string, string, error) {
	return runShellInRuntime(ctx, runner, settings, backendName, command, "exec "+shellJoin(command, args), env)
}

func runShellInRuntime(ctx context.Context, runner runtimeexec.Runner, settings fleet.RuntimeSettings, backendName string, backendCommand string, script string, env map[string]string) (string, string, error) {
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	policy := settings.Constraints
	policy.TimeoutSeconds = 0
	diagnosticEnv := diagnosticEnv(env)
	setupOptions := runtimeexec.BackendSetupOptions{BackendCommand: backendCommand}
	diagnosticEnv = runtimeexec.PrepareBackendEnv(diagnosticEnv, setupOptions)
	setup := runtimeexec.BackendSetupScript(backendName, setupOptions)
	status, err := runner.Run(runCtx, runtimeexec.ContainerSpec{
		Image:      settings.RunnerImage,
		Command:    []string{"/bin/sh", "-lc", setup + script},
		WorkingDir: runtimeexec.RunnerWorkspaceDir,
		Env:        diagnosticEnv,
		Stdout:     &stdout,
		Stderr:     &stderr,
		Policy:     policy,
	})
	if err != nil {
		return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
	}
	if status.Code != 0 {
		return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), fmt.Errorf("runner command exited with status %d", status.Code)
	}
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), nil
}

func diagnosticEnv(override map[string]string) []string {
	keys := []string{
		"GITHUB_TOKEN",
		"GH_TOKEN",
		"GH_HOST",
		"GITHUB_API_URL",
		"CLAUDE_CODE_OAUTH_TOKEN",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_MODEL",
		"CODEX_AUTH_JSON_BASE64",
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OPENAI_MODEL",
		"SSH_AUTH_SOCK",
	}
	out := []string{
		"HOME=" + diagnosticHome,
		"TMPDIR=" + runtimeexec.RunnerTempMount,
		"XDG_CONFIG_HOME=" + diagnosticConfigHome,
		"CODEX_HOME=" + diagnosticCodexHome,
	}
	seen := map[string]struct{}{
		"HOME":            {},
		"TMPDIR":          {},
		"XDG_CONFIG_HOME": {},
		"CODEX_HOME":      {},
	}
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		if value, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+value)
			seen[key] = struct{}{}
		}
	}
	if _, hasGH := seen["GH_TOKEN"]; !hasGH {
		if value, ok := os.LookupEnv("GITHUB_TOKEN"); ok {
			out = append(out, "GH_TOKEN="+value)
			seen["GH_TOKEN"] = struct{}{}
		}
	}
	for key, value := range override {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			for i := range out {
				if strings.HasPrefix(out[i], key+"=") {
					out[i] = key + "=" + value
					break
				}
			}
			continue
		}
		out = append(out, key+"="+value)
		seen[key] = struct{}{}
	}
	return out
}

func unavailableBackendStatuses(existing map[string]fleet.Backend, detail string) []BackendStatus {
	seen := make(map[string]struct{}, len(existing)+len(builtinBackendNames))
	for _, name := range builtinBackendNames {
		seen[name] = struct{}{}
	}
	for name := range existing {
		seen[name] = struct{}{}
	}
	names := slices.Sorted(maps.Keys(seen))
	out := make([]BackendStatus, 0, len(names))
	for _, name := range names {
		cfg := existing[name]
		command := cfg.Command
		if command == "" && slices.Contains(builtinBackendNames, name) {
			command = name
		}
		out = append(out, BackendStatus{
			Name:          name,
			Command:       command,
			LocalModelURL: cfg.LocalModelURL,
			HealthDetail:  "runner diagnostics unavailable: " + detail,
		})
	}
	return out
}

func unavailableToolStatuses(detail string) []ToolStatus {
	tools := []string{"cargo", "git", "github_cli", "go", "node", "npm", "rustc", "typescript"}
	out := make([]ToolStatus, 0, len(tools))
	for _, name := range tools {
		command := name
		if name == "github_cli" {
			command = "gh"
		} else if name == "typescript" {
			command = "tsc"
		}
		out = append(out, ToolStatus{
			Name:    name,
			Command: command,
			Detail:  "runner diagnostics unavailable: " + detail,
		})
	}
	return out
}

func shellJoin(command string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(command))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

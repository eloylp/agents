package backends

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

const (
	ClaudeName      = "claude"
	CodexName       = "codex"
	ClaudeLocalName = "claude_local"
)

var builtinBackendNames = []string{ClaudeName, CodexName}

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
	// GitHubCLI is kept as a compatibility alias for older UI/client code that
	// read the pre-tools diagnostic field directly.
	GitHubCLI *ToolStatus `json:"github_cli,omitempty"`
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
	diag := RunDiagnostics(ctx, existing)
	if err := persistDiagnostics(st, existing, diag); err != nil {
		return Diagnostics{}, err
	}
	return diag, nil
}

// RunDiagnostics executes live discovery checks without mutating the store.
func RunDiagnostics(ctx context.Context, existing map[string]fleet.Backend) Diagnostics {
	diag := Diagnostics{GeneratedAt: time.Now().UTC()}
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
			localURL:         "",
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
		toolsOut = diagnoseTools(ctx)
	}()
	for _, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status := diagnoseBackend(ctx, target.name, target.commandName, target.preferredCommand, target.localURL)
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
	return diag
}

func diagnoseTools(ctx context.Context) []ToolStatus {
	tools := []ToolStatus{
		diagnoseGitHubCLI(ctx),
		diagnoseVersionedTool(ctx, "git", "git", []string{"--version"}),
		diagnoseVersionedTool(ctx, "go", "go", []string{"version"}),
		diagnoseVersionedTool(ctx, "rustc", "rustc", []string{"--version"}),
		diagnoseVersionedTool(ctx, "cargo", "cargo", []string{"--version"}),
		diagnoseVersionedTool(ctx, "node", "node", []string{"--version"}),
		diagnoseVersionedTool(ctx, "npm", "npm", []string{"--version"}),
		diagnoseVersionedTool(ctx, "typescript", "tsc", []string{"--version"}),
	}
	slices.SortFunc(tools, func(a, b ToolStatus) int {
		return strings.Compare(a.Name, b.Name)
	})
	return tools
}

func diagnoseVersionedTool(ctx context.Context, name, command string, args []string) ToolStatus {
	path, detected := resolveCommand(command, "")
	status := ToolStatus{
		Name:     name,
		Detected: detected,
		Command:  path,
	}
	if !detected {
		status.Detail = command + " binary not found on PATH"
		return status
	}
	stdout, stderr, err := runToolCommand(ctx, path, args, nil)
	status.Version = firstNonEmptyLine(stdout, stderr)
	status.Healthy = err == nil
	if err != nil {
		status.Detail = "version check failed: " + firstNonEmptyLine(stderr, stdout, err.Error())
	} else if status.Version != "" {
		status.Detail = "version: " + status.Version
	} else {
		status.Detail = "version: ok"
	}
	return status
}

func diagnoseGitHubCLI(ctx context.Context) ToolStatus {
	path, detected := resolveCommand("gh", "")
	status := ToolStatus{
		Name:     "github_cli",
		Detected: detected,
		Command:  path,
	}
	if !detected {
		status.Detail = "gh binary not found on PATH"
		return status
	}

	versionOut, versionErr, versionRunErr := runToolCommand(ctx, path, []string{"--version"}, nil)
	status.Version = firstNonEmptyLine(versionOut, versionErr)

	authOut, authErr, authRunErr := runToolCommand(ctx, path, []string{"auth", "status", "--hostname", "github.com"}, nil)
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
		details = append(details, "version check failed: "+firstNonEmptyLine(versionErr, versionOut, versionRunErr.Error()))
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
			// No newly detected command and no prior record to update.
			continue
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

func diagnoseBackend(ctx context.Context, backendName, commandName, preferredCommand, localURL string) BackendStatus {
	path, detected := resolveCommand(commandName, preferredCommand)
	status := BackendStatus{
		Name:          backendName,
		Detected:      detected,
		Command:       path,
		LocalModelURL: localURL,
	}
	if !detected {
		status.Healthy = false
		status.HealthDetail = fmt.Sprintf("%s binary not found on PATH", commandName)
		return status
	}

	env := map[string]string{}
	if localURL != "" {
		env["ANTHROPIC_BASE_URL"] = localURL
	}

	versionOut, versionErr, versionRunErr := runToolCommand(ctx, path, []string{"--version"}, env)
	versionOK := versionRunErr == nil
	status.Version = firstNonEmptyLine(versionOut, versionErr)

	models, modelsDetail := discoverModels(ctx, commandName, path, env)
	status.Models = models

	mcpDetail := checkGitHubMCP(ctx, path, env)
	status.Healthy = versionOK

	details := make([]string, 0, 3)
	if versionOK {
		if status.Version != "" {
			details = append(details, "version: "+status.Version)
		} else {
			details = append(details, "version: ok")
		}
	} else {
		details = append(details, "version check failed: "+firstNonEmptyLine(versionErr, versionOut))
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

func discoverModels(ctx context.Context, backendName, command string, env map[string]string) ([]string, string) {
	commands := modelCommands(backendName)
	if len(commands) == 0 {
		return nil, "models discovery not supported"
	}
	failures := make([]string, 0, len(commands))
	for _, args := range commands {
		stdout, stderr, err := runToolCommand(ctx, command, args, env)
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

func checkGitHubMCP(ctx context.Context, command string, env map[string]string) string {
	stdout, stderr, err := runToolCommand(ctx, command, []string{"mcp", "list"}, env)
	if err != nil {
		detail := firstNonEmptyLine(stderr, stdout)
		if detail == "" {
			detail = err.Error()
		}
		return "mcp check failed: " + detail
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

func parseGitHubMCPStatus(output string) (hasGitHub bool, connected bool) {
	for _, line := range strings.Split(strings.ToLower(output), "\n") {
		line = strings.TrimSpace(line)
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
	for _, match := range regexp.MustCompile("`([a-zA-Z][a-zA-Z0-9._-]+)`").FindAllStringSubmatch(raw, -1) {
		backtickModels = append(backtickModels, match[1])
	}
	if len(backtickModels) > 0 {
		return dedupeSorted(backtickModels)
	}

	// Plain-text fallback: one model per line, skip headers and decorators.
	lines := strings.Split(raw, "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
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
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

func resolveCommand(defaultName, preferred string) (string, bool) {
	preferred = strings.TrimSpace(preferred)
	if preferred != "" {
		if p, err := exec.LookPath(preferred); err == nil {
			return p, true
		}
	}
	p, err := exec.LookPath(defaultName)
	if err != nil {
		return "", false
	}
	return p, true
}

func firstNonEmptyLine(values ...string) string {
	for _, v := range values {
		for _, line := range strings.Split(strings.TrimSpace(v), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				return line
			}
		}
	}
	return ""
}

func runToolCommand(ctx context.Context, command string, args []string, env map[string]string) (string, string, error) {
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, command, args...)
	cmd.Env = mergeEnv(os.Environ(), env)
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

func mergeEnv(base []string, override map[string]string) []string {
	if len(override) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(override))
	out = append(out, base...)
	for k, v := range override {
		out = append(out, k+"="+v)
	}
	return out
}

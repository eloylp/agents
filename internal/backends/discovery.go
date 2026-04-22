package backends

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/store"
)

const (
	ClaudeName      = "claude"
	CodexName       = "codex"
	ClaudeLocalName = "claude_local"
)

var builtinBackendNames = []string{ClaudeName, CodexName}

// GitHubStatus captures diagnostics for the GitHub CLI dependency.
type GitHubStatus struct {
	Detected      bool   `json:"detected"`
	Command       string `json:"command,omitempty"`
	Authenticated bool   `json:"authenticated"`
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

// Diagnostics is the full tool-discovery and health snapshot.
type Diagnostics struct {
	GeneratedAt time.Time       `json:"generated_at"`
	GitHub      GitHubStatus    `json:"github_cli"`
	Backends    []BackendStatus `json:"backends"`
}

// AutoDiscoverIfBackendsMissing runs discovery and persists results only when
// the backends table is currently empty.
func AutoDiscoverIfBackendsMissing(ctx context.Context, db *sql.DB) (bool, Diagnostics, error) {
	existing, err := store.ReadBackends(db)
	if err != nil {
		return false, Diagnostics{}, err
	}
	if len(existing) > 0 {
		return false, RunDiagnostics(ctx, existing), nil
	}
	diag, err := DiscoverAndPersist(ctx, db)
	if err != nil {
		return false, Diagnostics{}, err
	}
	return true, diag, nil
}

// DiscoverAndPersist runs diagnostics and writes discovered backend metadata to
// the store (upsert semantics).
func DiscoverAndPersist(ctx context.Context, db *sql.DB) (Diagnostics, error) {
	existing, err := store.ReadBackends(db)
	if err != nil {
		return Diagnostics{}, err
	}
	diag := RunDiagnostics(ctx, existing)
	if err := persistDiagnostics(db, existing, diag); err != nil {
		return Diagnostics{}, err
	}
	return diag, nil
}

// RunDiagnostics executes live discovery checks without mutating the store.
func RunDiagnostics(ctx context.Context, existing map[string]config.AIBackendConfig) Diagnostics {
	diag := Diagnostics{
		GeneratedAt: time.Now().UTC(),
		GitHub:      diagnoseGitHubCLI(ctx),
	}
	for _, name := range builtinBackendNames {
		cfg := existing[name]
		diag.Backends = append(diag.Backends, diagnoseBackend(ctx, name, name, cfg.Command, ""))
	}
	if local, ok := existing[ClaudeLocalName]; ok && strings.TrimSpace(local.LocalModelURL) != "" {
		diag.Backends = append(diag.Backends, diagnoseBackend(ctx, ClaudeLocalName, ClaudeName, local.Command, local.LocalModelURL))
	}
	sort.Slice(diag.Backends, func(i, j int) bool {
		return diag.Backends[i].Name < diag.Backends[j].Name
	})
	return diag
}

func persistDiagnostics(db *sql.DB, existing map[string]config.AIBackendConfig, diag Diagnostics) error {
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
		config.ApplyBackendDefaults(&next)
		config.NormalizeBackendConfig(&next)

		if err := store.UpsertBackend(db, b.Name, next); err != nil {
			return fmt.Errorf("persist backend %s: %w", b.Name, err)
		}
	}
	return nil
}

func diagnoseGitHubCLI(ctx context.Context) GitHubStatus {
	path, err := exec.LookPath("gh")
	if err != nil {
		return GitHubStatus{
			Detected: false,
			Healthy:  false,
			Detail:   "gh binary not found on PATH",
		}
	}
	stdout, stderr, runErr := runToolCommand(ctx, path, []string{"auth", "status"}, nil)
	detail := firstNonEmptyLine(stdout, stderr)
	if detail == "" {
		detail = "authentication check completed"
	}
	return GitHubStatus{
		Detected:      true,
		Command:       path,
		Authenticated: runErr == nil,
		Healthy:       runErr == nil,
		Detail:        detail,
	}
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

	models, modelsDetail := discoverModels(ctx, backendName, path, env)
	status.Models = models

	mcpOK, mcpDetail := checkGitHubMCP(ctx, path, env)
	status.Healthy = versionOK && mcpOK

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
	for _, args := range commands {
		stdout, _, err := runToolCommand(ctx, command, args, env)
		if err != nil {
			continue
		}
		models := parseModels(stdout)
		if len(models) == 0 {
			return nil, "models: none reported"
		}
		return models, fmt.Sprintf("models: %d discovered", len(models))
	}
	return nil, "models discovery failed"
}

func modelCommands(name string) [][]string {
	switch {
	case strings.HasPrefix(name, "claude"):
		return [][]string{
			{"models", "list", "--output", "json"},
			{"models", "list"},
		}
	case strings.HasPrefix(name, "codex"):
		return [][]string{
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

func checkGitHubMCP(ctx context.Context, command string, env map[string]string) (bool, string) {
	stdout, stderr, err := runToolCommand(ctx, command, []string{"mcp", "list"}, env)
	if err != nil {
		detail := firstNonEmptyLine(stderr, stdout)
		if detail == "" {
			detail = err.Error()
		}
		return false, "mcp check failed: " + detail
	}
	out := strings.ToLower(stdout + "\n" + stderr)
	hasGitHub := strings.Contains(out, "github")
	disconnected := strings.Contains(out, "disconnected") || strings.Contains(out, "not connected")
	if hasGitHub && !disconnected {
		return true, "github MCP: connected"
	}
	if hasGitHub {
		return false, "github MCP: found but disconnected"
	}
	return false, "github MCP: not configured"
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

	lines := strings.Split(raw, "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "model") || strings.HasPrefix(lower, "available models") {
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
	sort.Strings(out)
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
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)), "", nil
	}

	var stderr string
	if ee, ok := err.(*exec.ExitError); ok {
		stderr = strings.TrimSpace(string(ee.Stderr))
	}
	return strings.TrimSpace(string(out)), stderr, err
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

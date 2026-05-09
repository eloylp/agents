package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

// validAIBackendNames is the canonical list of supported AI backend names.
// Adding a new backend only requires updating this slice.
var validAIBackendNames = []string{"claude", "codex", "claude_local"}

// validEventKinds is the set of event kind strings accepted in the events:
// binding field. It must be kept in sync with the kinds emitted by the webhook
// server handlers.
var validEventKinds = map[string]struct{}{
	"issues.labeled":                      {},
	"issues.opened":                       {},
	"issues.edited":                       {},
	"issues.reopened":                     {},
	"issues.closed":                       {},
	"pull_request.labeled":                {},
	"pull_request.opened":                 {},
	"pull_request.synchronize":            {},
	"pull_request.ready_for_review":       {},
	"pull_request.closed":                 {},
	"issue_comment.created":               {},
	"pull_request_review.submitted":       {},
	"pull_request_review_comment.created": {},
	"push":                                {},
}

// validEventKindsSorted is a precomputed, sorted list of validEventKinds keys
// for use in human-readable error messages.
var validEventKindsSorted = slices.Sorted(maps.Keys(validEventKinds))

var validLogLevels = []string{"trace", "debug", "info", "warn", "error", "fatal", "panic", "disabled"}
var validLogFormats = []string{"json", "text"}

// validate runs the full Config-level invariant tree. Called from Load and
// FinishLoad after defaults / normalization / secret resolution. Each
// validate* method below covers one section of the config; ordering matters
// only for cross-references (agents must validate after backends and skills).
func (c *Config) validate() error {
	if c.Daemon.HTTP.DeliveryTTLSeconds < 0 {
		return fmt.Errorf("config: http delivery_ttl_seconds must be positive, got %d", c.Daemon.HTTP.DeliveryTTLSeconds)
	}
	if err := c.validateLogConfig(); err != nil {
		return err
	}
	if err := c.validateBackends(); err != nil {
		return err
	}
	if err := c.validateSkills(); err != nil {
		return err
	}
	if err := c.validateAgents(); err != nil {
		return err
	}
	if err := c.validateProxy(); err != nil {
		return err
	}
	if err := c.validateDispatchConfig(); err != nil {
		return err
	}
	return c.validateRepos()
}

func (c *Config) validateProxy() error {
	p := c.Daemon.Proxy
	if !p.Enabled {
		return nil
	}
	if p.Upstream.URL == "" {
		return errors.New("config: proxy.upstream.url is required when proxy.enabled is true")
	}
	if p.Upstream.Model == "" {
		return errors.New("config: proxy.upstream.model is required when proxy.enabled is true")
	}
	if !strings.HasPrefix(p.Path, "/") {
		return fmt.Errorf("config: proxy.path must start with '/', got %q", p.Path)
	}
	if p.Upstream.TimeoutSeconds <= 0 {
		return fmt.Errorf("config: proxy.upstream.timeout_seconds must be positive, got %d", p.Upstream.TimeoutSeconds)
	}
	// When an api_key_env is configured, the variable must resolve at startup so
	// that a missing or mis-spelled env var fails fast rather than producing
	// silent 401/403 errors against a protected upstream at request time.
	if p.Upstream.APIKeyEnv != "" && p.Upstream.APIKey == "" {
		return fmt.Errorf("config: proxy.upstream.api_key_env %q is set but the environment variable is empty or unset", p.Upstream.APIKeyEnv)
	}
	return nil
}

func (c *Config) validateDispatchConfig() error {
	d := c.Daemon.Processor.Dispatch
	if d.MaxDepth <= 0 {
		return fmt.Errorf("config: dispatch max_depth must be positive, got %d", d.MaxDepth)
	}
	if d.MaxFanout <= 0 {
		return fmt.Errorf("config: dispatch max_fanout must be positive, got %d", d.MaxFanout)
	}
	if d.DedupWindowSeconds <= 0 {
		return fmt.Errorf("config: dispatch dedup_window_seconds must be positive, got %d", d.DedupWindowSeconds)
	}
	return nil
}

func (c *Config) validateLogConfig() error {
	if c.Daemon.Log.Level != "" {
		if !slices.Contains(validLogLevels, c.Daemon.Log.Level) {
			return fmt.Errorf("config: invalid log level %q (supported: %s)", c.Daemon.Log.Level, strings.Join(validLogLevels, ", "))
		}
	}
	if c.Daemon.Log.Format != "" && !slices.Contains(validLogFormats, c.Daemon.Log.Format) {
		return fmt.Errorf("config: unknown log format %q (supported: %s)", c.Daemon.Log.Format, strings.Join(validLogFormats, ", "))
	}
	return nil
}

func (c *Config) validateBackends() error {
	for name, backend := range c.Daemon.AIBackends {
		if !isSupportedBackend(name, backend) {
			return fmt.Errorf("config: unsupported ai backend %q (supported: %s, or any custom name with local_model_url set)", name, strings.Join(validAIBackendNames, ", "))
		}
		if backend.Command == "" {
			return fmt.Errorf("config: ai backend %q: command is required", name)
		}
	}
	return nil
}

func (c *Config) validateSkills() error {
	for name, skill := range c.Skills {
		if strings.TrimSpace(name) == "" {
			return errors.New("config: skill name is required")
		}
		if skill.Prompt == "" {
			return fmt.Errorf("config: skill %q: prompt is empty", name)
		}
	}
	return nil
}

func (c *Config) validateAgents() error {
	seen := make(map[string]struct{}, len(c.Agents))
	for _, a := range c.Agents {
		if a.Name == "" {
			return errors.New("config: agent name is required")
		}
		key := workspaceNameKey(a.WorkspaceID, a.Name)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("config: duplicate agent name %q in workspace %q", a.Name, fleet.NormalizeWorkspaceID(a.WorkspaceID))
		}
		seen[key] = struct{}{}

		if a.Backend == "" {
			return fmt.Errorf("config: agent %q: backend is required", a.Name)
		}
		if _, ok := c.Daemon.AIBackends[a.Backend]; !ok {
			return fmt.Errorf("config: agent %q: unknown backend %q", a.Name, a.Backend)
		}
		for _, s := range a.Skills {
			if _, ok := c.Skills[s]; !ok {
				return fmt.Errorf("config: agent %q: unknown skill %q", a.Name, s)
			}
		}
		if a.Prompt == "" {
			return fmt.Errorf("config: agent %q: prompt is empty", a.Name)
		}
		if a.Description == "" {
			return fmt.Errorf("config: agent %q: description is required (used for agent identification and inter-agent conversations)", a.Name)
		}
	}
	// Validate can_dispatch references after all agents are seen.
	return validateDispatchWiring(c.Agents)
}

// validateDispatchWiring checks cross-agent dispatch references:
//   - can_dispatch entries must reference real dispatchable agents
//   - can_dispatch must not include the agent itself
func validateDispatchWiring(agents []fleet.Agent) error {
	agentByName := make(map[string]fleet.Agent, len(agents))
	for _, a := range agents {
		agentByName[workspaceNameKey(a.WorkspaceID, a.Name)] = a
	}
	for _, a := range agents {
		for _, t := range a.CanDispatch {
			target, ok := agentByName[workspaceNameKey(a.WorkspaceID, t)]
			if !ok {
				return fmt.Errorf("config: agent %q: can_dispatch references unknown agent %q", a.Name, t)
			}
			if t == a.Name {
				return fmt.Errorf("config: agent %q: can_dispatch must not include itself", a.Name)
			}
			if !target.AllowDispatch {
				return fmt.Errorf("config: agent %q: can_dispatch target %q has allow_dispatch disabled", a.Name, t)
			}
		}
	}
	return nil
}

// validateRepos checks per-repo invariants: name presence, name uniqueness,
// binding agent references, trigger exclusivity, and event-kind allow-list.
// It does NOT enforce an "at least one enabled repo" minimum, disabling all
// repos is a legitimate user action (fleet maintenance, evaluating prompts on
// a different repo) and the daemon runs cleanly with zero enabled repos:
// webhook events for disabled repos route through workflow_engine which logs
// "no bindings matched event, skipping". See issue #302.
func (c *Config) validateRepos() error {
	seen := make(map[string]struct{}, len(c.Repos))
	for _, r := range c.Repos {
		if err := fleet.ValidateRepoName(r.Name); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		key := workspaceNameKey(r.WorkspaceID, r.Name)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("config: duplicate repo %q in workspace %q", r.Name, fleet.NormalizeWorkspaceID(r.WorkspaceID))
		}
		seen[key] = struct{}{}
		for i, b := range r.Use {
			if b.Agent == "" {
				return fmt.Errorf("config: repo %q: binding #%d has no agent", r.Name, i)
			}
			if _, ok := c.AgentByNameInWorkspace(b.Agent, r.WorkspaceID); !ok {
				return fmt.Errorf("config: repo %q: binding references unknown agent %q", r.Name, b.Agent)
			}
			if !b.IsCron() && !b.IsLabel() && !b.IsEvent() {
				return fmt.Errorf("config: repo %q: binding for agent %q has no trigger (set cron, labels, or events)", r.Name, b.Agent)
			}
			if b.TriggerCount() > 1 {
				return fmt.Errorf("config: repo %q: binding for agent %q mixes multiple trigger types (labels, events, cron); each binding must use exactly one trigger", r.Name, b.Agent)
			}
			for _, kind := range b.Events {
				if _, ok := validEventKinds[kind]; !ok {
					return fmt.Errorf("config: repo %q: binding for agent %q has unknown event kind %q (supported: %s)",
						r.Name, b.Agent, kind, strings.Join(validEventKindsSorted, ", "))
				}
			}
		}
	}
	return nil
}

func isSupportedBackend(name string, backend fleet.Backend) bool {
	return slices.Contains(validAIBackendNames, name) || strings.TrimSpace(backend.LocalModelURL) != ""
}

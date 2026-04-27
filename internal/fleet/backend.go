// Package fleet defines the domain entities the daemon orchestrates: agents,
// skills, backends, repos, and the bindings that wire agents to repos. Other
// packages (config, store, mcp, webhook, workflow, autonomous) consume these
// types; this package depends on none of them.
//
// The types here are loaded from YAML by the config package, persisted by the
// store package, and surfaced over REST and MCP. They are pure data plus
// per-entity invariants and helpers (e.g. Binding.IsCron, Agent.IsAllowMemory).
// Cross-entity validation (agents that reference unknown skills, dispatch
// wiring that points at agents that do not exist) lives in the config
// package because it needs the full Config snapshot.
package fleet

// Backend is one AI CLI runner the daemon can dispatch agents through.
// "claude" and "codex" are built-in names; any other key in the daemon's
// AIBackends map is a custom backend, typically routed through a local LLM
// via LocalModelURL.
type Backend struct {
	Command          string   `yaml:"command"`
	Version          string   `yaml:"version"`
	Models           []string `yaml:"models"`
	Healthy          bool     `yaml:"healthy"`
	HealthDetail     string   `yaml:"health_detail"`
	LocalModelURL    string   `yaml:"local_model_url"`
	TimeoutSeconds   int      `yaml:"timeout_seconds"`
	MaxPromptChars   int      `yaml:"max_prompt_chars"`
	RedactionSaltEnv string   `yaml:"redaction_salt_env"`
}

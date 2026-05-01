package fleet

// Agent is a named capability: a backend, a set of skills, and a prompt.
// Agents are pure definitions — they don't run on their own. Repos bind them
// to triggers.
type Agent struct {
	Name    string   `yaml:"name"`
	Backend string   `yaml:"backend"`
	Model   string   `yaml:"model"`
	Skills  []string `yaml:"skills"`
	Prompt  string   `yaml:"prompt"`
	// AllowPRs controls whether the agent is permitted to open pull requests.
	// Defaults to false; the scheduler prepends a hard no-PR instruction when
	// false so the gate is code-level rather than relying on prompt wording.
	AllowPRs bool `yaml:"allow_prs"`

	// Description is a short human-readable summary of what this agent does.
	// Required when the agent appears in any other agent's can_dispatch list.
	Description string `yaml:"description"`

	// AllowDispatch opts this agent in as a dispatch target. Default false.
	// Other agents may only dispatch to this agent when this is true.
	AllowDispatch bool `yaml:"allow_dispatch"`

	// CanDispatch is the whitelist of agent names this agent is allowed to
	// dispatch. Validated: entries must reference real agents in the same
	// config and must not include the agent itself.
	CanDispatch []string `yaml:"can_dispatch"`

	// AllowMemory controls whether the daemon loads existing memory into the
	// prompt and persists the agent's returned memory after the run. Stored as
	// a pointer so absence can be distinguished from explicit false: when nil
	// (the YAML/JSON "absent" case), IsAllowMemory reports true so existing
	// agents authored before this field existed retain their previous behaviour.
	// Set to a non-nil false to disable memory load+persist for this agent
	// across every trigger surface (cron, webhook events,
	// dispatch, POST /run, MCP trigger_agent).
	AllowMemory *bool `yaml:"allow_memory,omitempty"`
}

// IsAllowMemory reports whether this agent's memory should be loaded into the
// prompt and persisted from the response. Default (nil pointer) is true, so
// agents authored before this field existed retain their previous behaviour.
func (a Agent) IsAllowMemory() bool {
	return a.AllowMemory == nil || *a.AllowMemory
}

package daemon_test

// Test-side mirrors of the wire shapes produced by the CRUD endpoints. The
// concrete types live in internal/daemon/fleet, but importing them would
// require either making them exported or moving every test that decodes a
// response into that package. These minimal local types let the webhook
// integration tests assert on response bodies without that churn.
type storeAgentJSON struct {
	ID            string   `json:"id,omitempty"`
	WorkspaceID   string   `json:"workspace_id,omitempty"`
	Name          string   `json:"name"`
	Backend       string   `json:"backend"`
	Model         string   `json:"model,omitempty"`
	Skills        []string `json:"skills"`
	Prompt        string   `json:"prompt"`
	PromptRef     string   `json:"prompt_ref,omitempty"`
	AllowPRs      bool     `json:"allow_prs"`
	AllowDispatch bool     `json:"allow_dispatch"`
	CanDispatch   []string `json:"can_dispatch"`
	Description   string   `json:"description"`
	AllowMemory   *bool    `json:"allow_memory,omitempty"`
}

type storeSkillJSON struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

type storePromptJSON struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Content     string `json:"content"`
}

type storeWorkspaceJSON struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type workspaceGuardrailJSON struct {
	WorkspaceID   string `json:"workspace_id"`
	GuardrailName string `json:"guardrail_name"`
	Position      int    `json:"position"`
	Enabled       bool   `json:"enabled"`
}

type storeRepoJSON struct {
	WorkspaceID string             `json:"workspace_id,omitempty"`
	Name        string             `json:"name"`
	Enabled     bool               `json:"enabled"`
	Bindings    []storeBindingJSON `json:"bindings"`
}

type storeBindingJSON struct {
	ID      int64    `json:"id,omitempty"`
	Agent   string   `json:"agent"`
	Labels  []string `json:"labels,omitempty"`
	Events  []string `json:"events,omitempty"`
	Cron    string   `json:"cron,omitempty"`
	Enabled *bool    `json:"enabled,omitempty"`
}

type storeBackendJSON struct {
	Name           string   `json:"name"`
	Command        string   `json:"command"`
	Version        string   `json:"version,omitempty"`
	Models         []string `json:"models,omitempty"`
	Healthy        bool     `json:"healthy"`
	HealthDetail   string   `json:"health_detail,omitempty"`
	LocalModelURL  string   `json:"local_model_url,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	MaxPromptChars int      `json:"max_prompt_chars"`
}

// view*JSON types mirror the fleet snapshot wire shape produced by GET
// /agents (HandleAgentsView in internal/daemon/fleet) so the webhook tests
// that already exercised the router can decode the response without
// importing the fleet package's unexported types.
type viewScheduleJSON struct {
	LastRun    *string `json:"last_run,omitempty"`
	NextRun    string  `json:"next_run"`
	LastStatus string  `json:"last_status,omitempty"`
}

type viewBindingJSON struct {
	Repo        string            `json:"repo"`
	RepoEnabled bool              `json:"repo_enabled"`
	Labels      []string          `json:"labels,omitempty"`
	Events      []string          `json:"events,omitempty"`
	Cron        string            `json:"cron,omitempty"`
	Enabled     bool              `json:"enabled"`
	Schedule    *viewScheduleJSON `json:"schedule,omitempty"`
}

type viewAgentJSON struct {
	WorkspaceID   string            `json:"workspace_id"`
	Name          string            `json:"name"`
	Backend       string            `json:"backend"`
	Model         string            `json:"model,omitempty"`
	Skills        []string          `json:"skills,omitempty"`
	Description   string            `json:"description,omitempty"`
	AllowDispatch bool              `json:"allow_dispatch"`
	CanDispatch   []string          `json:"can_dispatch,omitempty"`
	AllowPRs      bool              `json:"allow_prs"`
	AllowMemory   bool              `json:"allow_memory"`
	CurrentStatus string            `json:"current_status"`
	Bindings      []viewBindingJSON `json:"bindings,omitempty"`
}

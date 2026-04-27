package webhook

// Test-side mirrors of the wire shapes produced by the CRUD endpoints. The
// concrete types live in internal/server/fleet, but importing them would
// require either making them exported or moving every test that decodes a
// response into that package. These minimal local types let the webhook
// integration tests assert on response bodies without that churn.
type storeAgentJSON struct {
	Name          string   `json:"name"`
	Backend       string   `json:"backend"`
	Model         string   `json:"model,omitempty"`
	Skills        []string `json:"skills"`
	Prompt        string   `json:"prompt"`
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

type storeBackendJSON struct {
	Name             string   `json:"name"`
	Command          string   `json:"command"`
	Version          string   `json:"version,omitempty"`
	Models           []string `json:"models,omitempty"`
	Healthy          bool     `json:"healthy"`
	HealthDetail     string   `json:"health_detail,omitempty"`
	LocalModelURL    string   `json:"local_model_url,omitempty"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	MaxPromptChars   int      `json:"max_prompt_chars"`
	RedactionSaltEnv string   `json:"redaction_salt_env"`
}

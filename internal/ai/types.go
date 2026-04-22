package ai

import "context"

type Runner interface {
	Run(ctx context.Context, req Request) (Response, error)
}

// RenderedPrompt is the result of RenderAgentPrompt: stable system-level
// content (skills + agent body) separated from per-run user content (runtime
// context). Keeping them split lets backends that support a native system
// channel (e.g. Claude's --append-system-prompt) benefit from prompt caching
// without any behavioural change on backends that do not (codex: concatenated).
type RenderedPrompt struct {
	System string // stable across runs: skills + agent prompt body
	User   string // per-run: runtime context, memory, event payload
}

type Request struct {
	Workflow string
	Repo     string
	Number   int
	Model    string // optional per-agent model override
	System   string // stable system-level content (from RenderedPrompt.System)
	User     string // per-run user content (from RenderedPrompt.User)
}

type Artifact struct {
	Type     string  `json:"type"`
	PartKey  string  `json:"part_key"`
	GitHubID string  `json:"github_id"`
	URL      *string `json:"url"`
}

// DispatchRequest is a request from an agent to dispatch another agent on the
// same repo. The daemon validates these requests against whitelist and safety
// limits before enqueuing a synthetic "agent.dispatch" event.
type DispatchRequest struct {
	Agent  string `json:"agent"`
	Number int    `json:"number,omitempty"`
	Reason string `json:"reason"`
}

// TraceStep records one tool call in the agent's tool loop.
// It is populated by the runner from stream-json output and is never part of
// the agent's JSON schema — agents do not return steps themselves.
type TraceStep struct {
	ToolName      string `json:"tool_name"`
	InputSummary  string `json:"input_summary"`
	OutputSummary string `json:"output_summary"`
	DurationMs    int64  `json:"duration_ms"`
}

type Response struct {
	Artifacts []Artifact        `json:"artifacts"`
	Summary   string            `json:"summary"`
	Dispatch  []DispatchRequest `json:"dispatch,omitempty"`
	// Memory is the agent's full updated memory state returned at the end of
	// each run. The daemon writes this value back to the memory store (SQLite or
	// filesystem) after the run completes. An empty string clears the memory.
	Memory string `json:"memory"`
	// Steps holds the tool-loop transcript extracted from stream-json CLI
	// output. It is populated by the runner and not part of the agent schema.
	Steps []TraceStep `json:"-"`
}

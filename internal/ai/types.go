package ai

import "context"

type Runner interface {
	Run(ctx context.Context, req Request) (Response, error)
}

type Request struct {
	Workflow string
	Repo     string
	Number   int
	Prompt   string
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

type Response struct {
	Artifacts []Artifact        `json:"artifacts"`
	Summary   string            `json:"summary"`
	Dispatch  []DispatchRequest `json:"dispatch,omitempty"`
}

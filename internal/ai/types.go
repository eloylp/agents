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

type Response struct {
	Artifacts []Artifact `json:"artifacts"`
	Summary   string     `json:"summary"`
}

package workflow

import "github.com/eloylp/agents/internal/config"

type Label struct {
	Name string `json:"name"`
}

type Issue struct {
	Number      int       `json:"number"`
	PullRequest *struct{} `json:"pull_request,omitempty"`
}

type PullRequest struct {
	Number int  `json:"number"`
	Draft  bool `json:"draft"`
}

type IssueRequest struct {
	Repo  config.RepoConfig
	Issue Issue
	Label string
}

type PRRequest struct {
	Repo  config.RepoConfig
	PR    PullRequest
	Label string
}

package workflow

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

// RepoRef is a minimal repository descriptor used by workflow types.
// It contains only the fields needed by the workflow package, avoiding
// a direct dependency on config.RepoConfig.
type RepoRef struct {
	FullName string
	Enabled  bool
}

type IssueRequest struct {
	Repo  RepoRef
	Issue Issue
	Label string
}

type PRRequest struct {
	Repo  RepoRef
	PR    PullRequest
	Label string
}

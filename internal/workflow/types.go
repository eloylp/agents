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

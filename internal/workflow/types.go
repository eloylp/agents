package workflow

// RepoRef is a minimal repository descriptor used by workflow types.
// It contains only the fields needed by the workflow package, avoiding
// a direct dependency on config.RepoConfig.
type RepoRef struct {
	FullName string
	Enabled  bool
}

// Event is the single in-process event type for all workflow triggers.
// Kind follows the convention "{github_event_type}.{action}" for most events
// (e.g. "issues.labeled", "pull_request.opened", "issue_comment.created") or
// just "{github_event_type}" for events without an action (e.g. "push").
// Draft PR filtering and AI-label filtering happen at the webhook boundary
// before the event is enqueued.
type Event struct {
	Repo    RepoRef
	Kind    string            // e.g. "issues.labeled", "pull_request.opened", "push"
	Number  int               // issue/PR number; 0 for push and other non-item events
	Actor   string            // GitHub login that triggered the event
	Payload map[string]any    // kind-specific fields (label name, comment body, head SHA, ...)
}

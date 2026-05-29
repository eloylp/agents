package workflow

import (
	"encoding/json"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

// RunAttribution is the compact, public-safe identity for one agent run.
// It is persisted privately and rendered into prompts as the hidden metadata
// contract agents should copy into GitHub PR bodies and comments they author.
type RunAttribution struct {
	WorkspaceID         string   `json:"workspace"`
	RepoOwner           string   `json:"repo_owner"`
	RepoName            string   `json:"repo_name"`
	IssueOrPRNumber     int      `json:"issue_or_pr_number,omitempty"`
	EventID             string   `json:"event_id,omitempty"`
	EventQueueID        int64    `json:"event_queue_id,omitempty"`
	SpanID              string   `json:"span_id"`
	AgentID             string   `json:"agent_id,omitempty"`
	AgentName           string   `json:"agent_name"`
	BackendID           string   `json:"backend_id,omitempty"`
	BackendName         string   `json:"backend_name"`
	PromptVersionID     string   `json:"prompt_version_id,omitempty"`
	PromptRef           string   `json:"prompt_ref,omitempty"`
	SkillVersionIDs     []string `json:"skill_version_ids,omitempty"`
	GuardrailVersionIDs []string `json:"guardrail_version_ids,omitempty"`
	HeadSHA             string   `json:"head_sha,omitempty"`
	Branch              string   `json:"branch,omitempty"`
}

func (a RunAttribution) HiddenComment() string {
	b, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	return "<!-- agents-run: " + string(b) + " -->"
}

func buildRunAttribution(ev Event, agent fleet.Agent, backend, spanID string) RunAttribution {
	owner, name, _ := strings.Cut(ev.Repo.FullName, "/")
	if name == "" {
		name = owner
		owner = ""
	}
	return RunAttribution{
		WorkspaceID:     eventWorkspaceID(ev),
		RepoOwner:       owner,
		RepoName:        name,
		IssueOrPRNumber: ev.Number,
		EventID:         ev.ID,
		EventQueueID:    ev.QueueID,
		SpanID:          spanID,
		AgentID:         agent.ID,
		AgentName:       agent.Name,
		BackendID:       backend,
		BackendName:     backend,
		PromptRef:       promptRef(agent),
		HeadSHA:         payloadStringValue(ev.Payload, "head_sha"),
		Branch:          payloadBranch(ev.Payload),
	}
}

func promptRef(agent fleet.Agent) string {
	if strings.TrimSpace(agent.PromptID) != "" {
		return strings.TrimSpace(agent.PromptID)
	}
	return strings.TrimSpace(agent.PromptRef)
}

func payloadStringValue(payload map[string]any, key string) string {
	if len(payload) == 0 {
		return ""
	}
	v, _ := payload[key].(string)
	return strings.TrimSpace(v)
}

func payloadBranch(payload map[string]any) string {
	if branch := payloadStringValue(payload, "branch"); branch != "" {
		return branch
	}
	ref := payloadStringValue(payload, "ref")
	if branch, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return branch
	}
	return ref
}

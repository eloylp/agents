package workflow

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

const (
	attributionVersion       = 1
	defaultAttributionInstID = "default"
)

// RunAttribution is the private identity for one agent run. It is persisted as
// a snapshot, while HiddenComment renders a smaller public lookup token for
// GitHub PR bodies and comments agents author.
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
	return a.HiddenCommentWithSignature("", "")
}

func (a RunAttribution) HiddenCommentWithSignature(secret, instanceID string) string {
	b, err := json.Marshal(a.PublicMetadata(secret, instanceID))
	if err != nil {
		return ""
	}
	return "<!-- agents-run: " + string(b) + " -->"
}

func (a RunAttribution) CommitAttributionTrailer(secret, instanceID string) string {
	b, err := json.Marshal(a.PublicMetadata(secret, instanceID))
	if err != nil {
		return ""
	}
	return "Agents-Attribution: " + base64.RawURLEncoding.EncodeToString(b)
}

func (a RunAttribution) PublicMetadata(secret, instanceID string) PublicRunAttribution {
	repo := strings.Trim(strings.TrimSpace(a.RepoOwner)+"/"+strings.TrimSpace(a.RepoName), "/")
	meta := PublicRunAttribution{
		WorkspaceID:     a.WorkspaceID,
		Repo:            repo,
		IssueOrPRNumber: a.IssueOrPRNumber,
		SpanID:          a.SpanID,
		AgentID:         a.AgentID,
		AgentName:       a.AgentName,
	}
	if strings.TrimSpace(secret) == "" {
		return meta
	}
	meta.Version = attributionVersion
	meta.InstanceID = NormalizeAttributionInstanceID(instanceID)
	meta.Signature = signPublicRunAttribution(meta, secret)
	return meta
}

func DecodeCommitAttributionTrailer(value string) (PublicRunAttribution, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return PublicRunAttribution{}, errors.New("empty attribution trailer")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return PublicRunAttribution{}, fmt.Errorf("decode attribution trailer: %w", err)
	}
	return DecodePublicRunAttribution(raw)
}

func DecodePublicRunAttribution(raw []byte) (PublicRunAttribution, error) {
	var meta PublicRunAttribution
	if err := json.Unmarshal(raw, &meta); err != nil {
		return PublicRunAttribution{}, err
	}
	meta.Normalize()
	if meta.SpanID == "" {
		return PublicRunAttribution{}, errors.New("missing span_id")
	}
	return meta, nil
}

func VerifyPublicRunAttribution(meta PublicRunAttribution, secret, instanceID string) error {
	meta.Normalize()
	if meta.SpanID == "" {
		return errors.New("missing span_id")
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil
	}
	if meta.Signature == "" {
		return errors.New("unsigned attribution metadata")
	}
	if meta.Version != attributionVersion {
		return fmt.Errorf("unsupported attribution version %d", meta.Version)
	}
	wantInstance := NormalizeAttributionInstanceID(instanceID)
	if meta.InstanceID != wantInstance {
		return fmt.Errorf("attribution instance %q does not match local instance %q", meta.InstanceID, wantInstance)
	}
	want := signPublicRunAttribution(meta, secret)
	if !hmac.Equal([]byte(meta.Signature), []byte(want)) {
		return errors.New("invalid attribution signature")
	}
	return nil
}

func NormalizeAttributionInstanceID(instanceID string) string {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return defaultAttributionInstID
	}
	return instanceID
}

type PublicRunAttribution struct {
	Version         int    `json:"v,omitempty"`
	InstanceID      string `json:"instance_id,omitempty"`
	WorkspaceID     string `json:"workspace"`
	Repo            string `json:"repo,omitempty"`
	IssueOrPRNumber int    `json:"number,omitempty"`
	SpanID          string `json:"span_id"`
	AgentID         string `json:"agent_id,omitempty"`
	AgentName       string `json:"agent_name,omitempty"`
	Signature       string `json:"sig,omitempty"`
}

func (m *PublicRunAttribution) Normalize() {
	m.InstanceID = NormalizeAttributionInstanceID(m.InstanceID)
	m.WorkspaceID = strings.TrimSpace(m.WorkspaceID)
	m.Repo = strings.ToLower(strings.TrimSpace(m.Repo))
	m.SpanID = strings.TrimSpace(m.SpanID)
	m.AgentID = strings.TrimSpace(m.AgentID)
	m.AgentName = strings.TrimSpace(m.AgentName)
	m.Signature = strings.TrimSpace(m.Signature)
}

func signPublicRunAttribution(meta PublicRunAttribution, secret string) string {
	meta.Normalize()
	meta.Signature = ""
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	mac.Write([]byte(canonicalPublicRunAttribution(meta)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func canonicalPublicRunAttribution(meta PublicRunAttribution) string {
	return strings.Join([]string{
		strconv.Itoa(meta.Version),
		meta.InstanceID,
		meta.WorkspaceID,
		meta.Repo,
		strconv.Itoa(meta.IssueOrPRNumber),
		meta.SpanID,
		meta.AgentID,
		meta.AgentName,
	}, "\n")
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

package observe

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/workflow"
)

type AttributionSnapshot struct {
	WorkspaceID         string    `json:"workspace"`
	RepoOwner           string    `json:"repo_owner"`
	RepoName            string    `json:"repo_name"`
	IssueOrPRNumber     int       `json:"issue_or_pr_number"`
	EventID             string    `json:"event_id"`
	EventQueueID        int64     `json:"event_queue_id"`
	SpanID              string    `json:"span_id"`
	AgentID             string    `json:"agent_id,omitempty"`
	AgentName           string    `json:"agent_name"`
	BackendID           string    `json:"backend_id,omitempty"`
	BackendName         string    `json:"backend_name"`
	PromptVersionID     string    `json:"prompt_version_id,omitempty"`
	PromptRef           string    `json:"prompt_ref,omitempty"`
	SkillVersionIDs     []string  `json:"skill_version_ids,omitempty"`
	GuardrailVersionIDs []string  `json:"guardrail_version_ids,omitempty"`
	HeadSHA             string    `json:"head_sha,omitempty"`
	Branch              string    `json:"branch,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

type AttributionQuery struct {
	Body            string
	CommitMessage   string
	WorkspaceID     string
	RepoOwner       string
	RepoName        string
	IssueOrPRNumber int
	HeadSHA         string
	At              time.Time
	Window          time.Duration
}

type AttributionResolution struct {
	Confidence string               `json:"confidence"`
	Mode       string               `json:"mode,omitempty"`
	Snapshot   *AttributionSnapshot `json:"snapshot,omitempty"`
	Diagnostic string               `json:"diagnostic,omitempty"`
}

const (
	AttributionExact      = "exact"
	AttributionInferred   = "inferred"
	AttributionUnresolved = "unresolved"
)

var agentsRunCommentRE = regexp.MustCompile(`(?s)<!--\s*agents-run:\s*(\{.*?\})\s*-->`)

func (s *Store) ResolveRunAttribution(q AttributionQuery) AttributionResolution {
	workspaceID := fleet.NormalizeWorkspaceID(q.WorkspaceID)
	if spanID := spanIDFromBody(q.Body); spanID != "" {
		if snap, ok := s.runAttributionBySpan(workspaceID, spanID); ok {
			return AttributionResolution{Confidence: AttributionExact, Mode: "exact", Snapshot: &snap}
		}
		return AttributionResolution{Confidence: AttributionUnresolved, Mode: "exact", Diagnostic: fmt.Sprintf("metadata names unknown span %q", spanID)}
	}
	if spanID := spanIDFromCommitTrailers(q.CommitMessage); spanID != "" {
		if snap, ok := s.runAttributionBySpan(workspaceID, spanID); ok {
			return AttributionResolution{Confidence: AttributionExact, Mode: "exact", Snapshot: &snap}
		}
		return AttributionResolution{Confidence: AttributionUnresolved, Mode: "exact", Diagnostic: fmt.Sprintf("commit trailer names unknown span %q", spanID)}
	}
	matches := s.inferRunAttributions(workspaceID, q)
	switch len(matches) {
	case 0:
		return AttributionResolution{Confidence: AttributionUnresolved, Mode: "inferred", Diagnostic: "no matching run attribution snapshot"}
	case 1:
		return AttributionResolution{Confidence: AttributionInferred, Mode: "inferred", Snapshot: &matches[0]}
	default:
		return AttributionResolution{Confidence: AttributionUnresolved, Mode: "inferred", Diagnostic: fmt.Sprintf("ambiguous run attribution: %d matches", len(matches))}
	}
}

func spanIDFromBody(body string) string {
	m := agentsRunCommentRE.FindStringSubmatch(body)
	if len(m) != 2 {
		return ""
	}
	var meta struct {
		SpanID string `json:"span_id"`
	}
	if err := json.Unmarshal([]byte(m[1]), &meta); err != nil {
		return ""
	}
	return strings.TrimSpace(meta.SpanID)
}

func spanIDFromCommitTrailers(message string) string {
	var spanID string
	for _, line := range strings.Split(message, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "Agents-Run") {
			spanID = strings.TrimSpace(v)
		}
	}
	return spanID
}

func (s *Store) recordRunAttribution(in workflow.SpanInput, createdAt time.Time) {
	if s.db == nil || in.SpanID == "" {
		return
	}
	a := attributionSnapshotFromSpan(in, createdAt)
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO run_attributions (
			span_id, workspace_id, repo_owner, repo_name, issue_or_pr_number,
			event_id, event_queue_id, agent_id, agent_name, backend_id, backend_name,
			prompt_version_id, prompt_ref, skill_version_ids, guardrail_version_ids,
			head_sha, branch, created_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.SpanID, a.WorkspaceID, a.RepoOwner, a.RepoName, a.IssueOrPRNumber,
		a.EventID, a.EventQueueID, a.AgentID, a.AgentName, a.BackendID, a.BackendName,
		nullString(a.PromptVersionID), a.PromptRef, strings.Join(a.SkillVersionIDs, ","), strings.Join(a.GuardrailVersionIDs, ","),
		a.HeadSHA, a.Branch, a.CreatedAt,
	)
	if err != nil {
		log.Printf("observe: persist run attribution %s: %v", in.SpanID, err)
	}
}

func attributionSnapshotFromSpan(in workflow.SpanInput, createdAt time.Time) AttributionSnapshot {
	a := in.Attribution
	owner, name := repoParts(in.Repo)
	out := AttributionSnapshot{
		WorkspaceID:         fleet.NormalizeWorkspaceID(firstNonEmpty(a.WorkspaceID, in.WorkspaceID)),
		RepoOwner:           firstNonEmpty(a.RepoOwner, owner),
		RepoName:            firstNonEmpty(a.RepoName, name),
		IssueOrPRNumber:     firstNonZero(a.IssueOrPRNumber, in.Number),
		EventID:             a.EventID,
		EventQueueID:        firstNonZero64(a.EventQueueID, in.EventQueueID),
		SpanID:              firstNonEmpty(a.SpanID, in.SpanID),
		AgentID:             a.AgentID,
		AgentName:           firstNonEmpty(a.AgentName, in.Agent),
		BackendID:           firstNonEmpty(a.BackendID, in.Backend),
		BackendName:         firstNonEmpty(a.BackendName, in.Backend),
		PromptVersionID:     a.PromptVersionID,
		PromptRef:           a.PromptRef,
		SkillVersionIDs:     a.SkillVersionIDs,
		GuardrailVersionIDs: a.GuardrailVersionIDs,
		HeadSHA:             a.HeadSHA,
		Branch:              a.Branch,
		CreatedAt:           createdAt,
	}
	if out.CreatedAt.IsZero() {
		out.CreatedAt = time.Now()
	}
	return out
}

func (s *Store) runAttributionBySpan(workspaceID, spanID string) (AttributionSnapshot, bool) {
	if s.db == nil {
		return AttributionSnapshot{}, false
	}
	row := s.db.QueryRow(`SELECT workspace_id, repo_owner, repo_name, issue_or_pr_number, event_id, event_queue_id, span_id, agent_id, agent_name, backend_id, backend_name, COALESCE(prompt_version_id, ''), prompt_ref, skill_version_ids, guardrail_version_ids, head_sha, branch, created_at FROM run_attributions WHERE workspace_id=? AND span_id=?`, workspaceID, spanID)
	snap, err := scanAttribution(row)
	return snap, err == nil
}

func (s *Store) inferRunAttributions(workspaceID string, q AttributionQuery) []AttributionSnapshot {
	if s.db == nil || q.RepoOwner == "" || q.RepoName == "" {
		return nil
	}
	if q.At.IsZero() {
		return nil
	}
	window := q.Window
	if window <= 0 {
		window = 24 * time.Hour
	}
	from := q.At.Add(-window)
	to := q.At.Add(window)
	rows, err := s.db.Query(
		`SELECT workspace_id, repo_owner, repo_name, issue_or_pr_number, event_id, event_queue_id, span_id, agent_id, agent_name, backend_id, backend_name, COALESCE(prompt_version_id, ''), prompt_ref, skill_version_ids, guardrail_version_ids, head_sha, branch, created_at
		FROM run_attributions
		WHERE workspace_id=? AND repo_owner=? AND repo_name=? AND issue_or_pr_number=?
		  AND (?='' OR head_sha=?)
		  AND created_at BETWEEN ? AND ?
		ORDER BY created_at DESC`,
		workspaceID, q.RepoOwner, q.RepoName, q.IssueOrPRNumber,
		strings.TrimSpace(q.HeadSHA), strings.TrimSpace(q.HeadSHA),
		from, to,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AttributionSnapshot
	for rows.Next() {
		snap, err := scanAttribution(rows)
		if err == nil {
			out = append(out, snap)
		}
	}
	return out
}

type attributionScanner interface {
	Scan(dest ...any) error
}

func scanAttribution(row attributionScanner) (AttributionSnapshot, error) {
	var snap AttributionSnapshot
	var skills, guardrails string
	err := row.Scan(
		&snap.WorkspaceID, &snap.RepoOwner, &snap.RepoName, &snap.IssueOrPRNumber,
		&snap.EventID, &snap.EventQueueID, &snap.SpanID, &snap.AgentID, &snap.AgentName,
		&snap.BackendID, &snap.BackendName, &snap.PromptVersionID, &snap.PromptRef,
		&skills, &guardrails, &snap.HeadSHA, &snap.Branch, &snap.CreatedAt,
	)
	if err != nil {
		return AttributionSnapshot{}, err
	}
	snap.SkillVersionIDs = splitList(skills)
	snap.GuardrailVersionIDs = splitList(guardrails)
	return snap, nil
}

func repoParts(repo string) (string, string) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return "", strings.TrimSpace(repo)
	}
	return strings.TrimSpace(owner), strings.TrimSpace(name)
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: strings.TrimSpace(s) != ""}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

func firstNonZero64(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

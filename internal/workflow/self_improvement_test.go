package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

type selfImprovementJSONRunner struct {
	req ai.JSONRequest
	raw json.RawMessage
	err error
}

func (r *selfImprovementJSONRunner) Run(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, nil
}

func (r *selfImprovementJSONRunner) RunJSON(_ context.Context, req ai.JSONRequest) (json.RawMessage, error) {
	r.req = req
	return r.raw, r.err
}

func TestAnalyzeSelfImprovementFeedbackRunsStructuredAssistant(t *testing.T) {
	t.Parallel()

	st := newTempStore(t)
	if err := st.UpsertBackend("codex", fleet.Backend{Command: "codex"}); err != nil {
		t.Fatalf("upsert backend: %v", err)
	}
	feedback, err := st.UpsertSelfImprovementFeedback(store.SelfImprovementFeedbackInput{
		WorkspaceID:           "default",
		RepoOwner:             "owner",
		RepoName:              "repo",
		SourceType:            "issue_comment",
		GitHubCommentID:       123,
		SourceURL:             "https://github.com/owner/repo/issues/7#issuecomment-123",
		AuthorLogin:           "maintainer",
		AuthorAuthorized:      true,
		IssueNumber:           7,
		RawBody:               "Keep files under 800 lines /agents improve",
		Tag:                   store.FeedbackTag,
		LinkedPromptVersionID: "promptver_self_improvement_analyst_v1",
		LinkConfidence:        "exact",
		LinkDiagnostics:       "matched run attribution metadata",
		Status:                store.FeedbackStatusNew,
	})
	if err != nil {
		t.Fatalf("upsert feedback: %v", err)
	}
	runner := &selfImprovementJSONRunner{raw: json.RawMessage(`{
		"type":"patch_prompt",
		"status":"recommended",
		"confidence":"medium",
		"risk":"low",
		"finding":"The feedback asks for a file-size guidance improvement.",
		"normalized_lesson":"Prefer files under 800 lines when practical.",
		"rationale":"Feedback event 1 provides direct evidence.",
		"evidence_feedback_ids":[1],
		"evidence_source_urls":["https://github.com/owner/repo/issues/7#issuecomment-123"],
		"attribution_confidence":"exact",
		"target_asset_type":"prompt",
		"target_asset_id":"prompt_self-improvement-analyst",
		"target_base_version_id":"promptver_self_improvement_analyst_v1",
		"proposed_patch":"",
		"proposed_new_body":"",
		"suggested_rollout_scope":"workspace",
		"no_auto_apply_confirmed":true
	}`)}
	e := NewEngine(st, config.ProcessorConfig{}, nil, zerolog.Nop())
	e.WithRunnerBuilder(func(_ string, _ string, _ fleet.Backend) ai.Runner { return runner })

	rec, err := e.AnalyzeSelfImprovementFeedback(context.Background(), feedback)
	if err != nil {
		t.Fatalf("AnalyzeSelfImprovementFeedback: %v", err)
	}
	if rec.Status != store.RecommendationStatusRecommended || rec.AnalyzerPromptVersionID == "" {
		t.Fatalf("recommendation = %+v, want recommended with analyzer prompt version", rec)
	}
	gotFeedback, err := st.GetSelfImprovementFeedback(feedback.ID)
	if err != nil {
		t.Fatalf("get feedback: %v", err)
	}
	if gotFeedback.Status != store.FeedbackStatusAnalyzed {
		t.Fatalf("feedback status = %q, want analyzed", gotFeedback.Status)
	}
	if !strings.Contains(runner.req.System, "Hard contract") || runner.req.Schema == "" {
		t.Fatalf("assistant request missing hard contract/schema: %+v", runner.req)
	}
	var input selfImprovementInput
	if err := json.Unmarshal([]byte(runner.req.User), &input); err != nil {
		t.Fatalf("assistant input json: %v", err)
	}
	if input.FeedbackEventID != feedback.ID || input.RawFeedbackBody != feedback.RawBody || len(input.RelevantCurrentCatalogVersions) == 0 {
		t.Fatalf("assistant input = %+v, want feedback context and current catalog versions", input)
	}
}

func TestAnalyzeSelfImprovementFeedbackMarksFailedWhenAssistantFails(t *testing.T) {
	t.Parallel()

	st := newTempStore(t)
	if err := st.UpsertBackend("codex", fleet.Backend{Command: "codex"}); err != nil {
		t.Fatalf("upsert backend: %v", err)
	}
	feedback, err := st.UpsertSelfImprovementFeedback(store.SelfImprovementFeedbackInput{
		WorkspaceID:      "default",
		RepoOwner:        "owner",
		RepoName:         "repo",
		SourceType:       "issue_comment",
		GitHubCommentID:  456,
		SourceURL:        "https://github.com/owner/repo/issues/8#issuecomment-456",
		AuthorLogin:      "maintainer",
		AuthorAuthorized: true,
		IssueNumber:      8,
		RawBody:          "Needs more guardrail clarity /agents improve",
		Tag:              store.FeedbackTag,
		LinkConfidence:   "unresolved",
		Status:           store.FeedbackStatusNew,
	})
	if err != nil {
		t.Fatalf("upsert feedback: %v", err)
	}
	runner := &selfImprovementJSONRunner{err: context.Canceled}
	e := NewEngine(st, config.ProcessorConfig{}, nil, zerolog.Nop())
	e.WithRunnerBuilder(func(_ string, _ string, _ fleet.Backend) ai.Runner { return runner })

	if _, err := e.AnalyzeSelfImprovementFeedback(context.Background(), feedback); err == nil {
		t.Fatal("AnalyzeSelfImprovementFeedback succeeded, want error")
	}
	gotFeedback, err := st.GetSelfImprovementFeedback(feedback.ID)
	if err != nil {
		t.Fatalf("get feedback: %v", err)
	}
	if gotFeedback.Status != store.FeedbackStatusFailed {
		t.Fatalf("feedback status = %q, want failed", gotFeedback.Status)
	}
}

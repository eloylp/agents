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

type selfImprovementStreamPublisher struct {
	begin []BeginRunInput
	end   []string
}

func (p *selfImprovementStreamPublisher) BeginRun(in BeginRunInput) {
	p.begin = append(p.begin, in)
}

func (p *selfImprovementStreamPublisher) EndRun(spanID string) {
	p.end = append(p.end, spanID)
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
		"status":"accepted",
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
	traceRec := &traceRecorderStub{}
	streamPub := &selfImprovementStreamPublisher{}
	e.WithTraceRecorder(traceRec)
	e.WithRunStreamPublisher(streamPub)

	rec, err := e.AnalyzeSelfImprovementFeedback(context.Background(), feedback)
	if err != nil {
		t.Fatalf("AnalyzeSelfImprovementFeedback: %v", err)
	}
	if rec.Status != store.RecommendationStatusNeedsUserInput || rec.AnalyzerPromptVersionID == "" {
		t.Fatalf("recommendation = %+v, want machine-owned status with analyzer prompt version", rec)
	}
	if got := rec.StructuredOutput["status"]; got != store.RecommendationStatusNeedsUserInput {
		t.Fatalf("structured status = %v, want clamped machine-owned status", got)
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
	inputJSON := renderedPayloadBlock(t, runner.req.User, "analysis_input_json")
	var input selfImprovementInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		t.Fatalf("assistant input json: %v", err)
	}
	if input.FeedbackEventID != feedback.ID || input.RawFeedbackBody != feedback.RawBody || len(input.RelevantCurrentCatalogVersions) != 1 {
		t.Fatalf("assistant input = %+v, want feedback context and only linked catalog target", input)
	}
	if got := input.RelevantCurrentCatalogVersions[0]; got.VersionID != feedback.LinkedPromptVersionID || got.Content == "" || got.IndexOnly {
		t.Fatalf("catalog context = %+v, want linked prompt with full body", got)
	}
	span, ok := traceRec.last()
	if !ok {
		t.Fatal("self-improvement analyst run did not record a trace span")
	}
	if span.Agent != "self-improvement-analyst" || span.EventKind != selfImprovementEventKind || span.PromptVersionID != feedback.LinkedPromptVersionID {
		t.Fatalf("trace span = %+v, want observable analyst run", span)
	}
	if len(streamPub.begin) != 1 || len(streamPub.end) != 1 || streamPub.begin[0].Agent != "self-improvement-analyst" {
		t.Fatalf("stream lifecycle begin=%+v end=%+v, want analyst run lifecycle", streamPub.begin, streamPub.end)
	}
}

func renderedPayloadBlock(t *testing.T, user, key string) string {
	t.Helper()
	prefix := key + ":\n"
	start := strings.Index(user, prefix)
	if start < 0 {
		t.Fatalf("rendered user prompt missing %s block: %s", key, user)
	}
	block := user[start+len(prefix):]
	var lines []string
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "  ") {
			lines = append(lines, strings.TrimPrefix(line, "  "))
			continue
		}
		break
	}
	return strings.Join(lines, "\n")
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

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
	"github.com/eloylp/agents/internal/selfimprovement"
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
	if err := st.UpsertBackend("codex", fleet.Backend{Command: "codex", Models: []string{"gpt-5.3-codex", "gpt-5.5"}}); err != nil {
		t.Fatalf("upsert backend: %v", err)
	}
	if _, err := st.UpsertPrompt(fleet.Prompt{Name: "coder", Content: "Do coding work."}); err != nil {
		t.Fatalf("upsert prompt: %v", err)
	}
	if err := st.UpsertAgent(fleet.Agent{
		Name:        "coder",
		Backend:     "codex",
		Model:       "gpt-5.5",
		PromptRef:   "coder",
		Description: "writes code",
	}); err != nil {
		t.Fatalf("upsert agent: %v", err)
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
		"changes":[{
			"operation":"update_existing",
			"asset_type":"prompt",
			"asset_id":"prompt_self-improvement-analyst",
			"base_version_id":"promptver_self_improvement_analyst_v1",
			"proposed_body":"Prefer files under 800 lines when practical.",
			"rationale":"Feedback event 1 provides direct evidence."
		}],
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
	if rec.Status != selfimprovement.RecommendationStatusNeedsUserInput || rec.AnalyzerPromptVersionID == "" {
		t.Fatalf("recommendation = %+v, want machine-owned status with analyzer prompt version", rec)
	}
	if got := rec.StructuredOutput["status"]; got != selfimprovement.RecommendationStatusNeedsUserInput {
		t.Fatalf("structured status = %v, want clamped machine-owned status", got)
	}
	if changes, ok := rec.StructuredOutput["changes"].([]any); !ok || len(changes) != 1 {
		t.Fatalf("structured changes = %#v, want one bundle change preserved", rec.StructuredOutput["changes"])
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(runner.req.Schema), &schema); err != nil {
		t.Fatalf("assistant schema json: %v", err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties["changes"] == nil {
		t.Fatalf("assistant schema properties = %#v, want changes property", schema["properties"])
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
	if runner.req.Model != "gpt-5.5" {
		t.Fatalf("assistant model = %q, want inferred configured agent model", runner.req.Model)
	}
	inputJSON := renderedPayloadBlock(t, runner.req.User, "analysis_input_json")
	var input selfImprovementInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		t.Fatalf("assistant input json: %v", err)
	}
	if input.AnalysisMode != selfImprovementAnalysisModeInitial || input.ClarificationPresent {
		t.Fatalf("assistant input mode = %q clarification_present=%v, want initial without clarification", input.AnalysisMode, input.ClarificationPresent)
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
	if span.Agent != selfImprovementInternalAgentName || span.EventKind != selfImprovementEventKind || span.PromptVersionID != rec.AnalyzerPromptVersionID {
		t.Fatalf("trace span = %+v, want observable analyst run", span)
	}
	if len(streamPub.begin) != 1 || len(streamPub.end) != 1 || streamPub.begin[0].Agent != selfImprovementInternalAgentName {
		t.Fatalf("stream lifecycle begin=%+v end=%+v, want analyst run lifecycle", streamPub.begin, streamPub.end)
	}
}

func TestSelfImprovementAnalysisInputMarksClarificationMode(t *testing.T) {
	t.Parallel()

	feedback := store.SelfImprovementFeedback{
		ID:          17,
		WorkspaceID: "default",
		RawBody:     "This guidance is too vague /agents improve",
	}
	prior := &selfimprovement.SelfImprovementRecommendation{ID: "rec_17", FeedbackEventID: feedback.ID}
	clarification := &selfimprovement.SelfImprovementClarification{
		RecommendationID: prior.ID,
		Body:             "Scope it only to refactorer prompts.",
	}

	input := selfImprovementAnalysisInput(feedback, prior, clarification, nil)

	if input.AnalysisMode != selfImprovementAnalysisModeClarification || !input.ClarificationPresent {
		t.Fatalf("assistant input mode = %q clarification_present=%v, want clarification", input.AnalysisMode, input.ClarificationPresent)
	}
	if input.PriorRecommendation != prior || input.Clarification != clarification {
		t.Fatalf("assistant input prior=%+v clarification=%+v, want provided objects", input.PriorRecommendation, input.Clarification)
	}
}

func TestSelfImprovementBackendUsesRuntimeAnalystSettings(t *testing.T) {
	t.Parallel()

	st := newTempStore(t)
	if err := st.UpsertBackend("codex", fleet.Backend{Command: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatalf("upsert codex backend: %v", err)
	}
	if err := st.UpsertBackend("claude", fleet.Backend{Command: "claude", Models: []string{"claude-sonnet-4-5"}}); err != nil {
		t.Fatalf("upsert claude backend: %v", err)
	}
	if _, err := st.WriteRuntimeSettings(fleet.RuntimeSettings{
		SelfImprovementAnalyst: fleet.SelfImprovementAnalystRuntimeSettings{
			Backend: "claude",
			Model:   "claude-sonnet-4-5",
		},
	}); err != nil {
		t.Fatalf("write runtime settings: %v", err)
	}

	e := NewEngine(st, config.ProcessorConfig{}, nil, zerolog.Nop())
	backendName, backend, model, err := e.selfImprovementBackend()
	if err != nil {
		t.Fatalf("selfImprovementBackend: %v", err)
	}
	if backendName != "claude" || backend.Command != "claude" || model != "claude-sonnet-4-5" {
		t.Fatalf("selection = (%q, %+v, %q), want configured claude/claude-sonnet-4-5", backendName, backend, model)
	}
}

func TestSelfImprovementRecommendationSchemaIsStrict(t *testing.T) {
	t.Parallel()

	var schema map[string]any
	if err := json.Unmarshal([]byte(selfImprovementRecommendationSchema), &schema); err != nil {
		t.Fatalf("schema json: %v", err)
	}
	assertStrictObjectSchema(t, "root", schema)
}

func assertStrictObjectSchema(t *testing.T, path string, schema map[string]any) {
	t.Helper()
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) > 0 {
		requiredItems, ok := schema["required"].([]any)
		if !ok {
			t.Fatalf("%s: object with properties must declare required array", path)
		}
		required := make(map[string]struct{}, len(requiredItems))
		for _, item := range requiredItems {
			name, ok := item.(string)
			if !ok {
				t.Fatalf("%s: required item %v is not a string", path, item)
			}
			required[name] = struct{}{}
		}
		for name := range properties {
			if _, ok := required[name]; !ok {
				t.Fatalf("%s: property %q is missing from required", path, name)
			}
		}
	}
	for name, raw := range properties {
		child, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		assertStrictObjectSchema(t, path+"."+name, child)
		if items, ok := child["items"].(map[string]any); ok {
			assertStrictObjectSchema(t, path+"."+name+"[]", items)
		}
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

package daemon_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/daemon"
	"github.com/eloylp/agents/internal/daemon/daemontest"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/selfimprovement"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// testCfg builds a *config.Config suitable for webhook tests, with the
// daemon defaults the real Daemon requires populated.
func testCfg(mutator func(*config.Config)) *config.Config {
	cfg := daemontest.MinimalCfg(func(c *config.Config) {
		c.Daemon.HTTP.MaxBodyBytes = 1024
		c.Daemon.HTTP.WebhookSecret = "secret"
	})
	if mutator != nil {
		mutator(cfg)
	}
	return cfg
}

func signatureForTests(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// newTestServer constructs a real *daemon.Daemon backed by a tempdir
// SQLite seeded from cfg. The returned channels point at the same
// EventChan production reads from, so tests can drain events the
// webhook handler enqueued.
func newTestServer(t *testing.T, cfg *config.Config) (*daemon.Daemon, *workflow.DataChannels) {
	t.Helper()
	srv := daemontest.New(t, cfg)
	return srv, srv.Channels()
}

func TestBuildRouterRegistersExpectedRoutes(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t, testCfg(nil))
	router := srv.Router()

	expected := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/status"},
		{http.MethodGet, "/"},
		{http.MethodPost, "/run"},
		{http.MethodPost, "/webhooks/github"},
		{http.MethodGet, "/auth/status"},
		{http.MethodPost, "/auth/bootstrap"},
		{http.MethodPost, "/auth/login"},
		{http.MethodPost, "/auth/logout"},
		{http.MethodGet, "/auth/me"},
		{http.MethodGet, "/auth/users"},
		{http.MethodPost, "/auth/users"},
		{http.MethodDelete, "/auth/users/2"},
		{http.MethodGet, "/auth/tokens"},
		{http.MethodPost, "/auth/tokens"},
		{http.MethodDelete, "/auth/tokens/1"},
		{http.MethodGet, "/agents"},
		{http.MethodPost, "/agents"},
		{http.MethodGet, "/agents/orphans/status"},
		{http.MethodGet, "/agents/coder"},
		{http.MethodPatch, "/agents/coder"},
		{http.MethodDelete, "/agents/coder"},
		{http.MethodGet, "/graph/layout"},
		{http.MethodPut, "/graph/layout"},
		{http.MethodDelete, "/graph/layout"},
		{http.MethodGet, "/skills"},
		{http.MethodPost, "/skills"},
		{http.MethodGet, "/skills/reviewer"},
		{http.MethodPatch, "/skills/reviewer"},
		{http.MethodDelete, "/skills/reviewer"},
		{http.MethodGet, "/guardrails"},
		{http.MethodPost, "/guardrails"},
		{http.MethodGet, "/guardrails/security"},
		{http.MethodPatch, "/guardrails/security"},
		{http.MethodDelete, "/guardrails/security"},
		{http.MethodPost, "/guardrails/security/reset"},
		{http.MethodGet, "/backends"},
		{http.MethodPost, "/backends"},
		{http.MethodGet, "/backends/status"},
		{http.MethodPost, "/backends/discover"},
		{http.MethodPost, "/backends/local"},
		{http.MethodGet, "/backends/claude"},
		{http.MethodPatch, "/backends/claude"},
		{http.MethodDelete, "/backends/claude"},
		{http.MethodGet, "/repos"},
		{http.MethodPost, "/repos"},
		{http.MethodGet, "/repos/owner/repo"},
		{http.MethodPatch, "/repos/owner/repo"},
		{http.MethodDelete, "/repos/owner/repo"},
		{http.MethodPost, "/repos/owner/repo/bindings"},
		{http.MethodGet, "/repos/owner/repo/bindings/1"},
		{http.MethodPatch, "/repos/owner/repo/bindings/1"},
		{http.MethodDelete, "/repos/owner/repo/bindings/1"},
		{http.MethodGet, "/config"},
		{http.MethodGet, "/export"},
		{http.MethodPost, "/import"},
		{http.MethodGet, "/token_budgets"},
		{http.MethodPost, "/token_budgets"},
		{http.MethodGet, "/token_budgets/alerts"},
		{http.MethodGet, "/token_budgets/1"},
		{http.MethodPatch, "/token_budgets/1"},
		{http.MethodDelete, "/token_budgets/1"},
		{http.MethodGet, "/token_leaderboard"},
		{http.MethodGet, "/events"},
		{http.MethodGet, "/traces"},
		{http.MethodGet, "/traces/root-1"},
		{http.MethodGet, "/traces/span-1/steps"},
		{http.MethodGet, "/traces/span-1/prompt"},
		{http.MethodGet, "/graph"},
		{http.MethodGet, "/dispatches"},
		{http.MethodGet, "/memory/coder/owner_repo"},
		{http.MethodGet, "/improvements/feedback"},
		{http.MethodGet, "/improvements/recommendations"},
		{http.MethodGet, "/improvements/recommendations/rec_1"},
		{http.MethodPost, "/improvements/recommendations/rec_1/status"},
		{http.MethodPost, "/improvements/recommendations/rec_1/clarification"},
		{http.MethodPost, "/improvements/recommendations/rec_1/proposal-bundle"},
		{http.MethodGet, "/improvements/recommendations/rec_1/proposal-bundle"},
		{http.MethodPatch, "/improvements/proposal-bundles/bundle_1/items/item_1"},
		{http.MethodPost, "/improvements/proposal-bundles/bundle_1/items/item_1/reject"},
		{http.MethodPost, "/improvements/proposal-bundles/bundle_1/items/item_1/link-existing"},
		{http.MethodPost, "/improvements/proposal-bundles/bundle_1/publish"},
		{http.MethodPost, "/improvements/proposal-bundles/bundle_1/discard"},
		{http.MethodPost, "/improvements/feedback/1/analyze"},
		{http.MethodGet, "/runners"},
		{http.MethodDelete, "/runners/1"},
		{http.MethodPost, "/runners/1/retry"},
		{http.MethodPost, "/mcp"},
	}

	for _, tc := range expected {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if !router.Match(req, &mux.RouteMatch{}) {
				t.Fatalf("route not registered")
			}
		})
	}
}

func TestImprovementProposalBundleRESTLifecycle(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, testCfg(nil))
	recID := seedProposalBundleRecommendation(t, srv, "publish")

	var created selfimprovement.SelfImprovementProposalBundle
	serveJSON(t, srv, http.MethodPost, "/improvements/recommendations/"+recID+"/proposal-bundle", "", http.StatusOK, &created)
	if created.ID == "" || created.Status != selfimprovement.ProposalBundleStatusPending || len(created.Items) != 3 {
		t.Fatalf("created bundle = %+v, want pending bundle with three items", created)
	}

	var fetched selfimprovement.SelfImprovementProposalBundle
	serveJSON(t, srv, http.MethodGet, "/improvements/recommendations/"+recID+"/proposal-bundle", "", http.StatusOK, &fetched)
	if fetched.ID != created.ID {
		t.Fatalf("fetched bundle id = %q, want %q", fetched.ID, created.ID)
	}

	guardItem := bundleItemBy(t, created, func(item selfimprovement.SelfImprovementBundleItem) bool {
		return item.AssetType == "guardrail" && item.Operation == selfimprovement.ProposalBundleOperationUpdateExisting
	})
	var edited selfimprovement.SelfImprovementProposalBundle
	serveJSON(t, srv, http.MethodPatch, "/improvements/proposal-bundles/"+created.ID+"/items/"+guardItem.ID, `{"proposed_body":"guard body edited"}`, http.StatusOK, &edited)
	guardItem = bundleItemBy(t, edited, func(item selfimprovement.SelfImprovementBundleItem) bool { return item.ID == guardItem.ID })
	if guardItem.ProposedBody != "guard body edited" || guardItem.ProposedDescription != "guard desc v2" || guardItem.ProposedEnabled || guardItem.ProposedPosition != 17 {
		t.Fatalf("body-only REST patch guardrail item = %+v, want body edit with metadata preserved", guardItem)
	}

	promptItem := bundleItemBy(t, created, func(item selfimprovement.SelfImprovementBundleItem) bool { return item.AssetType == "prompt" })
	var rejected selfimprovement.SelfImprovementProposalBundle
	serveJSON(t, srv, http.MethodPost, "/improvements/proposal-bundles/"+created.ID+"/items/"+promptItem.ID+"/reject", `{"reason":"not needed"}`, http.StatusOK, &rejected)
	promptItem = bundleItemBy(t, rejected, func(item selfimprovement.SelfImprovementBundleItem) bool { return item.ID == promptItem.ID })
	if promptItem.Decision != selfimprovement.ProposalBundleDecisionRejected || promptItem.DecisionReason != "not needed" {
		t.Fatalf("rejected item = decision %q reason %q, want rejected decision", promptItem.Decision, promptItem.DecisionReason)
	}

	skillItem := bundleItemBy(t, created, func(item selfimprovement.SelfImprovementBundleItem) bool { return item.AssetType == "skill" })
	var linked selfimprovement.SelfImprovementProposalBundle
	serveJSON(t, srv, http.MethodPost, "/improvements/proposal-bundles/"+created.ID+"/items/"+skillItem.ID+"/link-existing", `{"asset_id":"existing-rest-skill","reason":"already exists"}`, http.StatusOK, &linked)
	skillItem = bundleItemBy(t, linked, func(item selfimprovement.SelfImprovementBundleItem) bool { return item.ID == skillItem.ID })
	if skillItem.Decision != selfimprovement.ProposalBundleDecisionLinkedExisting || skillItem.AssetID != "existing-rest-skill" || skillItem.DecisionReason != "already exists" {
		t.Fatalf("linked item = %+v, want linked_existing decision evidence", skillItem)
	}

	var published selfimprovement.SelfImprovementProposalBundle
	serveJSON(t, srv, http.MethodPost, "/improvements/proposal-bundles/"+created.ID+"/publish", "", http.StatusOK, &published)
	if published.Status != selfimprovement.ProposalBundleStatusPublished {
		t.Fatalf("published status = %q, want published", published.Status)
	}
	gotGuardrail, err := srv.Store().GetGuardrail("rest-bundle-guardrail")
	if err != nil {
		t.Fatalf("read published guardrail: %v", err)
	}
	if gotGuardrail.Description != "guard desc v2" || gotGuardrail.Content != "guard body edited" || gotGuardrail.Enabled || gotGuardrail.Position != 17 {
		t.Fatalf("published guardrail = %+v, want edited REST body and preserved metadata", gotGuardrail)
	}

	discardRecID := seedProposalBundleRecommendation(t, srv, "discard")
	var discardBundle selfimprovement.SelfImprovementProposalBundle
	serveJSON(t, srv, http.MethodPost, "/improvements/recommendations/"+discardRecID+"/proposal-bundle", "", http.StatusOK, &discardBundle)
	var discarded selfimprovement.SelfImprovementProposalBundle
	serveJSON(t, srv, http.MethodPost, "/improvements/proposal-bundles/"+discardBundle.ID+"/discard", "", http.StatusOK, &discarded)
	if discarded.Status != selfimprovement.ProposalBundleStatusDiscarded {
		t.Fatalf("discarded status = %q, want discarded", discarded.Status)
	}
}

func seedProposalBundleRecommendation(t *testing.T, srv *daemon.Daemon, suffix string) string {
	t.Helper()
	st := srv.Store()
	prompt, err := st.UpsertPrompt(fleet.Prompt{
		Name:        "rest-bundle-prompt-" + suffix,
		Description: "prompt desc",
		Content:     "prompt v1",
	})
	if err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
	if err := st.UpsertSkill("existing-rest-skill", fleet.Skill{Name: "existing-rest-skill", Prompt: "existing skill"}); err != nil {
		t.Fatalf("seed existing skill: %v", err)
	}
	guardrailName := "rest-bundle-guardrail"
	if suffix != "publish" {
		guardrailName += "-" + suffix
	}
	if err := st.UpsertGuardrail(fleet.Guardrail{
		Name:        guardrailName,
		Description: "guard desc v1",
		Content:     "guard v1",
		Enabled:     true,
		Position:    9,
	}); err != nil {
		t.Fatalf("seed guardrail: %v", err)
	}
	guardrail, err := st.GetGuardrail(guardrailName)
	if err != nil {
		t.Fatalf("read seeded guardrail: %v", err)
	}
	feedback, err := st.UpsertSelfImprovementFeedback(store.SelfImprovementFeedbackInput{
		WorkspaceID:      fleet.DefaultWorkspaceID,
		RepoOwner:        "owner",
		RepoName:         "repo",
		SourceType:       "issue_comment",
		GitHubCommentID:  proposalBundleCommentIDForTest(suffix),
		SourceURL:        "https://github.com/owner/repo/issues/683#issuecomment-" + suffix,
		AuthorLogin:      "maintainer",
		AuthorAuthorized: true,
		IssueNumber:      683,
		RawBody:          "multi asset feedback " + suffix,
		Tag:              store.FeedbackTag,
		LinkConfidence:   "exact",
	})
	if err != nil {
		t.Fatalf("seed feedback: %v", err)
	}
	rec, err := selfimprovement.New(st).RecordRecommendation(selfimprovement.SelfImprovementRecommendationInput{
		WorkspaceID:           fleet.DefaultWorkspaceID,
		FeedbackEventID:       feedback.ID,
		Type:                  "catalog_patch_bundle",
		Status:                selfimprovement.RecommendationStatusAccepted,
		Finding:               "coordinated catalog update",
		Rationale:             "prompt, skill, and guardrail need a narrow update",
		AnalyzerPromptRef:     "prompt_self-improvement-analyst",
		AttributionConfidence: "exact",
		StructuredOutput: map[string]any{
			"changes": []map[string]any{
				{"operation": "update_existing", "asset_type": "prompt", "asset_id": prompt.ID, "base_version_id": prompt.VersionID, "proposed_body": "prompt v2"},
				{"operation": "update_existing", "asset_type": "guardrail", "asset_id": guardrailName, "base_version_id": guardrail.VersionID, "proposed_body": "guard v2", "proposed_description": "guard desc v2", "proposed_enabled": false, "proposed_position": 17},
				{"operation": "create_new", "asset_type": "skill", "proposed_ref": "new-rest-skill-" + suffix, "proposed_name": "new rest skill " + suffix, "proposed_scope": "workspace", "proposed_body": "new skill"},
			},
		},
	})
	if err != nil {
		t.Fatalf("seed recommendation: %v", err)
	}
	return rec.ID
}

func serveJSON(t *testing.T, srv *daemon.Daemon, method, path, body string, wantStatus int, out any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != wantStatus {
		t.Fatalf("%s %s: got status %d body %q, want %d", method, path, rr.Code, rr.Body.String(), wantStatus)
	}
	if out != nil {
		if err := json.NewDecoder(rr.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode response: %v", method, path, err)
		}
	}
}

func proposalBundleCommentIDForTest(suffix string) int64 {
	if suffix == "publish" {
		return 683001
	}
	return 683002
}

func bundleItemBy(t *testing.T, bundle selfimprovement.SelfImprovementProposalBundle, match func(selfimprovement.SelfImprovementBundleItem) bool) selfimprovement.SelfImprovementBundleItem {
	t.Helper()
	for _, item := range bundle.Items {
		if match(item) {
			return item
		}
	}
	t.Fatalf("matching bundle item not found in %+v", bundle.Items)
	return selfimprovement.SelfImprovementBundleItem{}
}

// webhookRequest builds a signed POST request to /webhooks/github.
func webhookRequest(t *testing.T, event, deliveryID, body string) *http.Request {
	t.Helper()
	sig := signatureForTests([]byte(body), "secret")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	req.Header.Set("X-Hub-Signature-256", sig)
	return req
}

// drainEvent returns the next Event from dc or fails the test.
func drainEvent(t *testing.T, dc *workflow.DataChannels) workflow.Event {
	t.Helper()
	select {
	case qe := <-dc.EventChan():
		return qe.Event
	default:
		t.Fatal("expected an event in the queue but found none")
		panic("unreachable")
	}
}

// assertNoEvent fails if dc has a queued event.
func assertNoEvent(t *testing.T, dc *workflow.DataChannels) {
	t.Helper()
	select {
	case <-dc.EventChan():
		t.Fatal("expected no event in the queue but found one")
	default:
	}
}

// ─── issues events ────────────────────────────────────────────────────────────

func TestHandleIssueWebhookDeduplicatesDelivery(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(t, testCfg(nil))

	body := `{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":1},"sender":{"login":"octocat"}}`

	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, webhookRequest(t, "issues", "delivery-1", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("first delivery: got %d, want %d", rr.Code, http.StatusAccepted)
	}

	rr2 := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr2, webhookRequest(t, "issues", "delivery-1", body))
	if rr2.Code != http.StatusAccepted {
		t.Fatalf("dedup delivery: got %d, want %d", rr2.Code, http.StatusAccepted)
	}

	// Only one Event should have been pushed.
	drainEvent(t, dc)
	assertNoEvent(t, dc)
}

func TestHandleIssuesLabeledEnqueuesEventWithLabel(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(t, testCfg(nil))

	body := `{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":7},"sender":{"login":"octocat"}}`
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, webhookRequest(t, "issues", "d-1", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}

	ev := drainEvent(t, dc)
	if ev.Kind != "issues.labeled" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "issues.labeled")
	}
	if ev.Number != 7 {
		t.Errorf("number: got %d, want 7", ev.Number)
	}
	if ev.Actor != "octocat" {
		t.Errorf("actor: got %q, want %q", ev.Actor, "octocat")
	}
	if ev.Payload["label"] != "ai:refine" {
		t.Errorf("payload label: got %v, want %q", ev.Payload["label"], "ai:refine")
	}
}

func TestHandleIssuesOpenedEnqueuesEvent(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(t, testCfg(nil))

	body := `{"action":"opened","repository":{"full_name":"owner/repo"},"issue":{"number":10,"title":"Bug","body":"desc"},"sender":{"login":"dev"}}`
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, webhookRequest(t, "issues", "d-opened", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}

	ev := drainEvent(t, dc)
	if ev.Kind != "issues.opened" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "issues.opened")
	}
	if ev.Number != 10 {
		t.Errorf("number: got %d, want 10", ev.Number)
	}
	if ev.Actor != "dev" {
		t.Errorf("actor: got %q, want %q", ev.Actor, "dev")
	}
}

// TestHandleNonAILabelEnqueues verifies that non-AI labels are enqueued for both
// pull_request.labeled and issues.labeled so that event-based bindings
// (events: ["pull_request.labeled"] / events: ["issues.labeled"]) can match
// them. Label-based bindings are filtered later by the engine via agentsForEvent.
func TestHandleNonAILabelEnqueues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		event     string
		delivery  string
		body      string
		wantKind  string
		wantLabel string
	}{
		{
			name:      "pull_request.labeled",
			event:     "pull_request",
			delivery:  "delivery-non-ai",
			body:      `{"action":"labeled","label":{"name":"bug"},"repository":{"full_name":"owner/repo"},"pull_request":{"number":2},"sender":{"login":"dev"}}`,
			wantKind:  "pull_request.labeled",
			wantLabel: "bug",
		},
		{
			name:      "issues.labeled",
			event:     "issues",
			delivery:  "delivery-issue-non-ai",
			body:      `{"action":"labeled","label":{"name":"enhancement"},"repository":{"full_name":"owner/repo"},"issue":{"number":9},"sender":{"login":"dev"}}`,
			wantKind:  "issues.labeled",
			wantLabel: "enhancement",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, dc := newTestServer(t, testCfg(nil))
			rr := httptest.NewRecorder()
			server.Handler().ServeHTTP(rr, webhookRequest(t, tc.event, tc.delivery, tc.body))
			if rr.Code != http.StatusAccepted {
				t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
			}
			ev := drainEvent(t, dc)
			if ev.Kind != tc.wantKind {
				t.Errorf("kind: got %q, want %q", ev.Kind, tc.wantKind)
			}
			if ev.Payload["label"] != tc.wantLabel {
				t.Errorf("payload label: got %v, want %q", ev.Payload["label"], tc.wantLabel)
			}
		})
	}
}

// TestHandleIssuesEventDropsPRBackedIssueActions verifies that issues events
// for PR-backed issues are dropped for every action type. GitHub routes some
// issue events (labeled, opened, …) for pull requests through the issues
// webhook; the server drops them because pull_request events handle those.
func TestHandleIssuesEventDropsPRBackedIssueActions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{
			name: "labeled",
			body: `{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":3,"pull_request":{}}}`,
		},
		{
			name: "opened",
			body: `{"action":"opened","repository":{"full_name":"owner/repo"},"issue":{"number":3,"title":"t","body":"b","pull_request":{}},"sender":{"login":"dev"}}`,
		},
		{
			name: "edited",
			body: `{"action":"edited","repository":{"full_name":"owner/repo"},"issue":{"number":3,"title":"t","body":"b","pull_request":{}},"sender":{"login":"dev"}}`,
		},
		{
			name: "reopened",
			body: `{"action":"reopened","repository":{"full_name":"owner/repo"},"issue":{"number":3,"title":"t","body":"b","pull_request":{}},"sender":{"login":"dev"}}`,
		},
		{
			name: "closed",
			body: `{"action":"closed","repository":{"full_name":"owner/repo"},"issue":{"number":3,"title":"t","body":"b","pull_request":{}},"sender":{"login":"dev"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, dc := newTestServer(t, testCfg(nil))
			rr := httptest.NewRecorder()
			server.Handler().ServeHTTP(rr, webhookRequest(t, "issues", "d-pr-"+tc.name, tc.body))
			if rr.Code != http.StatusAccepted {
				t.Fatalf("action %q: got %d, want %d", tc.name, rr.Code, http.StatusAccepted)
			}
			assertNoEvent(t, dc)
		})
	}
}

// ─── pull_request events ──────────────────────────────────────────────────────

func TestHandlePullRequestLabeledEnqueuesEvent(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(t, testCfg(nil))

	body := `{"action":"labeled","label":{"name":"ai:review"},"repository":{"full_name":"owner/repo"},"pull_request":{"number":5},"sender":{"login":"bot"}}`
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, webhookRequest(t, "pull_request", "d-pr-labeled", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}

	ev := drainEvent(t, dc)
	if ev.Kind != "pull_request.labeled" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "pull_request.labeled")
	}
	if ev.Payload["label"] != "ai:review" {
		t.Errorf("payload label: got %v", ev.Payload["label"])
	}
}

func TestHandleDraftPRWebhookDoesNotEnqueue(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(t, testCfg(nil))

	body := `{"action":"labeled","label":{"name":"ai:review"},"repository":{"full_name":"owner/repo"},"pull_request":{"number":5,"draft":true}}`
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, webhookRequest(t, "pull_request", "delivery-draft", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}
	assertNoEvent(t, dc)
}

func TestHandlePullRequestOpenedEnqueuesEvent(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(t, testCfg(nil))

	body := `{"action":"opened","repository":{"full_name":"owner/repo"},"pull_request":{"number":8,"title":"feat","draft":false},"sender":{"login":"dev"}}`
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, webhookRequest(t, "pull_request", "d-pr-opened", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}

	ev := drainEvent(t, dc)
	if ev.Kind != "pull_request.opened" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "pull_request.opened")
	}
	if ev.Number != 8 {
		t.Errorf("number: got %d, want 8", ev.Number)
	}
}

func TestHandlePullRequestClosedPayloadIncludesMerged(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		merged     bool
		deliveryID string
	}{
		{name: "merged close", merged: true, deliveryID: "d-pr-closed-merged"},
		{name: "non-merged close", merged: false, deliveryID: "d-pr-closed-ordinary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, dc := newTestServer(t, testCfg(nil))

			mergedVal := "false"
			if tc.merged {
				mergedVal = "true"
			}
			body := `{"action":"closed","repository":{"full_name":"owner/repo"},"pull_request":{"number":12,"title":"feat","draft":false,"merged":` + mergedVal + `},"sender":{"login":"dev"}}`
			rr := httptest.NewRecorder()
			server.Handler().ServeHTTP(rr, webhookRequest(t, "pull_request", tc.deliveryID, body))
			if rr.Code != http.StatusAccepted {
				t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
			}

			ev := drainEvent(t, dc)
			if ev.Kind != "pull_request.closed" {
				t.Errorf("kind: got %q, want %q", ev.Kind, "pull_request.closed")
			}
			if ev.Number != 12 {
				t.Errorf("number: got %d, want 12", ev.Number)
			}
			got, ok := ev.Payload["merged"].(bool)
			if !ok {
				t.Fatalf("payload[merged] missing or not bool: %v", ev.Payload["merged"])
			}
			if got != tc.merged {
				t.Errorf("payload[merged]: got %v, want %v", got, tc.merged)
			}
		})
	}
}

// ─── comment and review enqueue events ───────────────────────────────────────

func TestHandleCommentAndReviewEventsEnqueue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		eventType   string
		deliveryID  string
		body        string
		wantKind    string
		wantNumber  int
		wantActor   string
		wantPayload map[string]any
	}{
		{
			name:        "issue_comment.created",
			eventType:   "issue_comment",
			deliveryID:  "d-comment",
			body:        `{"action":"created","comment":{"body":"LGTM"},"issue":{"number":11},"repository":{"full_name":"owner/repo"},"sender":{"login":"reviewer"}}`,
			wantKind:    "issue_comment.created",
			wantNumber:  11,
			wantActor:   "reviewer",
			wantPayload: map[string]any{"body": "LGTM"},
		},
		{
			name:        "pull_request_review.submitted",
			eventType:   "pull_request_review",
			deliveryID:  "d-review",
			body:        `{"action":"submitted","review":{"state":"approved","body":"LGTM"},"pull_request":{"number":9},"repository":{"full_name":"owner/repo"},"sender":{"login":"approver"}}`,
			wantKind:    "pull_request_review.submitted",
			wantNumber:  9,
			wantPayload: map[string]any{"state": "approved"},
		},
		{
			name:        "pull_request_review_comment.created",
			eventType:   "pull_request_review_comment",
			deliveryID:  "d-rc-1",
			body:        `{"action":"created","comment":{"body":"nit: rename this"},"pull_request":{"number":7},"repository":{"full_name":"owner/repo"},"sender":{"login":"reviewer"}}`,
			wantKind:    "pull_request_review_comment.created",
			wantNumber:  7,
			wantActor:   "reviewer",
			wantPayload: map[string]any{"body": "nit: rename this"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, dc := newTestServer(t, testCfg(nil))
			rr := httptest.NewRecorder()
			server.Handler().ServeHTTP(rr, webhookRequest(t, tc.eventType, tc.deliveryID, tc.body))
			if rr.Code != http.StatusAccepted {
				t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
			}
			ev := drainEvent(t, dc)
			if ev.Kind != tc.wantKind {
				t.Errorf("kind: got %q, want %q", ev.Kind, tc.wantKind)
			}
			if ev.Number != tc.wantNumber {
				t.Errorf("number: got %d, want %d", ev.Number, tc.wantNumber)
			}
			if tc.wantActor != "" && ev.Actor != tc.wantActor {
				t.Errorf("actor: got %q, want %q", ev.Actor, tc.wantActor)
			}
			for k, want := range tc.wantPayload {
				if got := ev.Payload[k]; got != want {
					t.Errorf("payload[%q]: got %v, want %v", k, got, want)
				}
			}
		})
	}
}

// TestHandleNonTriggeringActionsIgnored verifies that non-triggering actions for
// comment and review event types are accepted but produce no workflow event.
func TestHandleNonTriggeringActionsIgnored(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		eventType  string
		deliveryID string
		body       string
	}{
		{
			name:       "issue_comment non-created action ignored",
			eventType:  "issue_comment",
			deliveryID: "d-edit",
			body:       `{"action":"edited","comment":{"body":"updated"},"issue":{"number":1},"repository":{"full_name":"owner/repo"},"sender":{"login":"u"}}`,
		},
		{
			name:       "pull_request_review non-submitted action ignored",
			eventType:  "pull_request_review",
			deliveryID: "d-dismissed",
			body:       `{"action":"dismissed","review":{"state":"dismissed"},"pull_request":{"number":1},"repository":{"full_name":"owner/repo"},"sender":{"login":"u"}}`,
		},
		{
			name:       "pull_request_review_comment non-created action ignored",
			eventType:  "pull_request_review_comment",
			deliveryID: "d-rc-2",
			body:       `{"action":"edited","comment":{"body":"updated"},"pull_request":{"number":7},"repository":{"full_name":"owner/repo"},"sender":{"login":"u"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, dc := newTestServer(t, testCfg(nil))
			rr := httptest.NewRecorder()
			server.Handler().ServeHTTP(rr, webhookRequest(t, tc.eventType, tc.deliveryID, tc.body))
			if rr.Code != http.StatusAccepted {
				t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
			}
			assertNoEvent(t, dc)
		})
	}
}

// ─── push events ─────────────────────────────────────────────────────────────

func TestHandlePushEnqueuesEvent(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(t, testCfg(nil))

	body := `{"ref":"refs/heads/main","after":"abc123","repository":{"full_name":"owner/repo"},"sender":{"login":"pusher"}}`
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, webhookRequest(t, "push", "d-push", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}

	ev := drainEvent(t, dc)
	if ev.Kind != "push" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "push")
	}
	if ev.Actor != "pusher" {
		t.Errorf("actor: got %q, want %q", ev.Actor, "pusher")
	}
	if ev.Payload["ref"] != "refs/heads/main" {
		t.Errorf("payload ref: got %v", ev.Payload["ref"])
	}
	if ev.Payload["head_sha"] != "abc123" {
		t.Errorf("payload head_sha: got %v", ev.Payload["head_sha"])
	}
}

func TestHandlePushIgnoresNonBranchRefs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ref  string
		sha  string
	}{
		{name: "tag push", ref: "refs/tags/v1.0.0", sha: "abc123"},
		{name: "branch deletion", ref: "refs/heads/main", sha: "0000000000000000000000000000000000000000"},
		{name: "notes ref", ref: "refs/notes/commits", sha: "abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, dc := newTestServer(t, testCfg(nil))
			body := `{"ref":"` + tc.ref + `","after":"` + tc.sha + `","repository":{"full_name":"owner/repo"},"sender":{"login":"pusher"}}`
			rr := httptest.NewRecorder()
			server.Handler().ServeHTTP(rr, webhookRequest(t, "push", "d-push-"+tc.name, body))
			if rr.Code != http.StatusAccepted {
				t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
			}
			assertNoEvent(t, dc)
		})
	}
}

// ─── unknown event ────────────────────────────────────────────────────────────

func TestHandleUnknownEventReturnsAccepted(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(t, testCfg(nil))

	body := `{"action":"something"}`
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, webhookRequest(t, "unknown_event", "d-unknown", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}
	assertNoEvent(t, dc)
}

// ─── queue-full ───────────────────────────────────────────────────────────────

func TestHandleWebhookReturnsServiceUnavailableWhenQueueFull(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) { c.Daemon.Processor.EventQueueBuffer = 1 })
	srv, dc := newTestServer(t, cfg)
	// Preload the queue so the next push trips ErrEventQueueFull.
	if _, err := dc.PushEvent(context.Background(), workflow.Event{
		Repo:   workflow.RepoRef{FullName: cfg.Repos[0].Name, Enabled: cfg.Repos[0].Enabled},
		Kind:   "issues.labeled",
		Number: 99,
	}); err != nil {
		t.Fatalf("preload event queue: %v", err)
	}

	body := `{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":2}}`
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, webhookRequest(t, "issues", "delivery-queue-full", body))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("queue full: got %d, want %d body=%s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}

	// Delivery ID must be released so a retry can succeed.
	<-dc.EventChan()
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, webhookRequest(t, "issues", "delivery-queue-full", body))
	if rr2.Code != http.StatusAccepted {
		t.Fatalf("retry: got %d, want %d", rr2.Code, http.StatusAccepted)
	}
}

// ─── /agents/run endpoint tests ───────────────────────────────────────────────

func newRunServer(t *testing.T) *daemon.Daemon {
	t.Helper()
	srv, _ := newTestServer(t, testCfg(nil))
	return srv
}

func newRequest(method, path, body string) *http.Request {
	return httptest.NewRequest(method, path, strings.NewReader(body))
}

func bootstrapSessionCookie(t *testing.T, server *daemon.Daemon) *http.Cookie {
	t.Helper()
	req := newRequest(http.MethodPost, "/auth/bootstrap", `{"username":"admin","password":"correct horse battery staple"}`)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.AuthHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("bootstrap got %d, want %d: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}
	for _, cookie := range rr.Result().Cookies() {
		if cookie.Name == "agents_session" {
			return cookie
		}
	}
	t.Fatal("bootstrap did not set agents_session cookie")
	return nil
}

func TestHandleAgentsRunEnqueuesEvent(t *testing.T) {
	t.Parallel()
	server := newRunServer(t)

	req := newRequest(http.MethodPost, "/run", `{"agent":"coder","repo":"owner/repo"}`)
	req.AddCookie(bootstrapSessionCookie(t, server))
	rr := httptest.NewRecorder()
	server.AuthHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected %d, got %d: %s", http.StatusAccepted, rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "queued" || resp["agent"] != "coder" || resp["repo"] != "owner/repo" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp["event_id"] == "" {
		t.Fatal("expected non-empty event_id")
	}
}

func TestHandleAgentsRunNormalizesWorkspace(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Workspaces = append(c.Workspaces, fleet.Workspace{ID: "team-a", Name: "Team A"})
		c.Agents = append(c.Agents, fleet.Agent{
			WorkspaceID: "team-a",
			Name:        "coder",
			Backend:     "claude",
			PromptRef:   "coder",
			Description: "team coder",
		})
		c.Repos = append(c.Repos, fleet.Repo{
			WorkspaceID: "team-a",
			Name:        "owner/team",
			Enabled:     true,
		})
	})
	server, dc := newTestServer(t, cfg)

	req := newRequest(http.MethodPost, "/run", `{"agent":"coder","repo":"OWNER/TEAM","workspace":"Team-A"}`)
	req.AddCookie(bootstrapSessionCookie(t, server))
	rr := httptest.NewRecorder()
	server.AuthHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected %d, got %d: %s", http.StatusAccepted, rr.Code, rr.Body.String())
	}
	ev := drainEvent(t, dc)
	if ev.WorkspaceID != "team-a" || ev.Repo.FullName != "owner/team" {
		t.Fatalf("event scope = %q/%q, want team-a/owner/team", ev.WorkspaceID, ev.Repo.FullName)
	}
}

func TestHandleAgentsRunReturnsBadRequestOnMissingFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{"missing agent", `{"repo":"owner/repo"}`},
		{"missing repo", `{"agent":"coder"}`},
		{"empty body", `{}`},
		{"empty agent", `{"agent":""}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := newRunServer(t)
			req := newRequest(http.MethodPost, "/run", tc.body)
			req.AddCookie(bootstrapSessionCookie(t, server))
			rr := httptest.NewRecorder()
			server.AuthHandler().ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want %d", rr.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestHandleAgentsRunReturnsNotFoundForUnknownRepo(t *testing.T) {
	t.Parallel()
	server := newRunServer(t)
	req := newRequest(http.MethodPost, "/run", `{"agent":"coder","repo":"unknown/repo"}`)
	req.AddCookie(bootstrapSessionCookie(t, server))
	rr := httptest.NewRecorder()
	server.AuthHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestInvalidSignatureDoesNotPoisonDeliveryDedupe(t *testing.T) {
	t.Parallel()
	server, _ := newTestServer(t, testCfg(nil))

	body := `{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":7}}`

	reqBad := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	reqBad.Header.Set("X-GitHub-Event", "issues")
	reqBad.Header.Set("X-GitHub-Delivery", "delivery-poison")
	reqBad.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rrBad := httptest.NewRecorder()
	server.Handler().ServeHTTP(rrBad, reqBad)
	if rrBad.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature: got %d, want %d", rrBad.Code, http.StatusUnauthorized)
	}

	// Retry the same delivery ID with valid sig, it must be processed.
	sig := signatureForTests([]byte(body), "secret")
	reqGood := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	reqGood.Header.Set("X-GitHub-Event", "issues")
	reqGood.Header.Set("X-GitHub-Delivery", "delivery-poison")
	reqGood.Header.Set("X-Hub-Signature-256", sig)
	rrGood := httptest.NewRecorder()
	server.Handler().ServeHTTP(rrGood, reqGood)
	if rrGood.Code != http.StatusAccepted {
		t.Fatalf("retry with good sig: got %d body=%s", rrGood.Code, rrGood.Body.String())
	}
}

// TestUISlashlessRedirect verifies that GET /ui (no trailing slash) redirects
// to /ui/ with a 301. The UI FS is always mounted from the embedded next.js
// build, so the redirect is unconditional in production.
func TestUISlashlessRedirect(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t, testCfg(nil))
	ts := httptest.NewServer(srv.AuthHandler())
	defer ts.Close()

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse // do not follow redirects
		},
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ui", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("want 301, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc != "/ui/" {
		t.Fatalf("want Location /ui/, got %q", loc)
	}
}

// TestBuildHandlerPublicRoutesStayOpen verifies that setup/liveness/browser-shell
// routes are reachable without daemon auth. Sensitive APIs stay protected even
// before the first user is created.
func TestBuildHandlerPublicRoutesStayOpen(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t, testCfg(nil))
	ts := httptest.NewServer(srv.AuthHandler())
	t.Cleanup(ts.Close)

	openRoutes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/status"},
		{http.MethodGet, "/"},
		{http.MethodGet, "/auth/status"},
		{http.MethodGet, "/ui/"},
	}

	for _, tc := range openRoutes {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			req, _ := http.NewRequest(tc.method, ts.URL+tc.path, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("public route %s %s must be open, got 401", tc.method, tc.path)
			}
		})
	}
}

func TestBuildHandlerAuthProtectsSensitiveRoutes(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t, testCfg(nil))
	ts := httptest.NewServer(srv.AuthHandler())
	t.Cleanup(ts.Close)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "status stays public", method: http.MethodGet, path: "/status", wantStatus: http.StatusOK},
		{name: "ui shell stays public", method: http.MethodGet, path: "/ui/", wantStatus: http.StatusOK},
		{name: "config requires auth", method: http.MethodGet, path: "/config", wantStatus: http.StatusUnauthorized},
		{name: "agents requires auth", method: http.MethodGet, path: "/agents", wantStatus: http.StatusUnauthorized},
		{name: "repos requires auth", method: http.MethodGet, path: "/repos", wantStatus: http.StatusUnauthorized},
		{name: "skills requires auth", method: http.MethodGet, path: "/skills", wantStatus: http.StatusUnauthorized},
		{name: "runners requires auth", method: http.MethodGet, path: "/runners", wantStatus: http.StatusUnauthorized},
		{name: "traces requires auth", method: http.MethodGet, path: "/traces", wantStatus: http.StatusUnauthorized},
		{name: "events requires auth", method: http.MethodGet, path: "/events", wantStatus: http.StatusUnauthorized},
		{name: "graph requires auth", method: http.MethodGet, path: "/graph", wantStatus: http.StatusUnauthorized},
		{name: "memory requires auth", method: http.MethodGet, path: "/memory/coder/eloylp_agents", wantStatus: http.StatusUnauthorized},
		{name: "backends status requires auth", method: http.MethodGet, path: "/backends/status", wantStatus: http.StatusUnauthorized},
		{name: "orphans requires auth", method: http.MethodGet, path: "/agents/orphans/status", wantStatus: http.StatusUnauthorized},
		{name: "auth users requires auth", method: http.MethodGet, path: "/auth/users", wantStatus: http.StatusUnauthorized},
		{name: "auth me requires auth", method: http.MethodGet, path: "/auth/me", wantStatus: http.StatusUnauthorized},
		{name: "change own password requires auth", method: http.MethodPost, path: "/auth/me/password", wantStatus: http.StatusUnauthorized},
		{name: "logout requires auth", method: http.MethodPost, path: "/auth/logout", wantStatus: http.StatusUnauthorized},
		{name: "create auth user requires auth", method: http.MethodPost, path: "/auth/users", wantStatus: http.StatusUnauthorized},
		{name: "delete auth user requires auth", method: http.MethodDelete, path: "/auth/users/2", wantStatus: http.StatusUnauthorized},
		{name: "auth tokens requires auth", method: http.MethodGet, path: "/auth/tokens", wantStatus: http.StatusUnauthorized},
		{name: "create token requires auth", method: http.MethodPost, path: "/auth/tokens", wantStatus: http.StatusUnauthorized},
		{name: "revoke token requires auth", method: http.MethodDelete, path: "/auth/tokens/1", wantStatus: http.StatusUnauthorized},
		{name: "run requires auth", method: http.MethodPost, path: "/run", wantStatus: http.StatusUnauthorized},
		{name: "import requires auth", method: http.MethodPost, path: "/import", wantStatus: http.StatusUnauthorized},
		{name: "export requires auth", method: http.MethodGet, path: "/export", wantStatus: http.StatusUnauthorized},
		{name: "token budgets require auth", method: http.MethodGet, path: "/token_budgets", wantStatus: http.StatusUnauthorized},
		{name: "create token budget requires auth", method: http.MethodPost, path: "/token_budgets", wantStatus: http.StatusUnauthorized},
		{name: "token budget alerts require auth", method: http.MethodGet, path: "/token_budgets/alerts", wantStatus: http.StatusUnauthorized},
		{name: "token budget detail requires auth", method: http.MethodGet, path: "/token_budgets/1", wantStatus: http.StatusUnauthorized},
		{name: "token budget update requires auth", method: http.MethodPatch, path: "/token_budgets/1", wantStatus: http.StatusUnauthorized},
		{name: "token budget delete requires auth", method: http.MethodDelete, path: "/token_budgets/1", wantStatus: http.StatusUnauthorized},
		{name: "token leaderboard requires auth", method: http.MethodGet, path: "/token_leaderboard", wantStatus: http.StatusUnauthorized},
		{name: "discovery requires auth", method: http.MethodPost, path: "/backends/discover", wantStatus: http.StatusUnauthorized},
		{name: "local backend probe requires auth", method: http.MethodPost, path: "/backends/local", wantStatus: http.StatusUnauthorized},
		{name: "events stream requires auth", method: http.MethodGet, path: "/events/stream", wantStatus: http.StatusUnauthorized},
		{name: "traces stream requires auth", method: http.MethodGet, path: "/traces/stream", wantStatus: http.StatusUnauthorized},
		{name: "memory stream requires auth", method: http.MethodGet, path: "/memory/stream", wantStatus: http.StatusUnauthorized},
		{name: "trace step stream requires auth", method: http.MethodGet, path: "/traces/not-a-span/stream", wantStatus: http.StatusUnauthorized},
		{name: "mcp post requires auth", method: http.MethodPost, path: "/mcp", wantStatus: http.StatusUnauthorized},
		{name: "mcp get requires auth", method: http.MethodGet, path: "/mcp", wantStatus: http.StatusUnauthorized},
		{name: "mcp delete requires auth", method: http.MethodDelete, path: "/mcp", wantStatus: http.StatusUnauthorized},
		{name: "proxy models requires auth remotely", method: http.MethodGet, path: "/v1/models", wantStatus: http.StatusUnauthorized},
		{name: "proxy messages requires auth remotely", method: http.MethodPost, path: "/v1/messages", wantStatus: http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req, _ := http.NewRequest(tc.method, ts.URL+tc.path, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("%s %s got %d, want %d", tc.method, tc.path, resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

func TestBuildHandlerProxyRoutesAreLocalOnlyWithoutAuth(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t, testCfg(func(c *config.Config) {
		c.Daemon.Proxy = config.ProxyConfig{
			Enabled: true,
			Path:    "/v1/messages",
			Upstream: config.ProxyUpstreamConfig{
				URL:            "http://llm.local/v1",
				Model:          "local-model",
				TimeoutSeconds: 60,
			},
		}
	}))

	tests := []struct {
		name       string
		remoteAddr string
		wantStatus int
	}{
		{name: "remote proxy call requires auth", remoteAddr: "203.0.113.10:4444", wantStatus: http.StatusUnauthorized},
		{name: "loopback proxy call reaches proxy", remoteAddr: "127.0.0.1:4444", wantStatus: http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
			req.RemoteAddr = tc.remoteAddr
			rr := httptest.NewRecorder()
			srv.AuthHandler().ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("got %d, want %d: %s", rr.Code, tc.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestBuildHandlerDBAuthBootstrapLoginAndAPIToken(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t, testCfg(nil))
	ts := httptest.NewServer(srv.AuthHandler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("pre-bootstrap config request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("pre-bootstrap /config got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}

	bootstrapBody := []byte(`{"username":"admin","password":"correct horse battery staple"}`)
	resp, err = http.Post(ts.URL+"/auth/bootstrap", "application/json", bytes.NewReader(bootstrapBody))
	if err != nil {
		t.Fatalf("bootstrap request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap got %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "agents_session" {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly {
		t.Fatalf("bootstrap session cookie = %#v, want HttpOnly agents_session", sessionCookie)
	}

	noRedirect := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	resp, err = noRedirect.Do(req)
	if err != nil {
		t.Fatalf("root login request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unauthenticated root got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.AddCookie(sessionCookie)
	resp, err = noRedirect.Do(req)
	if err != nil {
		t.Fatalf("authenticated root request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/graph/" {
		t.Fatalf("authenticated root got status=%d location=%q, want 302 /ui/graph/", resp.StatusCode, resp.Header.Get("Location"))
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/config", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unauthenticated config request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-bootstrap unauthenticated /config got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/config", nil)
	req.AddCookie(sessionCookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("session config request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session /config got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/auth/users", nil)
	req.AddCookie(sessionCookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list users request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list users got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/auth/users", bytes.NewReader([]byte(`{"username":"operator","password":"correct horse battery staple"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create user request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		resp.Body.Close()
		t.Fatalf("create user got %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var user struct {
		ID      int64 `json:"id"`
		IsAdmin bool  `json:"is_admin"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		resp.Body.Close()
		t.Fatalf("decode created user: %v", err)
	}
	resp.Body.Close()
	if user.ID == 0 || user.IsAdmin {
		t.Fatalf("created user = %+v, want non-admin with id", user)
	}

	operatorLogin := []byte(`{"username":"operator","password":"correct horse battery staple"}`)
	resp, err = http.Post(ts.URL+"/auth/login", "application/json", bytes.NewReader(operatorLogin))
	if err != nil {
		t.Fatalf("operator login request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator login got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var operatorCookie *http.Cookie
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "agents_session" {
			operatorCookie = cookie
		}
	}
	if operatorCookie == nil {
		t.Fatal("operator login did not return session cookie")
	}

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/auth/users", bytes.NewReader([]byte(`{"username":"blocked","password":"correct horse battery staple"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(operatorCookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("non-admin create user request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin create user got %d, want %d", resp.StatusCode, http.StatusForbidden)
	}

	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/auth/users/1", nil)
	req.AddCookie(operatorCookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("non-admin delete user request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin delete user got %d, want %d", resp.StatusCode, http.StatusForbidden)
	}

	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/auth/users/1", nil)
	req.AddCookie(sessionCookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete admin request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete admin got %d, want %d", resp.StatusCode, http.StatusConflict)
	}

	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/auth/users/"+strconv.FormatInt(user.ID, 10), nil)
	req.AddCookie(sessionCookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete user request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete user got %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/auth/tokens", bytes.NewReader([]byte(`{"name":"Codex MCP"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create token request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token got %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var created struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if created.Token == "" {
		t.Fatal("created API token is empty")
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/config", nil)
	req.Header.Set("Authorization", "Bearer "+created.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("api token config request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("api token /config got %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestBuildHandlerAuthChangeOwnPassword(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t, testCfg(nil))
	ts := httptest.NewServer(srv.AuthHandler())
	t.Cleanup(ts.Close)

	adminCookie := bootstrapViaHTTP(t, ts.URL, "admin", "correct horse battery staple")

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/auth/me/password", bytes.NewReader([]byte(`{"current_password":"wrong","new_password":"new admin password"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(adminCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("wrong current password request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong current password got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/auth/me/password", bytes.NewReader([]byte(`{"current_password":"correct horse battery staple","new_password":""}`)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(adminCookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("empty new password request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty new password got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/auth/me/password", bytes.NewReader([]byte(`{"current_password":"correct horse battery staple","new_password":"new admin password"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(adminCookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("change password request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("change password got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if got := loginStatus(t, ts.URL, "admin", "correct horse battery staple"); got != http.StatusUnauthorized {
		t.Fatalf("old admin password login got %d, want %d", got, http.StatusUnauthorized)
	}
	if got := loginStatus(t, ts.URL, "admin", "new admin password"); got != http.StatusOK {
		t.Fatalf("new admin password login got %d, want %d", got, http.StatusOK)
	}
}

func TestBuildHandlerAuthNonAdminChangesOnlyOwnPassword(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t, testCfg(nil))
	ts := httptest.NewServer(srv.AuthHandler())
	t.Cleanup(ts.Close)

	adminCookie := bootstrapViaHTTP(t, ts.URL, "admin", "admin password")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/auth/users", bytes.NewReader([]byte(`{"username":"operator","password":"operator password"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(adminCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create operator request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create operator got %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	operatorCookie := loginViaHTTP(t, ts.URL, "operator", "operator password")
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/auth/me/password", bytes.NewReader([]byte(`{"current_password":"operator password","new_password":"new operator password"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(operatorCookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("operator change password request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator change password got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if got := loginStatus(t, ts.URL, "operator", "operator password"); got != http.StatusUnauthorized {
		t.Fatalf("old operator password login got %d, want %d", got, http.StatusUnauthorized)
	}
	if got := loginStatus(t, ts.URL, "operator", "new operator password"); got != http.StatusOK {
		t.Fatalf("new operator password login got %d, want %d", got, http.StatusOK)
	}
	if got := loginStatus(t, ts.URL, "admin", "admin password"); got != http.StatusOK {
		t.Fatalf("admin password login after operator change got %d, want %d", got, http.StatusOK)
	}
}

func bootstrapViaHTTP(t *testing.T, baseURL, username, password string) *http.Cookie {
	t.Helper()
	resp, err := http.Post(baseURL+"/auth/bootstrap", "application/json", bytes.NewReader([]byte(`{"username":`+strconv.Quote(username)+`,"password":`+strconv.Quote(password)+`}`)))
	if err != nil {
		t.Fatalf("bootstrap request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap got %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	return sessionCookieFromResponse(t, resp)
}

func loginViaHTTP(t *testing.T, baseURL, username, password string) *http.Cookie {
	t.Helper()
	resp, err := http.Post(baseURL+"/auth/login", "application/json", bytes.NewReader([]byte(`{"username":`+strconv.Quote(username)+`,"password":`+strconv.Quote(password)+`}`)))
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	return sessionCookieFromResponse(t, resp)
}

func loginStatus(t *testing.T, baseURL, username, password string) int {
	t.Helper()
	resp, err := http.Post(baseURL+"/auth/login", "application/json", bytes.NewReader([]byte(`{"username":`+strconv.Quote(username)+`,"password":`+strconv.Quote(password)+`}`)))
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func sessionCookieFromResponse(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "agents_session" {
			return cookie
		}
	}
	t.Fatal("response did not set agents_session cookie")
	return nil
}

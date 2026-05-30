package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

type fakeImprovementAnalyzer struct {
	store *store.Store
}

func (f fakeImprovementAnalyzer) AnalyzeSelfImprovementFeedback(_ context.Context, feedback store.SelfImprovementFeedback) (store.SelfImprovementRecommendation, error) {
	return f.store.UpsertSelfImprovementRecommendation(store.RecommendationFromFeedback(feedback))
}

// TestVerifySignature exercises the HMAC-SHA256 signature check that gates
// every incoming GitHub webhook delivery.
func TestVerifySignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"hello":"world"}`)
	secret := "secret"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !verifySignature(body, secret, sig) {
		t.Fatalf("expected signature to verify")
	}
	if verifySignature(body, secret, "sha256=deadbeef") {
		t.Fatalf("bad signature should not verify")
	}
	if verifySignature(body, "", sig) {
		t.Fatalf("empty secret must not verify")
	}
}

func TestPushEventCarriesRepoWorkspace(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db)
	t.Cleanup(func() { st.Close() })

	repo := fleet.Repo{
		WorkspaceID: "team-a",
		Name:        "owner/repo",
		Enabled:     true,
	}
	agents := []fleet.Agent{{
		Name:        "reviewer",
		Backend:     "claude",
		PromptRef:   "reviewer",
		Description: "Reviews repository events",
	}}
	backends := map[string]fleet.Backend{"claude": {Command: "claude"}}
	if _, err := st.UpsertPrompt(fleet.Prompt{Name: "reviewer", Content: "Review events."}); err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
	if err := st.ImportAll(agents, []fleet.Repo{repo}, nil, backends, nil, nil); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	dc := workflow.NewDataChannels(1, st)
	h := NewHandler(NewDeliveryStore(10*time.Minute), dc, st, nil, config.HTTPConfig{}, config.SelfImprovementConfig{}, zerolog.Nop())
	body := []byte(`{
		"ref":"refs/heads/main",
		"after":"0123456789012345678901234567890123456789",
		"repository":{"full_name":"owner/repo"},
		"sender":{"login":"maintainer"}
	}`)
	w := httptest.NewRecorder()

	h.handlePushEvent(context.Background(), w, body, "delivery-1")

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	select {
	case queued := <-dc.EventChan():
		if queued.Event.WorkspaceID != "team-a" {
			t.Fatalf("WorkspaceID = %q, want team-a", queued.Event.WorkspaceID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for queued event")
	}
}

func TestWebhookRepoPrefersEnabledWorkspaceOverDisabledDefault(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db)
	t.Cleanup(func() { st.Close() })

	if _, err := store.UpsertWorkspace(db, fleet.Workspace{ID: "team-a", Name: "Team A"}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if err := st.UpsertRepo(fleet.Repo{
		WorkspaceID: fleet.DefaultWorkspaceID,
		Name:        "owner/repo",
		Enabled:     false,
	}); err != nil {
		t.Fatalf("seed default repo: %v", err)
	}
	if err := st.UpsertRepo(fleet.Repo{
		WorkspaceID: "team-a",
		Name:        "owner/repo",
		Enabled:     true,
	}); err != nil {
		t.Fatalf("seed team repo: %v", err)
	}

	dc := workflow.NewDataChannels(1, st)
	h := NewHandler(NewDeliveryStore(10*time.Minute), dc, st, nil, config.HTTPConfig{}, config.SelfImprovementConfig{}, zerolog.Nop())
	body := []byte(`{
		"action":"created",
		"comment":{"body":"continue"},
		"issue":{"number":7},
		"repository":{"full_name":"owner/repo"},
		"sender":{"login":"maintainer"}
	}`)
	w := httptest.NewRecorder()

	h.handleIssueCommentEvent(context.Background(), w, body, "delivery-1")

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	select {
	case queued := <-dc.EventChan():
		if queued.Event.WorkspaceID != "team-a" {
			t.Fatalf("WorkspaceID = %q, want team-a", queued.Event.WorkspaceID)
		}
		if queued.Event.Kind != "issue_comment.created" || queued.Event.Number != 7 {
			t.Fatalf("event = %+v, want issue_comment.created #7", queued.Event)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for queued event")
	}
}

func TestWebhookFansOutToEveryEnabledWorkspaceRepo(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db)
	t.Cleanup(func() { st.Close() })

	if _, err := store.UpsertWorkspace(db, fleet.Workspace{ID: "team-a", Name: "Team A"}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	for _, repo := range []fleet.Repo{
		{WorkspaceID: fleet.DefaultWorkspaceID, Name: "owner/repo", Enabled: true},
		{WorkspaceID: "team-a", Name: "owner/repo", Enabled: true},
	} {
		if err := st.UpsertRepo(repo); err != nil {
			t.Fatalf("seed repo %s/%s: %v", repo.WorkspaceID, repo.Name, err)
		}
	}

	dc := workflow.NewDataChannels(2, st)
	h := NewHandler(NewDeliveryStore(10*time.Minute), dc, st, nil, config.HTTPConfig{}, config.SelfImprovementConfig{}, zerolog.Nop())
	body := []byte(`{
		"action":"created",
		"comment":{"body":"continue"},
		"issue":{"number":7},
		"repository":{"full_name":"owner/repo"},
		"sender":{"login":"maintainer"}
	}`)
	w := httptest.NewRecorder()

	h.handleIssueCommentEvent(context.Background(), w, body, "delivery-1")

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	got := map[string]bool{}
	for range 2 {
		select {
		case queued := <-dc.EventChan():
			got[queued.Event.WorkspaceID] = true
			if queued.Event.Kind != "issue_comment.created" || queued.Event.Number != 7 {
				t.Fatalf("event = %+v, want issue_comment.created #7", queued.Event)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timed out waiting for queued event")
		}
	}
	for _, workspaceID := range []string{fleet.DefaultWorkspaceID, "team-a"} {
		if !got[workspaceID] {
			t.Fatalf("missing queued event for workspace %q; got %+v", workspaceID, got)
		}
	}
}

func TestIssueCommentAIImprovementFeedbackStoredForAllowlistedAuthor(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db)
	t.Cleanup(func() { st.Close() })
	if _, err := store.UpsertWorkspace(db, fleet.Workspace{ID: "team-a", Name: "Team A"}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if err := st.UpsertRepo(fleet.Repo{WorkspaceID: "team-a", Name: "owner/repo", Enabled: true}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	dc := workflow.NewDataChannels(1, st)
	h := NewHandler(
		NewDeliveryStore(10*time.Minute),
		dc,
		st,
		nil,
		config.HTTPConfig{},
		config.SelfImprovementConfig{FeedbackAuthorAllowlist: []string{"maintainer"}},
		zerolog.Nop(),
	)
	h.WithImprovementAnalyzer(fakeImprovementAnalyzer{store: st})
	body := []byte(`{
		"action":"created",
		"comment":{"id":123,"html_url":"https://github.com/owner/repo/issues/7#issuecomment-123","body":"Please remember this /agents improve","created_at":"2026-05-30T10:00:00Z","updated_at":"2026-05-30T10:00:00Z"},
		"issue":{"number":7},
		"repository":{"full_name":"owner/repo"},
		"sender":{"login":"maintainer"}
	}`)
	w := httptest.NewRecorder()

	h.handleIssueCommentEvent(context.Background(), w, body, "delivery-1")

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	rows, err := st.ListSelfImprovementFeedback("team-a", store.FeedbackStatusAnalyzed, 10)
	if err != nil {
		t.Fatalf("list feedback: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("feedback count = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.SourceType != "issue_comment" || got.GitHubCommentID != 123 || !got.AuthorAuthorized || got.Status != store.FeedbackStatusAnalyzed {
		t.Fatalf("feedback = %+v, want authorized analyzed issue comment 123", got)
	}
	if got.IssueNumber != 7 || got.PRNumber != 0 || got.LinkConfidence != "unresolved" {
		t.Fatalf("feedback context = %+v, want issue #7 unresolved", got)
	}
	recs, err := st.ListSelfImprovementRecommendations("team-a", "", 10)
	if err != nil {
		t.Fatalf("list recommendations: %v", err)
	}
	if len(recs) != 1 || recs[0].FeedbackEventID != got.ID || recs[0].Status != store.RecommendationStatusNeedsUserInput {
		t.Fatalf("recommendations = %+v, want one review-only recommendation for feedback", recs)
	}
}

func TestAIImprovementFeedbackUnauthorizedAuthorIsIgnored(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db)
	t.Cleanup(func() { st.Close() })
	if err := st.UpsertRepo(fleet.Repo{Name: "owner/repo", Enabled: true}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	dc := workflow.NewDataChannels(1, st)
	h := NewHandler(
		NewDeliveryStore(10*time.Minute),
		dc,
		st,
		nil,
		config.HTTPConfig{},
		config.SelfImprovementConfig{FeedbackAuthorAllowlist: []string{"maintainer"}},
		zerolog.Nop(),
	)
	body := []byte(`{
		"action":"created",
		"comment":{"id":124,"body":"Please remember this /agents improve"},
		"issue":{"number":7},
		"repository":{"full_name":"owner/repo"},
		"sender":{"login":"drive-by"}
	}`)
	w := httptest.NewRecorder()

	h.handleIssueCommentEvent(context.Background(), w, body, "delivery-1")

	rows, err := st.ListSelfImprovementFeedback("", "", 10)
	if err != nil {
		t.Fatalf("list feedback: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("feedback count = %d, want 1", len(rows))
	}
	if rows[0].AuthorAuthorized || rows[0].Status != "ignored" {
		t.Fatalf("feedback = %+v, want unauthorized ignored", rows[0])
	}
}

func TestEditedIssueCommentWithoutAIImprovementTagIgnoresExistingFeedback(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db)
	t.Cleanup(func() { st.Close() })
	if err := st.UpsertRepo(fleet.Repo{Name: "owner/repo", Enabled: true}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	dc := workflow.NewDataChannels(1, st)
	h := NewHandler(
		NewDeliveryStore(10*time.Minute),
		dc,
		st,
		nil,
		config.HTTPConfig{},
		config.SelfImprovementConfig{FeedbackAuthorAllowlist: []string{"maintainer"}},
		zerolog.Nop(),
	)
	created := []byte(`{
		"action":"created",
		"comment":{"id":125,"body":"Please remember this /agents improve"},
		"issue":{"number":7},
		"repository":{"full_name":"owner/repo"},
		"sender":{"login":"maintainer"}
	}`)
	h.handleIssueCommentEvent(context.Background(), httptest.NewRecorder(), created, "delivery-1")

	edited := []byte(`{
		"action":"edited",
		"comment":{"id":125,"body":"Please disregard this"},
		"issue":{"number":7},
		"repository":{"full_name":"owner/repo"},
		"sender":{"login":"maintainer"}
	}`)
	w := httptest.NewRecorder()
	h.handleIssueCommentEvent(context.Background(), w, edited, "delivery-2")

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	rows, err := st.ListSelfImprovementFeedback("", "", 10)
	if err != nil {
		t.Fatalf("list feedback: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("feedback count = %d, want 1", len(rows))
	}
	if rows[0].Status != store.FeedbackStatusIgnored || rows[0].RawBody != "Please disregard this" {
		t.Fatalf("feedback = %+v, want ignored row with edited body", rows[0])
	}
}

func TestImproveCommandIgnoresFencedCodeBlocks(t *testing.T) {
	t.Parallel()
	body := "```text\n/agents improve\n```\noutside"
	if containsImproveCommand(body) {
		t.Fatalf("command inside fenced code block should be ignored")
	}
	if !containsImproveCommand("outside /agents improve.") {
		t.Fatalf("command outside code block should be detected")
	}
	if containsImproveCommand("outside /agents analyze") {
		t.Fatalf("different slash command should not be detected")
	}
}

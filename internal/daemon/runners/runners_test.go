package runners_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/daemon/runners"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// fixture wires the handler against a tempdir SQLite + a real
// DataChannels + a real observe.Store so the round-trip exercises the
// same code paths production runs.
type fixture struct {
	store    *store.Store
	channels *workflow.DataChannels
	observe  *observe.Store
	handler  *runners.Handler
	router   *mux.Router
}

func newFixture(t *testing.T, buffer int) *fixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db)
	t.Cleanup(func() { st.Close() })

	dc := workflow.NewDataChannels(buffer, st)
	obs := observe.NewStore(st.DB())
	h := runners.New(st, dc, obs, zerolog.Nop())
	r := mux.NewRouter()
	h.RegisterRoutes(r, func(next http.Handler) http.Handler { return next })
	return &fixture{store: st, channels: dc, observe: obs, handler: h, router: r}
}

func TestList_InFlightEventsShowAsSingleRow(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 16)
	id1, _ := fx.channels.PushEvent(context.Background(), workflow.Event{
		ID: "ev-1", Kind: "issues.labeled", Number: 1, Repo: workflow.RepoRef{FullName: "a/r"},
	})
	id2, _ := fx.channels.PushEvent(context.Background(), workflow.Event{
		ID: "ev-2", Kind: "pull_request.opened", Number: 2, Repo: workflow.RepoRef{FullName: "a/r"},
	})

	req := httptest.NewRequest(http.MethodGet, "/runners", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp runners.ListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("total = %d, want 2", resp.Total)
	}
	if len(resp.Runners) != 2 || resp.Runners[0].ID != id2 || resp.Runners[1].ID != id1 {
		t.Fatalf("runners = %+v, want newest-first [%d %d]", resp.Runners, id2, id1)
	}
	// Both rows are in-flight (no traces) → status=enqueued, agent empty.
	for _, row := range resp.Runners {
		if row.Status != "enqueued" {
			t.Errorf("id=%d status=%q, want enqueued", row.ID, row.Status)
		}
		if row.Agent != "" {
			t.Errorf("id=%d agent=%q, want empty (no trace yet)", row.ID, row.Agent)
		}
	}
}

func TestList_CompletedEventFannedOutToTwoAgentsShowsTwoRows(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 16)
	id, _ := fx.channels.PushEvent(context.Background(), workflow.Event{
		ID: "ev-fan", Kind: "issues.labeled", Number: 7, Repo: workflow.RepoRef{FullName: "a/r"},
	})
	<-fx.channels.EventChan()
	_ = fx.store.MarkEventStarted(id)

	// Record two completed trace spans for this event, simulates fanout.
	now := time.Now()
	fx.observe.RecordSpan(workflow.SpanInput{
		SpanID: "sp-A", RootEventID: "ev-fan",
		Agent: "coder", Backend: "claude", Repo: "a/r", EventKind: "issues.labeled",
		Number: 7, Summary: "coder ran",
		StartedAt: now, FinishedAt: now.Add(time.Second),
		Status: "success",
	})
	fx.observe.RecordSpan(workflow.SpanInput{
		SpanID: "sp-B", RootEventID: "ev-fan",
		Agent: "reviewer", Backend: "claude", Repo: "a/r", EventKind: "issues.labeled",
		Number: 7, Summary: "reviewer failed",
		StartedAt: now, FinishedAt: now.Add(2 * time.Second),
		Status: "error",
	})
	// RecordSpan persists asynchronously into SQLite; poll until both
	// rows are visible before asking the handler to JOIN.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fx.observe.TracesByRootEventID("ev-fan")) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = fx.store.MarkEventCompleted(id)

	req := httptest.NewRequest(http.MethodGet, "/runners", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp runners.ListResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Runners) != 2 {
		t.Fatalf("rows = %d, want 2 (one per fanned-out agent)", len(resp.Runners))
	}
	agents := map[string]runners.RunnerRow{}
	for _, r := range resp.Runners {
		agents[r.Agent] = r
		if r.ID != id {
			t.Errorf("agent=%q id=%d, want %d (same event_queue row)", r.Agent, r.ID, id)
		}
	}
	if agents["coder"].Status != "success" {
		t.Errorf("coder status = %q, want success", agents["coder"].Status)
	}
	if agents["reviewer"].Status != "error" {
		t.Errorf("reviewer status = %q, want error", agents["reviewer"].Status)
	}
	if agents["coder"].RunDuration != 1000 {
		t.Errorf("coder duration = %d, want 1000", agents["coder"].RunDuration)
	}
	if agents["reviewer"].RunDuration != 2000 {
		t.Errorf("reviewer duration = %d, want 2000", agents["reviewer"].RunDuration)
	}
}

func TestList_CompletedEventWithNoTracesIsHidden(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 16)
	// Webhook with no matching binding → event flows through, completes,
	// but no spans recorded. Should not appear in /runners.
	id, _ := fx.channels.PushEvent(context.Background(), workflow.Event{
		ID: "ev-empty", Kind: "issues.opened",
	})
	<-fx.channels.EventChan()
	_ = fx.store.MarkEventStarted(id)
	_ = fx.store.MarkEventCompleted(id)

	req := httptest.NewRequest(http.MethodGet, "/runners", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	var resp runners.ListResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Runners) != 0 {
		t.Fatalf("rows = %+v, want 0 (completed event with no traces should be hidden)", resp.Runners)
	}
	// But the queue row itself is still counted in Total, paging is on
	// event rows, not output rows.
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1", resp.Total)
	}
}

func TestList_FilterByStatus(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 16)
	idEnq, _ := fx.channels.PushEvent(context.Background(), workflow.Event{ID: "ev-q", Kind: "a"})
	idDone, _ := fx.channels.PushEvent(context.Background(), workflow.Event{ID: "ev-d", Kind: "b"})
	if err := fx.store.MarkEventCompleted(idDone); err != nil {
		t.Fatalf("mark: %v", err)
	}
	_ = idEnq

	req := httptest.NewRequest(http.MethodGet, "/runners?status=completed", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	var resp runners.ListResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	// idDone has no traces → 0 rows returned, but Total = 1 (event_queue
	// rows in completed state). Operator paging is on events.
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1", resp.Total)
	}
	if len(resp.Runners) != 0 {
		t.Errorf("rows = %d, want 0 (completed event with no traces hidden)", len(resp.Runners))
	}
}

func TestList_RejectsBadStatus(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 1)
	req := httptest.NewRequest(http.MethodGet, "/runners?status=garbage", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDelete_RemovesRow(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 16)
	id, _ := fx.channels.PushEvent(context.Background(), workflow.Event{ID: "ev-x", Kind: "x"})

	req := httptest.NewRequest(http.MethodDelete, "/runners/"+strconv.FormatInt(id, 10), nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if _, err := fx.store.GetRunner(id); err == nil {
		t.Errorf("row still present after delete")
	}
}

func TestDelete_MissingReturns404(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 1)
	req := httptest.NewRequest(http.MethodDelete, "/runners/9999", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRetry_CompletedEventReinserts(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 16)
	id, _ := fx.channels.PushEvent(context.Background(), workflow.Event{
		ID: "ev-retry", Kind: "issues.labeled", Number: 42,
		Repo: workflow.RepoRef{FullName: "owner/repo", Enabled: true},
	})
	// Drain the channel so retry's PushEvent doesn't trip ErrEventQueueFull.
	<-fx.channels.EventChan()
	_ = fx.store.MarkEventStarted(id)
	_ = fx.store.MarkEventCompleted(id)

	req := httptest.NewRequest(http.MethodPost, "/runners/"+strconv.FormatInt(id, 10)+"/retry", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp runners.RetryResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.NewID == 0 || resp.NewID == id {
		t.Fatalf("new id = %d, want a fresh row id distinct from %d", resp.NewID, id)
	}

	// The new row must be the one currently sitting on the channel.
	select {
	case qe := <-fx.channels.EventChan():
		if qe.ID != resp.NewID {
			t.Errorf("dequeued id = %d, want %d", qe.ID, resp.NewID)
		}
		if qe.Event.Kind != "issues.labeled" || qe.Event.Number != 42 {
			t.Errorf("dequeued event = %+v, want kind/number preserved", qe.Event)
		}
		if qe.Event.EnqueuedAt.IsZero() {
			t.Errorf("EnqueuedAt is zero, want a fresh stamp")
		}
	default:
		t.Fatal("expected retried event on channel")
	}
}

func TestRetry_RunningReturns409(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 16)
	id, _ := fx.channels.PushEvent(context.Background(), workflow.Event{ID: "ev-r", Kind: "x"})
	<-fx.channels.EventChan()
	_ = fx.store.MarkEventStarted(id)

	req := httptest.NewRequest(http.MethodPost, "/runners/"+strconv.FormatInt(id, 10)+"/retry", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestRetry_MissingReturns404(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 1)
	req := httptest.NewRequest(http.MethodPost, "/runners/9999/retry", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

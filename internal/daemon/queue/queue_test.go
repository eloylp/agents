package queue_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/daemon/queue"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// fixture wires the handler against a tempdir SQLite + a real
// DataChannels so the round-trip exercises the same code paths
// production runs.
type fixture struct {
	store    *store.Store
	channels *workflow.DataChannels
	handler  *queue.Handler
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
	h := queue.New(st, dc, zerolog.Nop())
	r := mux.NewRouter()
	h.RegisterRoutes(r, func(next http.Handler) http.Handler { return next })
	return &fixture{store: st, channels: dc, handler: h, router: r}
}

func TestList_ReturnsRowsNewestFirst(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 16)
	id1, _ := fx.channels.PushEvent(context.Background(), workflow.Event{Kind: "issues.labeled", Number: 1, Repo: workflow.RepoRef{FullName: "a/r"}})
	id2, _ := fx.channels.PushEvent(context.Background(), workflow.Event{Kind: "pull_request.opened", Number: 2, Repo: workflow.RepoRef{FullName: "a/r"}})

	req := httptest.NewRequest(http.MethodGet, "/queue", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp queue.ListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("total = %d, want 2", resp.Total)
	}
	if len(resp.Events) != 2 || resp.Events[0].ID != id2 || resp.Events[1].ID != id1 {
		t.Fatalf("events = %+v, want newest-first [%d %d]", resp.Events, id2, id1)
	}
	if resp.Events[0].Status != store.QueueEventEnqueued {
		t.Errorf("status = %q, want enqueued", resp.Events[0].Status)
	}
}

func TestList_FilterByStatus(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 16)
	idEnq, _ := fx.channels.PushEvent(context.Background(), workflow.Event{Kind: "a"})
	idDone, _ := fx.channels.PushEvent(context.Background(), workflow.Event{Kind: "b"})
	if err := fx.store.MarkEventCompleted(idDone); err != nil {
		t.Fatalf("mark: %v", err)
	}
	_ = idEnq

	req := httptest.NewRequest(http.MethodGet, "/queue?status=completed", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp queue.ListResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Events) != 1 || resp.Events[0].ID != idDone {
		t.Fatalf("events = %+v, want [%d]", resp.Events, idDone)
	}
}

func TestList_RejectsBadStatus(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 1)
	req := httptest.NewRequest(http.MethodGet, "/queue?status=garbage", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDelete_RemovesRow(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 16)
	id, _ := fx.channels.PushEvent(context.Background(), workflow.Event{Kind: "x"})

	req := httptest.NewRequest(http.MethodDelete, "/queue/"+strconv.FormatInt(id, 10), nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if _, err := fx.store.GetQueueEvent(id); err == nil {
		t.Errorf("row still present after delete")
	}
}

func TestDelete_MissingReturns404(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 1)
	req := httptest.NewRequest(http.MethodDelete, "/queue/9999", nil)
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
		Kind:   "issues.labeled",
		Number: 42,
		Repo:   workflow.RepoRef{FullName: "owner/repo", Enabled: true},
	})
	// Drain the channel so retry's PushEvent doesn't trip ErrEventQueueFull.
	<-fx.channels.EventChan()
	if err := fx.store.MarkEventStarted(id); err != nil {
		t.Fatalf("started: %v", err)
	}
	if err := fx.store.MarkEventCompleted(id); err != nil {
		t.Fatalf("completed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/queue/"+strconv.FormatInt(id, 10)+"/retry", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp queue.RetryResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
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
	id, _ := fx.channels.PushEvent(context.Background(), workflow.Event{Kind: "x"})
	<-fx.channels.EventChan()
	if err := fx.store.MarkEventStarted(id); err != nil {
		t.Fatalf("started: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/queue/"+strconv.FormatInt(id, 10)+"/retry", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestRetry_MissingReturns404(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, 1)
	req := httptest.NewRequest(http.MethodPost, "/queue/9999/retry", nil)
	rec := httptest.NewRecorder()
	fx.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eloylp/agents/internal/store"
)

// TestEnqueueEventReturnsIncrementingID verifies the row id sequence the
// in-memory channel relies on for MarkEventStarted/Completed.
func TestEnqueueEventReturnsIncrementingID(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	st := store.New(db)

	id1, err := st.EnqueueEvent(`{"kind":"issues.labeled"}`)
	if err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}
	id2, err := st.EnqueueEvent(`{"kind":"pull_request.opened"}`)
	if err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}
	if id2 <= id1 {
		t.Fatalf("expected monotonically increasing ids, got %d then %d", id1, id2)
	}
}

// TestPendingEventIDsReturnsRowsWithoutCompletedAt covers the startup
// replay path: newly enqueued rows are returned in insertion order;
// rows marked completed are skipped.
func TestPendingEventIDsReturnsRowsWithoutCompletedAt(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	st := store.New(db)

	idA, _ := st.EnqueueEvent(`{"kind":"a"}`)
	idB, _ := st.EnqueueEvent(`{"kind":"b"}`)
	idC, _ := st.EnqueueEvent(`{"kind":"c"}`)

	// Mark B completed — A and C must remain pending.
	if err := st.MarkEventCompleted(idB); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	pending, err := st.PendingEventIDs()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 2 || pending[0] != idA || pending[1] != idC {
		t.Fatalf("pending = %v, want [%d %d]", pending, idA, idC)
	}
}

// TestPendingIncludesInterruptedRuns verifies that rows whose
// started_at is set but completed_at is still NULL come back in the
// pending list — that's the crash-recovery case for interrupted runs.
func TestPendingIncludesInterruptedRuns(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	st := store.New(db)

	id, _ := st.EnqueueEvent(`{"kind":"issues.labeled"}`)
	if err := st.MarkEventStarted(id); err != nil {
		t.Fatalf("mark started: %v", err)
	}

	pending, err := st.PendingEventIDs()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0] != id {
		t.Fatalf("pending = %v, want [%d]", pending, id)
	}
}

// TestReadQueuedEventReturnsBlob verifies the blob round-trip used by
// the daemon's replay step.
func TestReadQueuedEventReturnsBlob(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	st := store.New(db)

	const payload = `{"kind":"issues.labeled","number":42}`
	id, err := st.EnqueueEvent(payload)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	got, err := st.ReadQueuedEvent(id)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != payload {
		t.Fatalf("blob = %q, want %q", got, payload)
	}
}

// TestReadQueuedEventMissingReturnsErrEventNotFound covers the race
// where the row is pruned between PendingEventIDs and ReadQueuedEvent.
func TestReadQueuedEventMissingReturnsErrEventNotFound(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	st := store.New(db)

	if _, err := st.ReadQueuedEvent(99999); !errors.Is(err, store.ErrRunnerNotFound) {
		t.Fatalf("err = %v, want ErrEventNotFound", err)
	}
}

// TestDeleteRunnerRemovesRow verifies the cleanup path PushEvent
// uses when the in-memory channel rejects the queued event.
func TestDeleteRunnerRemovesRow(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	st := store.New(db)

	id, _ := st.EnqueueEvent(`{"kind":"x"}`)
	if err := st.DeleteRunner(id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.ReadQueuedEvent(id); !errors.Is(err, store.ErrRunnerNotFound) {
		t.Fatalf("read after delete: err = %v, want ErrEventNotFound", err)
	}
}

// TestDeleteCompletedEventsBeforePrunesOnlyCompletedRows verifies the
// TTL cleanup contract: only rows whose completed_at predates the
// cutoff are removed; pending and recent rows survive.
func TestDeleteCompletedEventsBeforePrunesOnlyCompletedRows(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	st := store.New(db)

	idOldDone, _ := st.EnqueueEvent(`{"kind":"old"}`)
	idRecentDone, _ := st.EnqueueEvent(`{"kind":"recent"}`)
	idPending, _ := st.EnqueueEvent(`{"kind":"pending"}`)

	if err := st.MarkEventCompleted(idOldDone); err != nil {
		t.Fatalf("mark old: %v", err)
	}
	if err := st.MarkEventCompleted(idRecentDone); err != nil {
		t.Fatalf("mark recent: %v", err)
	}

	// Backdate idOldDone so it's older than the cutoff.
	if _, err := db.Exec(
		"UPDATE event_queue SET completed_at = ? WHERE id = ?",
		time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339Nano), idOldDone,
	); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	n, err := st.DeleteCompletedEventsBefore(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows pruned = %d, want 1", n)
	}

	// idOldDone gone, idRecentDone and idPending remain.
	if _, err := st.ReadQueuedEvent(idOldDone); !errors.Is(err, store.ErrRunnerNotFound) {
		t.Fatalf("old still present: err = %v", err)
	}
	if _, err := st.ReadQueuedEvent(idRecentDone); err != nil {
		t.Fatalf("recent missing: %v", err)
	}
	if _, err := st.ReadQueuedEvent(idPending); err != nil {
		t.Fatalf("pending missing: %v", err)
	}
}

// TestListRunnersReturnsParsedRows seeds three rows in distinct
// states and asserts the listing decodes status + blob fields.
func TestListRunnersReturnsParsedRows(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	st := store.New(db)

	idEnq, _ := st.EnqueueEvent(`{"Kind":"issues.labeled","Number":1,"Repo":{"FullName":"owner/repo"}}`)
	idRun, _ := st.EnqueueEvent(`{"Kind":"pull_request.opened","Number":2,"Repo":{"FullName":"owner/repo"}}`)
	idDone, _ := st.EnqueueEvent(`{"Kind":"push","Number":0,"Repo":{"FullName":"owner/repo2"}}`)
	if err := st.MarkEventStarted(idRun); err != nil {
		t.Fatalf("mark run: %v", err)
	}
	if err := st.MarkEventCompleted(idDone); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	all, err := st.ListRunners("", 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len(all) = %d, want 3", len(all))
	}
	// Newest first.
	if all[0].ID != idDone || all[1].ID != idRun || all[2].ID != idEnq {
		t.Fatalf("ordering = [%d %d %d], want [%d %d %d]",
			all[0].ID, all[1].ID, all[2].ID, idDone, idRun, idEnq)
	}

	wantStatus := map[int64]store.RunnerStatus{
		idEnq:  store.RunnerEnqueued,
		idRun:  store.RunnerRunning,
		idDone: store.RunnerCompleted,
	}
	for _, r := range all {
		if r.Status != wantStatus[r.ID] {
			t.Errorf("id=%d status=%q want %q", r.ID, r.Status, wantStatus[r.ID])
		}
	}

	// Partial decode worked.
	for _, r := range all {
		if r.Repo == "" {
			t.Errorf("id=%d repo empty after partial decode", r.ID)
		}
	}
}

// TestListRunnersFilterByStatus exercises the WHERE branches.
func TestListRunnersFilterByStatus(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	st := store.New(db)

	idEnq, _ := st.EnqueueEvent(`{}`)
	idRun, _ := st.EnqueueEvent(`{}`)
	idDone, _ := st.EnqueueEvent(`{}`)
	_ = st.MarkEventStarted(idRun)
	_ = st.MarkEventCompleted(idDone)

	cases := []struct {
		status store.RunnerStatus
		wantID int64
	}{
		{store.RunnerEnqueued, idEnq},
		{store.RunnerRunning, idRun},
		{store.RunnerCompleted, idDone},
	}
	for _, tc := range cases {
		out, err := st.ListRunners(tc.status, 0, 0)
		if err != nil {
			t.Fatalf("list %s: %v", tc.status, err)
		}
		if len(out) != 1 || out[0].ID != tc.wantID {
			t.Errorf("status=%s got %v, want id=%d", tc.status, out, tc.wantID)
		}
	}
}

// TestCountRunnersMatchesList verifies CountRunners agrees with
// the listing under each filter.
func TestCountRunnersMatchesList(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	st := store.New(db)

	for range 3 {
		_, _ = st.EnqueueEvent(`{}`)
	}
	idDone, _ := st.EnqueueEvent(`{}`)
	_ = st.MarkEventCompleted(idDone)

	got, err := st.CountRunners("")
	if err != nil {
		t.Fatalf("count all: %v", err)
	}
	if got != 4 {
		t.Errorf("count all = %d, want 4", got)
	}
	gotDone, err := st.CountRunners(store.RunnerCompleted)
	if err != nil {
		t.Fatalf("count completed: %v", err)
	}
	if gotDone != 1 {
		t.Errorf("count completed = %d, want 1", gotDone)
	}
}

// TestGetRunnerReturnsRecord covers the by-id lookup the retry
// handler uses to validate a row exists before fetching its blob.
func TestGetRunnerReturnsRecord(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	st := store.New(db)

	id, _ := st.EnqueueEvent(`{"Kind":"issues.labeled","Number":7,"Repo":{"FullName":"owner/repo"}}`)
	rec, err := st.GetRunner(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.ID != id || rec.Kind != "issues.labeled" || rec.Number != 7 || rec.Repo != "owner/repo" {
		t.Fatalf("rec = %+v", rec)
	}
	if rec.Status != store.RunnerEnqueued {
		t.Errorf("status = %q, want enqueued", rec.Status)
	}

	if _, err := st.GetRunner(99999); !errors.Is(err, store.ErrRunnerNotFound) {
		t.Fatalf("err = %v, want ErrEventNotFound", err)
	}
}

// TestRunQueueCleanupReturnsOnContextCancel verifies the loop exits
// promptly when its context is cancelled — a producer-tier shutdown
// must not block on this consumer goroutine.
func TestRunQueueCleanupReturnsOnContextCancel(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	st := store.New(db)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- st.RunQueueCleanup(ctx, time.Hour, 10*time.Millisecond, nil)
	}()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunQueueCleanup returned %v, want nil on cancel", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunQueueCleanup did not return after cancel")
	}
}

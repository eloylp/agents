package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RunnerStatus is the derived state of an event_queue row, surfaced as
// "runner status" on the wire. The SQLite table is still called
// event_queue (one row per durable event), externally we frame each
// row as a runner because that's how operators think about a unit of
// work. A single event row may correspond to N agent runs (fanout); the
// REST handler does that JOIN with the traces table at read time.
type RunnerStatus string

const (
	RunnerEnqueued  RunnerStatus = "enqueued"
	RunnerRunning   RunnerStatus = "running"
	RunnerCompleted RunnerStatus = "completed"
)

// RunnerRecord is the wire shape of an event_queue row exposed to
// REST/MCP listings. Status is derived from the started_at/completed_at
// timestamps so callers don't have to interpret the nullable columns.
// Kind/Repo/Number/Actor/EventID/TargetAgent/Payload come from a partial
// decode of event_blob; callers that need the full event payload
// re-fetch with ReadQueuedEvent.
type RunnerRecord struct {
	ID          int64           `json:"id"`
	EventID     string          `json:"event_id"`
	Kind        string          `json:"kind"`
	Repo        string          `json:"repo"`
	Number      int             `json:"number"`
	Actor       string          `json:"actor,omitempty"`
	TargetAgent string          `json:"target_agent,omitempty"`
	Status      RunnerStatus    `json:"status"`
	EnqueuedAt  time.Time       `json:"enqueued_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

// ErrRunnerNotFound is returned when a lookup finds no event_queue row
// for the requested id. Distinct from a SQL error so callers can decide
// whether to log and continue (the row was cleaned up between scan and
// fetch) or surface as a hard failure.
var ErrRunnerNotFound = errors.New("store: runner not found")

// EnqueueEvent persists the JSON-serialised event blob and returns the
// auto-generated row id. The id flows through the in-memory channel to
// the worker, which uses it to mark the row started/completed.
func (s *Store) EnqueueEvent(blob string) (int64, error) {
	res, err := s.db.Exec(
		"INSERT INTO event_queue(event_blob, enqueued_at) VALUES (?, ?)",
		blob, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("store: enqueue event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: enqueue event: last id: %w", err)
	}
	return id, nil
}

// DeleteRunner removes an event_queue row by id. Used by PushEvent on
// channel-push failure (rollback) and by the runners DELETE handler.
func (s *Store) DeleteRunner(id int64) error {
	if _, err := s.db.Exec("DELETE FROM event_queue WHERE id = ?", id); err != nil {
		return fmt.Errorf("store: delete runner %d: %w", id, err)
	}
	return nil
}

// MarkEventStarted stamps started_at on the row with id. Workers call
// this when they pick an event off the channel.
func (s *Store) MarkEventStarted(id int64) error {
	_, err := s.db.Exec(
		"UPDATE event_queue SET started_at = ? WHERE id = ?",
		time.Now().UTC().Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return fmt.Errorf("store: mark event %d started: %w", id, err)
	}
	return nil
}

// MarkEventCompleted stamps completed_at on the row with id. Workers
// call this after HandleEvent returns (whether it succeeded or not, a
// failed run is still considered "done" from the queue's perspective;
// dropping the row prevents a replay loop on a deterministically failing
// event).
func (s *Store) MarkEventCompleted(id int64) error {
	_, err := s.db.Exec(
		"UPDATE event_queue SET completed_at = ? WHERE id = ?",
		time.Now().UTC().Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return fmt.Errorf("store: mark event %d completed: %w", id, err)
	}
	return nil
}

// PendingEventIDs returns the ids of every row whose completed_at is
// still NULL, in insertion order. Called once at daemon startup to
// replay events that were either never picked up by a worker
// (started_at NULL) or were picked up but interrupted by a crash
// (started_at set, completed_at NULL).
func (s *Store) PendingEventIDs() ([]int64, error) {
	rows, err := s.db.Query("SELECT id FROM event_queue WHERE completed_at IS NULL ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("store: scan pending events: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan pending event id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ReadQueuedEvent returns the JSON-serialised event blob for id, or
// ErrRunnerNotFound if the row has been deleted.
func (s *Store) ReadQueuedEvent(id int64) (string, error) {
	var blob string
	err := s.db.QueryRow("SELECT event_blob FROM event_queue WHERE id = ?", id).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrRunnerNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: read queued event %d: %w", id, err)
	}
	return blob, nil
}

// ListRunners returns event_queue rows in reverse-chronological order
// (newest first). When status is non-empty, only rows in that state are
// returned. limit and offset paginate; a non-positive limit is clamped
// to 100. The blob is partially decoded for kind / repo / number / actor
// / event_id / target_agent / payload so the UI can render a useful row
// without a second query.
func (s *Store) ListRunners(status RunnerStatus, limit, offset int) ([]RunnerRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	where, args := runnerStatusFilter(status)
	q := "SELECT id, event_blob, enqueued_at, started_at, completed_at FROM event_queue " +
		where + " ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list runners: %w", err)
	}
	defer rows.Close()
	out := make([]RunnerRecord, 0)
	for rows.Next() {
		rec, err := scanRunnerRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// CountRunners returns the number of rows matching status (empty status
// counts every row). Used by listings for client-side pagination.
func (s *Store) CountRunners(status RunnerStatus) (int, error) {
	where, args := runnerStatusFilter(status)
	q := "SELECT COUNT(*) FROM event_queue " + where
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count runners: %w", err)
	}
	return n, nil
}

// GetRunner returns one row by id. Returns ErrRunnerNotFound when
// missing, same error ReadQueuedEvent uses.
func (s *Store) GetRunner(id int64) (RunnerRecord, error) {
	row := s.db.QueryRow(
		"SELECT id, event_blob, enqueued_at, started_at, completed_at FROM event_queue WHERE id = ?",
		id,
	)
	rec, err := scanRunnerRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return RunnerRecord{}, ErrRunnerNotFound
	}
	return rec, err
}

// runnerStatusFilter maps a RunnerStatus to a SQL WHERE fragment and
// its argument list. Empty status returns no filter.
func runnerStatusFilter(status RunnerStatus) (string, []any) {
	switch status {
	case RunnerEnqueued:
		return "WHERE started_at IS NULL AND completed_at IS NULL", nil
	case RunnerRunning:
		return "WHERE started_at IS NOT NULL AND completed_at IS NULL", nil
	case RunnerCompleted:
		return "WHERE completed_at IS NOT NULL", nil
	}
	return "", nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows so scanRunnerRow
// can serve GetRunner and ListRunners from one helper.
type scanner interface {
	Scan(dest ...any) error
}

func scanRunnerRow(s scanner) (RunnerRecord, error) {
	var (
		id              int64
		blob            string
		enq             string
		started, comple sql.NullString
	)
	if err := s.Scan(&id, &blob, &enq, &started, &comple); err != nil {
		return RunnerRecord{}, err
	}
	rec := RunnerRecord{ID: id}
	if t, err := time.Parse(time.RFC3339Nano, enq); err == nil {
		rec.EnqueuedAt = t
	}
	if started.Valid {
		if t, err := time.Parse(time.RFC3339Nano, started.String); err == nil {
			rec.StartedAt = &t
		}
	}
	if comple.Valid {
		if t, err := time.Parse(time.RFC3339Nano, comple.String); err == nil {
			rec.CompletedAt = &t
		}
	}
	switch {
	case rec.CompletedAt != nil:
		rec.Status = RunnerCompleted
	case rec.StartedAt != nil:
		rec.Status = RunnerRunning
	default:
		rec.Status = RunnerEnqueued
	}
	// Partial blob decode, keep the listing useful even if the payload
	// schema gets new fields. A malformed blob leaves Kind/Repo/Number
	// zero rather than failing the whole listing.
	var partial struct {
		ID      string          `json:"ID"`
		Kind    string          `json:"Kind"`
		Number  int             `json:"Number"`
		Actor   string          `json:"Actor"`
		Repo    struct {
			FullName string `json:"FullName"`
		} `json:"Repo"`
		Payload json.RawMessage `json:"Payload"`
	}
	if err := json.Unmarshal([]byte(blob), &partial); err == nil {
		rec.EventID = partial.ID
		rec.Kind = partial.Kind
		rec.Number = partial.Number
		rec.Actor = partial.Actor
		rec.Repo = partial.Repo.FullName
		rec.Payload = partial.Payload
		// target_agent is convention on the payload for cron / dispatch /
		// agents.run kinds; surface it as a typed field so the UI can show
		// it without re-parsing the blob.
		if len(partial.Payload) > 0 {
			var p map[string]any
			if json.Unmarshal(partial.Payload, &p) == nil {
				if s, ok := p["target_agent"].(string); ok {
					rec.TargetAgent = s
				}
			}
		}
	}
	return rec, nil
}

// DeleteCompletedEventsBefore prunes rows whose completed_at predates
// before. Used by the cleanup loop so the table doesn't grow without
// bound. Returns the number of rows removed.
func (s *Store) DeleteCompletedEventsBefore(before time.Time) (int64, error) {
	res, err := s.db.Exec(
		"DELETE FROM event_queue WHERE completed_at IS NOT NULL AND completed_at < ?",
		before.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("store: prune completed events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune completed events: rows affected: %w", err)
	}
	return n, nil
}

// RunQueueCleanup blocks until ctx is cancelled, periodically deleting
// completed event rows older than retain. The caller (typically the
// daemon's consumer errgroup) owns goroutine creation and waits on Run
// for clean shutdown. retain defaults to 7d when zero; interval
// defaults to 1h.
//
// A delete error logs through onErr (when provided) and the loop
// continues, a transient cleanup failure must not bring down the
// daemon, and the next tick retries.
func (s *Store) RunQueueCleanup(ctx context.Context, retain, interval time.Duration, onErr func(error)) error {
	if interval <= 0 {
		interval = time.Hour
	}
	if retain <= 0 {
		retain = 7 * 24 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := s.DeleteCompletedEventsBefore(time.Now().Add(-retain)); err != nil && onErr != nil {
				onErr(err)
			}
		}
	}
}

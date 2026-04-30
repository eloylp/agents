package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// QueueEventStatus is the derived state of an event_queue row.
type QueueEventStatus string

const (
	QueueEventEnqueued  QueueEventStatus = "enqueued"
	QueueEventRunning   QueueEventStatus = "running"
	QueueEventCompleted QueueEventStatus = "completed"
)

// QueueEventRecord is the wire shape of an event_queue row exposed to
// REST/MCP listings. Status is derived from the started_at/completed_at
// timestamps so callers don't have to interpret the nullable columns.
// The Repo/Kind/Number fields come from a partial decode of event_blob;
// callers that need the full Event payload re-fetch with ReadQueuedEvent.
type QueueEventRecord struct {
	ID          int64            `json:"id"`
	Kind        string           `json:"kind"`
	Repo        string           `json:"repo"`
	Number      int              `json:"number"`
	Status      QueueEventStatus `json:"status"`
	EnqueuedAt  time.Time        `json:"enqueued_at"`
	StartedAt   *time.Time       `json:"started_at,omitempty"`
	CompletedAt *time.Time       `json:"completed_at,omitempty"`
}

// ErrEventNotFound is returned when ReadQueuedEvent cannot find a row for
// the requested id. Distinct from a SQL error so callers can decide
// whether to log and continue (the row was cleaned up between scan and
// fetch) or surface as a hard failure.
var ErrEventNotFound = errors.New("store: queued event not found")

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

// DeleteQueuedEvent removes a row by id. Used by PushEvent when the
// channel push fails — keeps SQLite in sync with what the runtime
// actually accepted.
func (s *Store) DeleteQueuedEvent(id int64) error {
	if _, err := s.db.Exec("DELETE FROM event_queue WHERE id = ?", id); err != nil {
		return fmt.Errorf("store: delete queued event %d: %w", id, err)
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
// call this after HandleEvent returns (whether it succeeded or not — a
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
// ErrEventNotFound if the row has been deleted.
func (s *Store) ReadQueuedEvent(id int64) (string, error) {
	var blob string
	err := s.db.QueryRow("SELECT event_blob FROM event_queue WHERE id = ?", id).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrEventNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: read queued event %d: %w", id, err)
	}
	return blob, nil
}

// ListQueueEvents returns event_queue rows in reverse-chronological
// order (newest first). When status is non-empty, only rows in that
// state are returned. limit and offset paginate; a non-positive limit
// is clamped to 100. The blob is partially decoded for kind / repo /
// number so the UI can render a useful row without a second query.
func (s *Store) ListQueueEvents(status QueueEventStatus, limit, offset int) ([]QueueEventRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	where, args := queueStatusFilter(status)
	q := "SELECT id, event_blob, enqueued_at, started_at, completed_at FROM event_queue " +
		where + " ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list queue events: %w", err)
	}
	defer rows.Close()
	out := make([]QueueEventRecord, 0)
	for rows.Next() {
		rec, err := scanQueueRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// CountQueueEvents returns the number of rows matching status (empty
// status counts every row). Used by listings for client-side pagination.
func (s *Store) CountQueueEvents(status QueueEventStatus) (int, error) {
	where, args := queueStatusFilter(status)
	q := "SELECT COUNT(*) FROM event_queue " + where
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count queue events: %w", err)
	}
	return n, nil
}

// GetQueueEvent returns one row by id. Returns ErrEventNotFound when
// missing — same error ReadQueuedEvent uses.
func (s *Store) GetQueueEvent(id int64) (QueueEventRecord, error) {
	row := s.db.QueryRow(
		"SELECT id, event_blob, enqueued_at, started_at, completed_at FROM event_queue WHERE id = ?",
		id,
	)
	rec, err := scanQueueRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return QueueEventRecord{}, ErrEventNotFound
	}
	return rec, err
}

// queueStatusFilter maps a QueueEventStatus to a SQL WHERE fragment and
// its argument list. Empty status returns no filter.
func queueStatusFilter(status QueueEventStatus) (string, []any) {
	switch status {
	case QueueEventEnqueued:
		return "WHERE started_at IS NULL AND completed_at IS NULL", nil
	case QueueEventRunning:
		return "WHERE started_at IS NOT NULL AND completed_at IS NULL", nil
	case QueueEventCompleted:
		return "WHERE completed_at IS NOT NULL", nil
	}
	return "", nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows so scanQueueRow
// can serve GetQueueEvent and ListQueueEvents from one helper.
type scanner interface {
	Scan(dest ...any) error
}

func scanQueueRow(s scanner) (QueueEventRecord, error) {
	var (
		id              int64
		blob            string
		enq             string
		started, comple sql.NullString
	)
	if err := s.Scan(&id, &blob, &enq, &started, &comple); err != nil {
		return QueueEventRecord{}, err
	}
	rec := QueueEventRecord{ID: id}
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
		rec.Status = QueueEventCompleted
	case rec.StartedAt != nil:
		rec.Status = QueueEventRunning
	default:
		rec.Status = QueueEventEnqueued
	}
	// Partial blob decode — keep the listing useful even if the payload
	// schema gets new fields. A malformed blob leaves Kind/Repo/Number
	// zero rather than failing the whole listing.
	var partial struct {
		Kind   string `json:"Kind"`
		Number int    `json:"Number"`
		Repo   struct {
			FullName string `json:"FullName"`
		} `json:"Repo"`
	}
	if err := json.Unmarshal([]byte(blob), &partial); err == nil {
		rec.Kind = partial.Kind
		rec.Number = partial.Number
		rec.Repo = partial.Repo.FullName
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
// continues — a transient cleanup failure must not bring down the
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

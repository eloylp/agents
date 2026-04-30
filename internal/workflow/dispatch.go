package workflow

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// EventEnqueuer can accept events for async processing.
// *DataChannels satisfies this interface.
type EventEnqueuer interface {
	PushEvent(ctx context.Context, ev Event) error
}

// DispatchStats is a snapshot of dispatch counters for reporting in /status.
type DispatchStats struct {
	RequestedTotal     int64 `json:"requested_total"`
	DroppedNoWhitelist int64 `json:"dropped_no_whitelist"`
	DroppedNoOptin     int64 `json:"dropped_no_optin"`
	DroppedSelf        int64 `json:"dropped_self"`
	DroppedDepth       int64 `json:"dropped_depth"`
	DroppedFanout      int64 `json:"dropped_fanout"`
	Deduped            int64 `json:"deduped"`
	Enqueued           int64 `json:"enqueued"`
	// RunsDeduped counts engine-level run invocations suppressed because an
	// identical (agent, repo, number) run was already in-flight or recently
	// completed within the dedup window. This covers webhook-triggered runs;
	// dispatch-enqueue dedup is counted separately in Deduped.
	RunsDeduped int64 `json:"runs_deduped"`
}

// dispatchCounters tracks aggregate dispatch statistics using atomic operations.
type dispatchCounters struct {
	requestedTotal     atomic.Int64
	droppedNoWhitelist atomic.Int64
	droppedNoOptin     atomic.Int64
	droppedSelf        atomic.Int64
	droppedDepth       atomic.Int64
	droppedFanout      atomic.Int64
	deduped            atomic.Int64
	enqueued           atomic.Int64
}

func (c *dispatchCounters) snapshot() DispatchStats {
	return DispatchStats{
		RequestedTotal:     c.requestedTotal.Load(),
		DroppedNoWhitelist: c.droppedNoWhitelist.Load(),
		DroppedNoOptin:     c.droppedNoOptin.Load(),
		DroppedSelf:        c.droppedSelf.Load(),
		DroppedDepth:       c.droppedDepth.Load(),
		DroppedFanout:      c.droppedFanout.Load(),
		Deduped:            c.deduped.Load(),
		Enqueued:           c.enqueued.Load(),
	}
}

// dispatchEntry records a dedup slot in the store.
// committed indicates whether the slot is backed by a real enqueued event.
// Pending (committed==false) entries block concurrent duplicate TryClaim calls
// but are invisible to DispatchAlreadyClaimed until committed.
type dispatchEntry struct {
	expiresAt time.Time
	committed bool
}

// DispatchDedupStore suppresses duplicate (target_agent, repo, number) dispatch
// requests within a configurable TTL window. It mirrors the shape of
// webhook.DeliveryStore.
type DispatchDedupStore struct {
	ttl              time.Duration
	mu               sync.Mutex
	entries          map[string]dispatchEntry
	cronRefCounts    map[string]int // active in-flight run count per cron key
	webhookRefCounts map[string]int // active in-flight run count per dispatch/webhook key
}

// NewDispatchDedupStore returns a store with the given TTL window in seconds.
func NewDispatchDedupStore(ttlSeconds int) *DispatchDedupStore {
	return &DispatchDedupStore{
		ttl:              time.Duration(ttlSeconds) * time.Second,
		entries:          make(map[string]dispatchEntry),
		cronRefCounts:    make(map[string]int),
		webhookRefCounts: make(map[string]int),
	}
}

// Start launches a background goroutine that periodically evicts expired entries.
func (s *DispatchDedupStore) Start(ctx context.Context) {
	if s.ttl <= 0 {
		return
	}
	go func() {
		tickInterval := max(s.ttl/4, time.Second)
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.evict(now)
			}
		}
	}()
}

func (s *DispatchDedupStore) evict(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range s.entries {
		// Do not evict entries whose run is still in flight: the refcount
		// tracks active callers that called MarkCronRun/MarkWebhookRunInFlight
		// but have not yet called the corresponding finalize/abandon method.
		// Evicting such an entry would allow TryClaimForDispatch to proceed
		// even though the run is still executing.
		if now.After(entry.expiresAt) && s.cronRefCounts[key] == 0 && s.webhookRefCounts[key] == 0 {
			delete(s.entries, key)
		}
	}
}

// dispatchStoreKey builds the map key for a dispatch-namespace entry.
func dispatchStoreKey(agent, repo string, number int) string {
	return fmt.Sprintf("%s\x00%s\x00%d", agent, repo, number)
}

// cronStoreKey builds the map key for a cron-namespace entry.
func cronStoreKey(agent, repo string, number int) string {
	return fmt.Sprintf("cron\x00%s\x00%s\x00%d", agent, repo, number)
}

// SeenOrAdd returns true if this (target, repo, number) combination has been
// seen within the TTL window (whether pending or committed), otherwise records
// it as a committed entry and returns false.
func (s *DispatchDedupStore) SeenOrAdd(target, repo string, number int, now time.Time) bool {
	key := dispatchStoreKey(target, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && now.Before(e.expiresAt) {
		return true
	}
	s.entries[key] = dispatchEntry{expiresAt: now.Add(s.ttl), committed: true}
	return false
}

// SeesPendingOrCommitted returns true if (target, repo, number) has any dispatch
// claim — pending or committed — within the TTL window. Pending (not-yet-committed)
// claims are included so that a TryClaim still in flight (PushEvent running) blocks
// concurrent cron/manual runs from also starting. This closes the race between a
// dispatch's TryClaim→CommitClaim window and an autonomous scheduler check.
func (s *DispatchDedupStore) SeesPendingOrCommitted(target, repo string, number int, now time.Time) bool {
	key := dispatchStoreKey(target, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	return ok && now.Before(e.expiresAt)
}

// TryClaim atomically reserves a dispatch dedup slot for (target, repo, number)
// if no pending or committed claim exists within the TTL window. Returns true if
// the reservation was created (caller may proceed to enqueue). Returns false if
// the slot is already held, preventing concurrent dispatchers from both
// proceeding past the dedup gate.
//
// A successful TryClaim creates a pending entry that blocks future TryClaim
// calls but is invisible to DispatchAlreadyClaimed until CommitClaim is
// called. On enqueue failure call AbandonClaim to release the pending slot.
func (s *DispatchDedupStore) TryClaim(target, repo string, number int, now time.Time) bool {
	key := dispatchStoreKey(target, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && now.Before(e.expiresAt) {
		return false
	}
	s.entries[key] = dispatchEntry{expiresAt: now.Add(s.ttl), committed: false}
	return true
}

// CommitClaim upgrades a pending claim to committed, making it visible to
// DispatchAlreadyClaimed. Must be called after a successful PushEvent.
func (s *DispatchDedupStore) CommitClaim(target, repo string, number int) {
	key := dispatchStoreKey(target, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok {
		e.committed = true
		s.entries[key] = e
	}
}

// AbandonClaim removes a pending (uncommitted) claim. Must be called when
// PushEvent fails so that the slot is released and future retries can proceed.
// It is a no-op if the entry has already been committed.
func (s *DispatchDedupStore) AbandonClaim(target, repo string, number int) {
	key := dispatchStoreKey(target, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && !e.committed {
		delete(s.entries, key)
	}
}

// MarkWebhookRunInFlight increments the in-flight reference count for the
// dispatch-namespace entry (agent, repo, number). It must be called immediately
// after a successful TryClaimForDispatch in the webhook/fanOut path so that the
// claim survives past the TTL window while the agent run is still executing.
// Callers must follow with FinalizeWebhookRun (success) or AbandonWebhookRun
// (failure) to release the marker.
func (s *DispatchDedupStore) MarkWebhookRunInFlight(agent, repo string, number int) {
	key := dispatchStoreKey(agent, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.webhookRefCounts[key]++
}

// FinalizeWebhookRun decrements the in-flight reference count for the
// dispatch-namespace entry (agent, repo, number) after a successful run.
// The TTL entry is preserved so TryClaimForDispatch continues to suppress
// duplicates until the dedup_window_seconds elapses. Once the refcount reaches
// zero, the evict() loop is free to remove the entry when expiresAt passes.
func (s *DispatchDedupStore) FinalizeWebhookRun(agent, repo string, number int) {
	key := dispatchStoreKey(agent, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.webhookRefCounts[key] <= 1 {
		delete(s.webhookRefCounts, key)
		// Leave the entry in s.entries so the TTL window remains active.
	} else {
		s.webhookRefCounts[key]--
	}
}

// AbandonWebhookRun decrements the in-flight reference count and removes the
// dispatch-namespace entry for (agent, repo, number) after a failed run. Used
// by the error and panic paths in fanOut so that a retry or a subsequent event
// for the same item can claim the slot and attempt the run again.
func (s *DispatchDedupStore) AbandonWebhookRun(agent, repo string, number int) {
	key := dispatchStoreKey(agent, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.webhookRefCounts[key] <= 1 {
		delete(s.webhookRefCounts, key)
		delete(s.entries, key)
	} else {
		s.webhookRefCounts[key]--
	}
}

// MarkCronRun records that a cron-fired execution has started
// for (agent, repo, number). The mark persists for the full TTL window and lives
// in a separate key namespace ("cron\x00…") from dispatch entries so that
// repeated cron runs are never suppressed by this mark — only dispatches are.
// Autonomous runs always pass number=0 because they are not tied to a specific
// issue or PR; this scoping ensures that a cron run for a repo-level context
// (number=0) does not suppress dispatches for unrelated items such as PR #42.
//
// An internal reference count is incremented on each call so that a rollback
// from one failed overlapping run does not clear a mark that a concurrently
// running autonomous pass is still holding.
func (s *DispatchDedupStore) MarkCronRun(agent, repo string, number int, now time.Time) {
	key := cronStoreKey(agent, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = dispatchEntry{expiresAt: now.Add(s.ttl), committed: true}
	s.cronRefCounts[key]++
}

// RemoveCronMark decrements the in-flight reference count for the cron-namespace
// entry (agent, repo, number) and deletes the entry from the store so that
// future dispatches are no longer suppressed. It is used by the rollback path
// (run failed before completing). When the count reaches zero, the entry is
// fully removed; otherwise only the count is decremented and the entry stays,
// ensuring a concurrent overlapping run's reservation is not cleared prematurely.
func (s *DispatchDedupStore) RemoveCronMark(agent, repo string, number int) {
	key := cronStoreKey(agent, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cronRefCounts[key] <= 1 {
		delete(s.entries, key)
		delete(s.cronRefCounts, key)
	} else {
		s.cronRefCounts[key]--
	}
}

// FinalizeCronMark decrements the in-flight reference count for the cron-namespace
// entry (agent, repo, number) without deleting the entry. It is used by the
// success path after a cron/manual run completes: the entry's expiresAt is kept
// in place so that TryClaimForDispatch continues to suppress autonomous-context
// dispatches until the full dedup_window_seconds elapses. Once the refcount
// reaches zero the evict() loop is free to remove the entry when expiresAt passes.
func (s *DispatchDedupStore) FinalizeCronMark(agent, repo string, number int) {
	key := cronStoreKey(agent, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cronRefCounts[key] <= 1 {
		delete(s.cronRefCounts, key)
		// Leave the entry in s.entries so the TTL window remains active.
	} else {
		s.cronRefCounts[key]--
	}
}

// SeenCronRun returns true if a cron or manual run has been recorded for
// (agent, repo, number) within the TTL window, or if the run is still
// in flight (cronRefCounts > 0). The refcount check ensures that a
// long-running autonomous pass (outlasting dedup_window_seconds) still
// blocks new dispatches even after its TTL entry would have expired.
func (s *DispatchDedupStore) SeenCronRun(agent, repo string, number int, now time.Time) bool {
	key := cronStoreKey(agent, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	return (ok && now.Before(e.expiresAt)) || s.cronRefCounts[key] > 0
}

// TryClaimForCron atomically checks whether a dispatch has already claimed
// the (agent, repo, number) slot and, if not, writes a cron-namespace mark.
// Returns true if the mark was written (caller may proceed with the run).
// Returns false if a dispatch claim — pending or committed — exists within
// the TTL window (caller should skip the run; dispatch-first ordering).
//
// Unlike the split DispatchAlreadyClaimed → MarkAutonomousRun sequence, this
// single-lock operation eliminates the TOCTOU window where the cron path
// could observe no dispatch claim and the dispatch path could observe no cron
// mark before either had written, allowing both to proceed concurrently.
//
// If the run fails before completing, call RemoveCronMark to release the mark
// so that future dispatches are not spuriously suppressed for the full TTL.
func (s *DispatchDedupStore) TryClaimForCron(agent, repo string, number int, now time.Time) bool {
	dispatchKey := dispatchStoreKey(agent, repo, number)
	cronKey := cronStoreKey(agent, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Block if dispatch has any claim (pending or committed) within the TTL.
	if e, ok := s.entries[dispatchKey]; ok && now.Before(e.expiresAt) {
		return false
	}
	// Block if a webhook/agents.run execution is still in flight past the TTL
	// window. Without this guard, a long-running run that outlasts
	// dedup_window_seconds would have an expired expiresAt while its refcount
	// is still positive, and TryClaimForCron would proceed concurrently,
	// breaking the fleet-wide dedup contract. TryClaimForDispatch has the same
	// guard for the symmetric case.
	if s.webhookRefCounts[dispatchKey] > 0 {
		return false
	}
	// Write the cron mark immediately so any dispatch arriving after this
	// point (including during backend/runner resolution) sees it and backs
	// off. The refcount allows concurrent autonomous passes to each hold a
	// reservation independently — a rollback from one does not clear a mark
	// that another is still holding.
	s.entries[cronKey] = dispatchEntry{expiresAt: now.Add(s.ttl), committed: true}
	s.cronRefCounts[cronKey]++
	return true
}

// TryClaimForDispatch atomically checks whether a cron/manual run is active
// for (agent, repo, number) and, if not, claims a pending dispatch slot.
// Returns true if the dispatch slot was claimed (caller may proceed to enqueue).
// Returns false if a cron mark exists or the dispatch slot is already taken.
//
// Unlike the split SeenCronRun → TryClaim sequence, this single-lock operation
// eliminates the TOCTOU window where the dispatch path could read no cron mark
// and the cron path could read no dispatch claim before either had written,
// allowing both to proceed concurrently.
//
// On success, caller must follow with CommitClaim (after a successful PushEvent)
// or AbandonClaim (on failure) to finalize the reservation.
func (s *DispatchDedupStore) TryClaimForDispatch(agent, repo string, number int, now time.Time) bool {
	cronKey := cronStoreKey(agent, repo, number)
	dispatchKey := dispatchStoreKey(agent, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Block if any cron/manual run is active — either within its original TTL
	// window or still in flight after the window expired (refcount > 0). The
	// second condition closes the race: a long-running job that outlasts
	// dedup_window_seconds would have an expired expiresAt, but its refcount
	// is still positive until RemoveCronMark is called.
	if e, ok := s.entries[cronKey]; (ok && now.Before(e.expiresAt)) || s.cronRefCounts[cronKey] > 0 {
		return false
	}
	// Block if dispatch has already claimed the slot (pending or committed) or
	// if a webhook/fanOut run is still in flight past the TTL window.
	if e, ok := s.entries[dispatchKey]; ok && now.Before(e.expiresAt) {
		return false
	}
	if s.webhookRefCounts[dispatchKey] > 0 {
		return false
	}
	// Reserve a pending dispatch slot. CommitClaim / AbandonClaim must follow.
	s.entries[dispatchKey] = dispatchEntry{expiresAt: now.Add(s.ttl), committed: false}
	return true
}

// Dispatcher validates and enqueues inter-agent dispatch requests produced by
// agent runs. It enforces whitelist, opt-in, self-dispatch, depth, fanout, and
// dedup safety limits.
type Dispatcher struct {
	cfg      config.DispatchConfig
	db       *sql.DB
	dedup    *DispatchDedupStore
	counters dispatchCounters
	queue    EventEnqueuer
	graphRec GraphRecorder // optional; set via WithGraphRecorder
	logger   zerolog.Logger
}

// NewDispatcher builds a Dispatcher. db is read on every dispatch to look up
// the target agent's allow_dispatch flag — there is no in-memory agent cache.
func NewDispatcher(cfg config.DispatchConfig, db *sql.DB, dedup *DispatchDedupStore, queue EventEnqueuer, logger zerolog.Logger) *Dispatcher {
	return &Dispatcher{
		cfg:    cfg,
		db:     db,
		dedup:  dedup,
		queue:  queue,
		logger: logger.With().Str("component", "dispatcher").Logger(),
	}
}

// WithGraphRecorder attaches an optional recorder called on each successfully
// enqueued dispatch. Safe to call after NewDispatcher.
func (d *Dispatcher) WithGraphRecorder(r GraphRecorder) {
	d.graphRec = r
}

// lookupAgent returns the named agent from SQLite, or false when no row
// matches. Called on every dispatch to check the target's allow_dispatch
// flag.
func (d *Dispatcher) lookupAgent(name string) (fleet.Agent, bool) {
	agents, err := store.ReadAgents(d.db)
	if err != nil {
		d.logger.Error().Err(err).Msg("dispatcher: read agents")
		return fleet.Agent{}, false
	}
	for _, a := range agents {
		if a.Name == name {
			return a, true
		}
	}
	return fleet.Agent{}, false
}

// ProcessDispatches validates and enqueues each dispatch request from a single
// agent run. originator is the agent that produced the requests; ev is the
// originating event; rootEventID and currentDepth describe the chain.
// parentSpanID is the trace span ID of the dispatching run; it is embedded in
// each dispatch event payload so child runs can link back to their parent span.
// All requests are attempted; an error is returned if any enqueue fails.
func (d *Dispatcher) ProcessDispatches(
	ctx context.Context,
	originator fleet.Agent,
	ev Event,
	rootEventID string,
	currentDepth int,
	parentSpanID string,
	requests []ai.DispatchRequest,
) error {
	var errs []error
	fanout := 0
	for _, req := range requests {
		req.Agent = sanitizeName(req.Agent)
		d.counters.requestedTotal.Add(1)

		// When the agent omits number (zero value), fall back to the originating
		// event's number so the target receives the correct item context and
		// unrelated omitted-number dispatches don't collapse under the same dedup key.
		number := req.Number
		if number == 0 {
			number = ev.Number
		}

		logBase := d.logger.With().
			Str("originator", originator.Name).
			Str("target", req.Agent).
			Str("repo", ev.Repo.FullName).
			Int("number", number).
			Logger()

		// Self-dispatch check (belt-and-braces; config validation already forbids it).
		if req.Agent == originator.Name {
			logBase.Warn().Msg("dispatch dropped: self-dispatch")
			d.counters.droppedSelf.Add(1)
			continue
		}

		// Whitelist check: originator's CanDispatch must include the target.
		if !slices.Contains(originator.CanDispatch, req.Agent) {
			logBase.Warn().Msg("dispatch dropped: target not in originator's can_dispatch whitelist")
			d.counters.droppedNoWhitelist.Add(1)
			continue
		}

		// Opt-in check: target must have allow_dispatch: true.
		target, ok := d.lookupAgent(req.Agent)
		if !ok || !target.AllowDispatch {
			logBase.Warn().Msg("dispatch dropped: target has allow_dispatch: false")
			d.counters.droppedNoOptin.Add(1)
			continue
		}

		// Depth cap.
		newDepth := currentDepth + 1
		if newDepth > d.cfg.MaxDepth {
			logBase.Warn().Int("depth", newDepth).Int("max_depth", d.cfg.MaxDepth).Msg("dispatch dropped: max depth exceeded")
			d.counters.droppedDepth.Add(1)
			continue
		}

		// Fanout cap.
		if fanout >= d.cfg.MaxFanout {
			logBase.Warn().Int("fanout", fanout).Int("max_fanout", d.cfg.MaxFanout).Msg("dispatch dropped: max fanout exceeded")
			d.counters.droppedFanout.Add(1)
			continue
		}

		// Atomic cron-and-dispatch dedup: TryClaimForDispatch checks the cron
		// namespace (any active cron/manual run for this item context) and the
		// dispatch namespace (any existing dispatch claim) in a single mutex
		// acquisition, then reserves a pending dispatch slot. This eliminates
		// the TOCTOU race that existed when SeenCronRun and TryClaim were
		// separate operations: the old sequence allowed a concurrent cron path
		// to observe no dispatch claim and the dispatch path to observe no cron
		// mark before either had written, so both could proceed concurrently.
		//
		// On success, CommitClaim (after PushEvent) or AbandonClaim (on failure)
		// finalises the reservation.
		if !d.dedup.TryClaimForDispatch(req.Agent, ev.Repo.FullName, number, time.Now()) {
			logBase.Debug().Msg("dispatch deduped: active cron run or existing dispatch claim within window")
			d.counters.deduped.Add(1)
			continue
		}

		dispatchEv := Event{
			ID:     GenEventID(),
			Repo:   ev.Repo,
			Kind:   "agent.dispatch",
			Number: number,
			Actor:  originator.Name,
			Payload: map[string]any{
				"target_agent":   req.Agent,
				"reason":         req.Reason,
				"root_event_id":  rootEventID,
				"dispatch_depth": newDepth,
				"invoked_by":     originator.Name,
				"parent_span_id": parentSpanID,
			},
		}

		if err := d.queue.PushEvent(ctx, dispatchEv); err != nil {
			// Release the pending claim so future retries are not blocked and
			// DispatchAlreadyClaimed never sees a phantom committed slot.
			d.dedup.AbandonClaim(req.Agent, ev.Repo.FullName, number)
			logBase.Error().Err(err).Msg("failed to enqueue dispatch event")
			errs = append(errs, fmt.Errorf("dispatch %q: %w", req.Agent, err))
			continue
		}

		// Enqueue succeeded — commit the claim so DispatchAlreadyClaimed returns
		// true and the autonomous scheduler skips a duplicate run for this target.
		d.dedup.CommitClaim(req.Agent, ev.Repo.FullName, number)

		// Record the dispatch edge in the interaction graph if an observer is set.
		if d.graphRec != nil {
			d.graphRec.RecordDispatch(originator.Name, req.Agent, ev.Repo.FullName, number, req.Reason)
		}

		fanout++
		d.counters.enqueued.Add(1)
		logBase.Info().Int("depth", newDepth).Str("reason", req.Reason).Msg("dispatch event enqueued")
	}
	return errors.Join(errs...)
}

// DispatchAlreadyClaimed returns true if a dispatch has claimed (pending or
// committed) the (agentName, repo, 0) slot in the dispatch dedup namespace
// within the current dedup window (dispatch-first ordering). Checking pending
// claims too ensures that a dispatch which has successfully TryClaim'd but has
// not yet called CommitClaim (PushEvent still in flight) still blocks a
// concurrent cron/manual run from starting.
//
// This is a read-only check; it does not write to the store. Call
// MarkAutonomousRun separately, only once the run is confirmed to proceed.
func (d *Dispatcher) DispatchAlreadyClaimed(agentName, repo string, now time.Time) bool {
	return d.dedup.SeesPendingOrCommitted(agentName, repo, 0, now)
}

// MarkAutonomousRun writes a cron-namespace activity mark for (agentName,
// repo, 0) that persists for the full dedup_window_seconds. It must be
// called before the run starts (after backend and runner resolution succeed)
// so that dispatches arriving during the in-flight run are suppressed. If the
// run fails, call RollbackAutonomousRun to remove the mark so that future
// dispatches are not spuriously suppressed for the full dedup window.
//
// The cron mark lives in a different key namespace from dispatch entries,
// so repeated cron runs are never blocked by this mark — only dispatches
// that share the same item context (number=0, the autonomous context) are.
func (d *Dispatcher) MarkAutonomousRun(agentName, repo string, now time.Time) {
	d.dedup.MarkCronRun(agentName, repo, 0, now)
}

// TryMarkAutonomousRun atomically checks whether a dispatch has already
// claimed the (agentName, repo, 0) slot and, if not, writes a cron-namespace
// mark. Returns true if the mark was written and the caller may proceed with
// the run. Returns false if a dispatch claim exists (caller should return
// ErrDispatchSkipped).
//
// This replaces the split DispatchAlreadyClaimed → MarkAutonomousRun sequence
// with a single-lock operation, closing the TOCTOU race between the two paths.
//
// If the run fails before completing, call RollbackAutonomousRun to remove the
// mark so that future dispatches are not spuriously suppressed.
func (d *Dispatcher) TryMarkAutonomousRun(agentName, repo string, now time.Time) bool {
	return d.dedup.TryClaimForCron(agentName, repo, 0, now)
}

// RollbackAutonomousRun removes the cron-namespace mark written by
// MarkAutonomousRun or TryMarkAutonomousRun. It must be called when a run
// fails so that the stale mark does not suppress autonomous-context dispatches
// for the full dedup_window_seconds.
func (d *Dispatcher) RollbackAutonomousRun(agentName, repo string) {
	d.dedup.RemoveCronMark(agentName, repo, 0)
}

// FinalizeAutonomousRun decrements the cron-namespace refcount for
// (agentName, repo, 0) after a run completes successfully. Unlike
// RollbackAutonomousRun it preserves the cron entry so that
// TryClaimForDispatch continues to suppress autonomous-context dispatches
// until the full dedup_window_seconds window expires naturally.
func (d *Dispatcher) FinalizeAutonomousRun(agentName, repo string) {
	d.dedup.FinalizeCronMark(agentName, repo, 0)
}

// Stats returns a snapshot of the current dispatch counters.
func (d *Dispatcher) Stats() DispatchStats {
	return d.counters.snapshot()
}

// sanitizeName lowercases and trims a name for safe comparison.
func sanitizeName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// GenEventID returns a short random hex string suitable for use as a root
// event ID when no delivery ID is available.
func GenEventID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

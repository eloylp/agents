package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
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

// DispatchDedupStore suppresses duplicate (target_agent, repo, number) dispatch
// requests within a configurable TTL window. It mirrors the shape of
// webhook.DeliveryStore.
type DispatchDedupStore struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]time.Time
}

// NewDispatchDedupStore returns a store with the given TTL window in seconds.
func NewDispatchDedupStore(ttlSeconds int) *DispatchDedupStore {
	return &DispatchDedupStore{
		ttl:     time.Duration(ttlSeconds) * time.Second,
		entries: make(map[string]time.Time),
	}
}

// Start launches a background goroutine that periodically evicts expired entries.
func (s *DispatchDedupStore) Start(ctx context.Context) {
	if s.ttl <= 0 {
		return
	}
	go func() {
		tickInterval := s.ttl / 4
		if tickInterval < time.Second {
			tickInterval = time.Second
		}
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
	for key, expiresAt := range s.entries {
		if now.After(expiresAt) {
			delete(s.entries, key)
		}
	}
}

// SeenOrAdd returns true if this (target, repo, number) combination has been
// seen within the TTL window, otherwise records it and returns false.
func (s *DispatchDedupStore) SeenOrAdd(target, repo string, number int, now time.Time) bool {
	key := fmt.Sprintf("%s\x00%s\x00%d", target, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	if expiresAt, ok := s.entries[key]; ok && now.Before(expiresAt) {
		return true
	}
	s.entries[key] = now.Add(s.ttl)
	return false
}

// Remove deletes a (target, repo, number) entry from the dedup store. It is
// used to roll back a SeenOrAdd call when the corresponding enqueue fails, so
// that a transient queue error does not suppress retries for the full TTL.
func (s *DispatchDedupStore) Remove(target, repo string, number int) {
	key := fmt.Sprintf("%s\x00%s\x00%d", target, repo, number)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
}

// Dispatcher validates and enqueues inter-agent dispatch requests produced by
// agent runs. It enforces whitelist, opt-in, self-dispatch, depth, fanout, and
// dedup safety limits.
type Dispatcher struct {
	cfg      config.DispatchConfig
	agents   map[string]config.AgentDef // all agents by name (lower-cased)
	dedup    *DispatchDedupStore
	counters dispatchCounters
	queue    EventEnqueuer
	logger   zerolog.Logger
}

// NewDispatcher builds a Dispatcher. agents must be the full agent map (by
// lower-cased name) from the loaded config.
func NewDispatcher(cfg config.DispatchConfig, agents map[string]config.AgentDef, dedup *DispatchDedupStore, queue EventEnqueuer, logger zerolog.Logger) *Dispatcher {
	return &Dispatcher{
		cfg:    cfg,
		agents: agents,
		dedup:  dedup,
		queue:  queue,
		logger: logger.With().Str("component", "dispatcher").Logger(),
	}
}

// ProcessDispatches validates and enqueues each dispatch request from a single
// agent run. originator is the agent that produced the requests; ev is the
// originating event; rootEventID and currentDepth describe the chain.
func (d *Dispatcher) ProcessDispatches(
	ctx context.Context,
	originator config.AgentDef,
	ev Event,
	rootEventID string,
	currentDepth int,
	requests []ai.DispatchRequest,
) {
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
		if !containsStr(originator.CanDispatch, req.Agent) {
			logBase.Warn().Msg("dispatch dropped: target not in originator's can_dispatch whitelist")
			d.counters.droppedNoWhitelist.Add(1)
			continue
		}

		// Opt-in check: target must have allow_dispatch: true.
		target, ok := d.agents[req.Agent]
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

		// Dedup check.
		if d.dedup.SeenOrAdd(req.Agent, ev.Repo.FullName, number, time.Now()) {
			logBase.Debug().Msg("dispatch deduped: already seen within window")
			d.counters.deduped.Add(1)
			continue
		}

		dispatchEv := Event{
			ID:     rootEventID,
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
			},
		}

		if err := d.queue.PushEvent(ctx, dispatchEv); err != nil {
			// Roll back the dedup entry so the next retry is not suppressed
			// for the full TTL window due to this transient enqueue failure.
			d.dedup.Remove(req.Agent, ev.Repo.FullName, number)
			logBase.Error().Err(err).Msg("failed to enqueue dispatch event")
			continue
		}

		fanout++
		d.counters.enqueued.Add(1)
		logBase.Info().Int("depth", newDepth).Str("reason", req.Reason).Msg("dispatch event enqueued")
	}
}

// CheckAndMarkAutonomousRun checks the dedup store for the key
// (agentName, repo, 0), which represents an autonomous (cron or manual)
// execution. Returns true if this (agent, repo) pair was already seen within
// the dedup window — meaning the caller should skip the run to avoid racing
// with an in-flight dispatch to the same target. If not seen, records the key
// so a near-simultaneous dispatch is collapsed against it.
func (d *Dispatcher) CheckAndMarkAutonomousRun(agentName, repo string, now time.Time) bool {
	return d.dedup.SeenOrAdd(agentName, repo, 0, now)
}

// Stats returns a snapshot of the current dispatch counters.
func (d *Dispatcher) Stats() DispatchStats {
	return d.counters.snapshot()
}

// containsStr reports whether slice contains s.
func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
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

package scheduler

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// DefaultReconcileInterval is the default cadence at which the scheduler
// re-reads bindings from SQLite and reconciles its registered cron entries.
// Configurable via NewScheduler's interval argument; 0 means use this default.
const DefaultReconcileInterval = 30 * time.Second

// zerologCronLogger adapts zerolog.Logger to the cron.Logger interface
// required by chain wrappers such as SkipIfStillRunning.
type zerologCronLogger struct {
	logger zerolog.Logger
}

func (z zerologCronLogger) Info(msg string, keysAndValues ...any) {
	appendLogKV(z.logger.Info(), keysAndValues).Msg(msg)
}

func (z zerologCronLogger) Error(err error, msg string, keysAndValues ...any) {
	appendLogKV(z.logger.Error().Err(err), keysAndValues).Msg(msg)
}

// appendLogKV attaches key-value pairs to a zerolog event. keysAndValues is a
// flat alternating slice of [key, value, key, value, …]; odd-length tails are
// silently ignored, matching cron.Logger convention.
func appendLogKV(e *zerolog.Event, keysAndValues []any) *zerolog.Event {
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		e = e.Interface(fmt.Sprintf("%v", keysAndValues[i]), keysAndValues[i+1])
	}
	return e
}

// AgentStatus is the runtime state of a single registered cron binding.
type AgentStatus struct {
	Name       string
	Repo       string
	LastRun    *time.Time // nil if never run in this process lifetime
	NextRun    time.Time
	LastStatus string // "success", "error", or "" if never run
}

// agentEntry records the metadata for a registered cron job.
type agentEntry struct {
	name   string
	repo   string
	spec   string // the cron expression as registered, used to detect spec changes
	cronID cron.EntryID
}

// lastRunRecord holds the outcome of the most recent binding execution.
type lastRunRecord struct {
	at     time.Time
	status string
}

// Scheduler wires cron-triggered agent bindings from SQLite into the
// robfig/cron engine. It is a pure event producer: every cron tick pushes a
// "cron" event onto the queue, and the engine handles execution uniformly
// with all other event kinds. The engine notifies us back via RecordLastRun
// so the per-binding schedule view in /agents stays current.
//
// The set of registered cron entries is the one piece of in-memory state
// the daemon still has to cache, because robfig/cron requires entries to
// be registered up front in order to fire at scheduled times. The
// scheduler keeps that state in sync with SQLite via a reconciler
// goroutine that polls every reconcileInterval, diffs the current binding
// set against what's registered, and adds/removes entries as needed.
// CRUD writes do not push updates to the scheduler — the next reconcile
// tick picks them up.
type Scheduler struct {
	store             *store.Store
	cron              *cron.Cron
	logger            zerolog.Logger
	reconcileInterval time.Duration
	ctxMu             sync.RWMutex
	runCtx            context.Context
	bindMu            sync.RWMutex // protects agentEntries during reconcile
	agentEntries      []agentEntry
	lastRunsMu        sync.RWMutex
	lastRuns          map[string]lastRunRecord // key: "name\x00repo"
	queue             *workflow.DataChannels   // required at runtime; cron ticks push events here for the engine to handle
}

// WithEventQueue wires the engine's event queue. Every cron tick builds a
// "cron" event and pushes it here; the engine handles execution uniformly
// with all other event kinds. The engine's LastRunRecorder hook calls
// RecordLastRun back into this scheduler when the run completes, keeping
// the per-binding schedule view in /agents up to date.
func (s *Scheduler) WithEventQueue(q *workflow.DataChannels) {
	s.queue = q
}

// RecordLastRun is called by the engine after every cron run completes.
// Implements workflow.LastRunRecorder so /agents and /status see the same
// schedule state operators saw under the old in-scheduler execution path.
func (s *Scheduler) RecordLastRun(agent, repo string, at time.Time, status string) {
	s.recordLastRun(agent, repo, at, status)
}

// NewScheduler builds a scheduler. It performs an initial reconcile from
// SQLite at construction time so the cron is fully populated before Run
// starts; subsequent updates flow through the reconciler goroutine.
//
// reconcileInterval controls how often the scheduler polls SQLite. Pass 0
// for the default (30s).
func NewScheduler(st *store.Store, reconcileInterval time.Duration, logger zerolog.Logger) (*Scheduler, error) {
	if reconcileInterval == 0 {
		reconcileInterval = DefaultReconcileInterval
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	cronLogger := zerologCronLogger{logger: logger.With().Str("component", "scheduler").Logger()}
	c := cron.New(
		cron.WithParser(parser),
		cron.WithChain(cron.SkipIfStillRunning(cronLogger)),
	)
	s := &Scheduler{
		store:             st,
		cron:              c,
		logger:            logger.With().Str("component", "scheduler").Logger(),
		reconcileInterval: reconcileInterval,
		lastRuns:          make(map[string]lastRunRecord),
	}
	if err := s.reconcile(); err != nil {
		return nil, err
	}
	return s, nil
}

// Run starts the cron engine and the reconciler goroutine, then blocks
// until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) error {
	s.setRunCtx(ctx)
	s.logger.Info().Int("jobs", len(s.cron.Entries())).Dur("reconcile_interval", s.reconcileInterval).Msg("starting scheduler")
	s.cron.Start()

	go s.reconcileLoop(ctx)

	<-ctx.Done()
	stopped := s.cron.Stop()
	<-stopped.Done()
	s.logger.Info().Msg("scheduler stopped")
	return nil
}

// reconcileLoop polls SQLite at reconcileInterval and reconciles registered
// cron entries against the current binding set. CRUD-added bindings begin
// firing within one interval; CRUD-removed bindings stop firing within one
// interval.
func (s *Scheduler) reconcileLoop(ctx context.Context) {
	t := time.NewTicker(s.reconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.reconcile(); err != nil {
				s.logger.Error().Err(err).Msg("scheduler reconcile failed")
			}
		}
	}
}

// reconcile reads the current cron-binding set from SQLite, diffs it
// against the registered entries, and applies the difference. The diff
// keys on (agent, repo, cron-spec): a binding whose cron string changes
// is treated as remove-old + add-new.
func (s *Scheduler) reconcile() error {
	repos, err := s.store.ReadRepos()
	if err != nil {
		return fmt.Errorf("scheduler reconcile: read repos: %w", err)
	}
	agents, err := s.store.ReadAgents()
	if err != nil {
		return fmt.Errorf("scheduler reconcile: read agents: %w", err)
	}
	agentByName := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		agentByName[a.Name] = struct{}{}
	}

	// Build the desired set of (agent, repo, spec) entries.
	type want struct{ name, repo, spec string }
	desired := map[want]bool{}
	for _, repo := range repos {
		if !repo.Enabled {
			continue
		}
		for _, b := range repo.Use {
			if !b.IsEnabled() || !b.IsCron() {
				continue
			}
			if _, ok := agentByName[b.Agent]; !ok {
				s.logger.Warn().Str("repo", repo.Name).Str("agent", b.Agent).Msg("scheduler reconcile: skipping binding to unknown agent")
				continue
			}
			desired[want{name: b.Agent, repo: repo.Name, spec: b.Cron}] = true
		}
	}

	s.bindMu.Lock()
	defer s.bindMu.Unlock()

	// Remove entries that are no longer desired (or whose spec changed).
	kept := s.agentEntries[:0]
	for _, e := range s.agentEntries {
		key := want{name: e.name, repo: e.repo, spec: e.spec}
		if desired[key] {
			delete(desired, key) // mark as already registered
			kept = append(kept, e)
			continue
		}
		s.cron.Remove(e.cronID)
	}
	s.agentEntries = kept

	// Register every entry that's desired but not yet registered. Any
	// remaining keys in `desired` after the loop above fall in this set.
	for w := range desired {
		job := s.makeCronJob(w.repo, w.name)
		id, err := s.cron.AddFunc(w.spec, job)
		if err != nil {
			s.logger.Error().Str("repo", w.repo).Str("agent", w.name).Str("cron", w.spec).Err(err).Msg("scheduler reconcile: add cron entry failed")
			continue
		}
		s.agentEntries = append(s.agentEntries, agentEntry{name: w.name, repo: w.repo, spec: w.spec, cronID: id})
		entry := s.cron.Entry(id)
		s.logger.Info().
			Str("repo", w.repo).
			Str("agent", w.name).
			Str("cron", w.spec).
			Time("next_run", entry.Next).
			Msg("cron job registered")
	}
	return nil
}

func (s *Scheduler) makeCronJob(repo string, agentName string) func() {
	return func() {
		ctx := s.currentRunCtx()
		if ctx.Err() != nil {
			return
		}
		if s.queue == nil {
			s.logger.Error().Str("repo", repo).Str("agent", agentName).Msg("cron tick: scheduler has no event queue wired (call WithEventQueue at startup)")
			return
		}
		ev := workflow.Event{
			ID:         workflow.GenEventID(),
			Repo:       workflow.RepoRef{FullName: repo, Enabled: true},
			Kind:       "cron",
			Actor:      agentName,
			Payload:    map[string]any{"target_agent": agentName},
			EnqueuedAt: time.Now(),
		}
		if _, err := s.queue.PushEvent(ctx, ev); err != nil {
			s.logger.Error().Str("repo", repo).Str("agent", agentName).Err(err).Msg("cron tick: enqueue failed")
			s.recordLastRun(agentName, repo, time.Now(), "error")
		}
	}
}

func (s *Scheduler) recordLastRun(name, repo string, at time.Time, status string) {
	key := name + "\x00" + repo
	s.lastRunsMu.Lock()
	s.lastRuns[key] = lastRunRecord{at: at, status: status}
	s.lastRunsMu.Unlock()
}

// AgentStatuses returns the current scheduling state for all registered bindings.
func (s *Scheduler) AgentStatuses() []AgentStatus {
	s.lastRunsMu.RLock()
	runs := maps.Clone(s.lastRuns)
	s.lastRunsMu.RUnlock()

	entries := s.cron.Entries()
	entryByID := make(map[cron.EntryID]cron.Entry, len(entries))
	for _, e := range entries {
		entryByID[e.ID] = e
	}

	s.bindMu.RLock()
	agentEntries := slices.Clone(s.agentEntries)
	s.bindMu.RUnlock()

	statuses := make([]AgentStatus, 0, len(agentEntries))
	for _, ae := range agentEntries {
		entry, ok := entryByID[ae.cronID]
		if !ok {
			continue
		}
		key := ae.name + "\x00" + ae.repo
		lr := runs[key]
		var lastRun *time.Time
		if !lr.at.IsZero() {
			t := lr.at
			lastRun = &t
		}
		nextRun := entry.Next
		if nextRun.IsZero() && entry.Schedule != nil {
			nextRun = entry.Schedule.Next(time.Now())
		}
		statuses = append(statuses, AgentStatus{
			Name:       ae.name,
			Repo:       ae.repo,
			LastRun:    lastRun,
			NextRun:    nextRun,
			LastStatus: lr.status,
		})
	}
	return statuses
}

// Reconcile triggers a one-shot reconcile out of band — useful for tests
// that want to force the registration cycle without waiting for the
// reconciler ticker.
func (s *Scheduler) Reconcile() error { return s.reconcile() }

func (s *Scheduler) setRunCtx(ctx context.Context) {
	s.ctxMu.Lock()
	defer s.ctxMu.Unlock()
	s.runCtx = ctx
}

func (s *Scheduler) currentRunCtx() context.Context {
	s.ctxMu.RLock()
	defer s.ctxMu.RUnlock()
	if s.runCtx == nil {
		return context.Background()
	}
	return s.runCtx
}

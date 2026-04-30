# Architecture

How the Go code is laid out, why, and how a request flows through it. Written for someone who has cloned the repo and wants the package map before reading code. Pairs with [`mental-model.md`](mental-model.md), which is about how *agents* work; this one is about how *the daemon* works.

## The thirty-second model

The daemon is a single binary, a single runtime, with a few moving parts:

- A **runtime engine** that turns events into agent invocations.
- A **durable event queue** backed by SQLite — the DB is the source of truth, the in-memory channel is just a wake-up notification.
- A handful of **domain handlers** (fleet, repos, config, observe, runners, webhook) that own the HTTP surface.
- A **cron scheduler** that produces events on a schedule — it doesn't run agents itself.
- An **MCP server** at `/mcp` that wraps the same handlers as fleet-management tools.
- A **composing root**, `internal/daemon.New`, that assembles every component and exposes a single blocking `Run(ctx) error`.

There is exactly one place that runs an agent: `engine.runAgent`. Cron tick, GitHub webhook, `POST /run`, MCP `trigger_agent`, and inter-agent dispatch all converge there through the same event queue. There is no out-of-band CLI execution mode — the run-lock and dispatch-dedup are race-free by construction.

## Layer cake

The packages stack like this:

```
cmd/agents/main.go              entry point — daemon.LoadConfig, daemon.New, d.Run

internal/
├─ fleet/                       domain entities — Agent, Repo, Skill, Backend, Binding
├─ config/                      *Config + YAML loader + cross-entity validators
├─ store/                       SQLite schema, migrations, *store.Store facade,
│                                 event_queue, memory, CRUD primitives
│
├─ workflow/                    Engine, Processor, Dispatcher, DataChannels, dispatch dedup
├─ scheduler/                   cron registration + event producer
├─ ai/                          prompt composition, CLI runner (stdin in, JSON out)
├─ backends/                    backend discovery (CLI probing, MCP health, model catalog)
├─ anthropic_proxy/             Anthropic↔OpenAI translation proxy
├─ observe/                     events/traces/spans/steps/memory persistence + SSE hubs
│
├─ daemon/                      daemon as a single composed unit: lifecycle, router,
│   │                             /status, /run, proxy/UI/MCP mounts
│   ├─ fleet/                   /agents (CRUD + view + orphans), /skills, /backends
│   ├─ repos/                   /repos, /repos/{}/bindings
│   ├─ config/                  /config snapshot, /export, /import
│   ├─ observe/                 /events, /traces, /graph, /dispatches, /memory + SSE
│   └─ runners/                 /runners listing + delete + retry (durable event_queue surface; JOINs with traces)
│
├─ webhook/                     /webhooks/github only — HMAC, delivery dedup, event parsing
├─ mcp/                         MCP server; one Deps struct of concrete pointers
├─ ui/                          embedded Next.js dist/, served at /ui/
└─ logging/, setup/             zerolog wiring, interactive setup
```

The tiers, from bottom to top:

**Domain (zero or near-zero deps):** `fleet`, `config`, `store`, `logging`. Pure data shapes and persistence. `fleet` has no transitive deps at all — it's structs and pure functions like `NormalizeAgent`. `config` and `store` import `fleet`. `*store.Store` wraps the bare `*sql.DB`; runtime components hold the facade, not the connection.

**Runtime engine:** `workflow`, `scheduler`, `ai`, `backends`, `anthropic_proxy`, `observe`. The actual fleet runtime. An event arrives on the queue, the processor pulls it, the engine looks up the right binding (or resolves the target agent from the payload), the AI runner invokes the CLI, the response is parsed, traces and steps are recorded, and any returned `dispatch` array is enqueued as new events.

**HTTP layer:** `internal/daemon` and its sub-packages, plus `webhook` and `mcp`. Each domain handler is a small package with a constructor that takes its dependencies as concrete pointers and a `RegisterRoutes(router, withTimeout)` method. The composing root is `internal/daemon/daemon.go`, which constructs every component, holds them as fields on the `*Daemon` value, and registers all routes from one place.

**Entry:** `cmd/agents/main.go` is six lines of real work — load config, build a logger, call `daemon.New`, call `d.Run(ctx)`. Everything else is in the daemon package.

## Composing root

`internal/daemon/daemon.go` is the only file in the codebase that knows how every piece fits. It reads roughly:

```go
func New(cfg *config.Config, st *store.Store, logger zerolog.Logger) (*Daemon, error) {
    // 1. runtime engine over the durable queue
    channels := workflow.NewDataChannels(cfg.Daemon.Processor.EventQueueBuffer, st)
    engine   := workflow.NewEngine(st, cfg.Daemon.Processor, channels, logger)

    memBackend := st.NewMemoryBackend()
    engine.WithMemory(memBackend)

    obs := observe.NewStore(st.DB())
    engine.WithTraceRecorder(obs)
    engine.WithGraphRecorder(obs)
    engine.WithRunTracker(obs.ActiveRuns)
    engine.WithStepRecorder(obs)
    memBackend.SetChangeNotifier(obs.PublishMemoryChange)

    // 2. scheduler is a cron event producer; engine notifies it on completion
    sched, _ := scheduler.NewScheduler(st, scheduler.DefaultReconcileInterval, logger)
    sched.WithEventQueue(channels)
    engine.WithLastRunRecorder(sched)

    // 3. domain HTTP handlers — concrete pointers, no With-pattern wiring
    fleetH   := daemonfleet.New(st, cfg.Daemon.HTTP.MaxBodyBytes, sched, obs, logger)
    reposH   := daemonrepos.New(st, cfg.Daemon.HTTP.MaxBodyBytes, logger)
    configH  := daemonconfig.New(st, cfg.Daemon, logger)
    observeH := daemonobserve.New(obs, st, sched, engine, st.NewMemoryReader(), logger)
    runnersH := daemonrunners.New(st, channels, obs, logger)
    webhookH := webhook.NewHandler(deliveryStore, channels, st, cfg.Daemon.HTTP, logger)

    // 4. processor over the queue
    processor := workflow.NewProcessor(channels, engine, workers, shutdown, logger)
    processor.WithEventRecorder(obs)

    d := &Daemon{ /* assignments */ }

    // 5. MCP last — concrete pointers to the same instances above
    d.mcp = mcpserver.New(mcpserver.Deps{
        Store: st, Channels: channels, Observe: obs, Engine: engine,
        Fleet: fleetH, Repos: reposH, Config: configH, RunnersH: runnersH,
        StatusJSON: d.StatusJSON, Logger: logger,
    })
    return d, nil
}
```

There are no `With*` setters and no plumbing interfaces between the daemon and its handlers. Every collaborator is a concrete pointer the daemon holds as a field. The "external construction" pattern is preserved — domain handlers don't import each other and don't import `internal/daemon` — but it's enforced by package layout, not by abstraction.

The MCP `Deps` struct holds **concrete pointers**, not interfaces. Coupling is fine; the daemon ships as one binary. Tests build against a real fixture (real SQLite tempdir, real handlers).

## Trigger surfaces — every run goes through one engine path

| Surface | Push site | Event Kind | Pre-run dedup gate |
|---|---|---|---|
| Cron tick | `scheduler.makeCronJob` | `cron` | `Dispatcher.TryMarkAutonomousRun` (cron-namespace) |
| GitHub webhook | `webhook.Handler.ServeHTTP` | `issues.*`, `pull_request.*`, `push`, … | per-(agent, repo, number) `TryClaimForDispatch` (in `fanOut`) |
| `POST /run` | `daemon.handleAgentsRun` | `agents.run` | per-(agent, repo, 0) `TryClaimForDispatch` |
| MCP `trigger_agent` | `mcp/tools_fleet.go: toolTriggerAgent` | `agents.run` | same as `POST /run` |
| Inter-agent dispatch | `Dispatcher.ProcessDispatches` (called from `runAgent`) | `agent.dispatch` | claim taken at enqueue time; handler skips re-claim |

`engine.HandleEvent` routes `agent.dispatch | agents.run | cron` through `handleDispatchEvent` (which resolves the target agent from `payload.target_agent` and bypasses binding lookup). Webhook events go through `fanOut` for label/event-binding match, then both paths converge on `runAgent`.

## Durable event queue

The queue is the choke point through which every trigger flows. It is durable so the daemon survives restarts without losing buffered work.

```
producer (webhook / scheduler / dispatcher / handleAgentsRun / trigger_agent)
   │
   ▼
DataChannels.PushEvent
   ├─ INSERT INTO event_queue(event_blob, enqueued_at)        ← persist first
   ├─ send QueuedEvent{id, event} on the in-memory channel    ← wake workers
   └─ on full channel / ctx-cancel: DELETE the just-inserted row (rollback)

consumer (workflow.Processor worker)
   ▼
read QueuedEvent{id, event} from channel
   ├─ UPDATE event_queue SET started_at = now WHERE id = ?
   ├─ engine.HandleEvent(ctx, event)
   └─ UPDATE event_queue SET completed_at = now WHERE id = ?  ← regardless of success
```

The DB is the source of truth; the channel is just a notification. On a clean shutdown the table is mostly empty (workers stamp `completed_at` as they go). On a crash, rows whose `completed_at` is still `NULL` are replayed at the next startup — events that were buffered when the daemon stopped, or runs that were interrupted mid-prompt, get a second chance instead of vanishing. Replay relies on agent idempotency: a Docker / Kubernetes orchestrator `SIGKILL`s after ~30s, so an in-flight prompt may be killed mid-execution and re-run from scratch.

A consumer-tier cleanup loop ticks hourly and deletes rows whose `completed_at` is older than 7 days. The table stays bounded regardless of throughput.

`internal/daemon/runners` exposes the table as a per-runner view through `GET /runners`, `DELETE /runners/{id}`, and `POST /runners/{id}/retry`. Each event_queue row is JOINed with `observe.traces` so a completed event that fanned out to N agents shows up as N rows on the wire (one per trace span). In-flight events with no spans recorded yet appear as a single row with `agent: null` and `status: enqueued|running` — that's the "what's running right now" surface. Retry copies the source row's blob into a fresh row and pushes onto the channel — the source row stays as audit history; the same operations are wired as MCP tools.

## Structured concurrency — startup and shutdown

Every long-lived goroutine implements `Run(ctx) error`. The composing root arranges them in two errgroup tiers with separate contexts so shutdown is ordered:

```
parentCtx (SIGTERM cancels this)
   │
   ├─ producers errgroup ─────────── derived from parentCtx
   │     ├─ scheduler.Run            cron + reconciler poll
   │     └─ daemon.runHTTP           HTTP listener
   │
   └─ consumers errgroup ─────────── derived from a fresh background context
         ├─ delivery.Run             webhook delivery dedup eviction
         ├─ engine.RunDispatchDedup  dispatch dedup eviction
         ├─ processor.Run            worker pool
         ├─ store.RunQueueCleanup    event_queue retention sweep
         └─ replayPendingEvents      one-shot startup replay
```

Sequence on shutdown:

1. `parentCtx` cancels (SIGTERM) or a producer returns an error.
2. Producer ctx cancels — HTTP server is gracefully drained, scheduler stops cron and the reconciler.
3. Producer goroutines join.
4. Consumer ctx cancels — processor closes the queue and waits for in-flight runs (bounded by `shutdown_timeout_seconds`); dedup eviction loops and queue cleanup exit.
5. Consumer goroutines join.

Each phase logs a clear line so an operator reading logs sees the full lifecycle. The split lifetime is what lets the queue drain after producers stop accepting new work.

## How a request flows

Three traced examples, one per surface.

### `POST /webhooks/github` — issue labeled

```
mux router
  → webhook.Handler.ServeHTTP
      verifies HMAC SHA-256 against cfg.Daemon.HTTP.WebhookSecret
      dedupes by X-GitHub-Delivery (DeliveryStore TTL cache)
      parses payload, builds workflow.Event
      → channels.PushEvent
            INSERT INTO event_queue
            send QueuedEvent on the in-memory channel
  ← 202 Accepted

async on a worker goroutine:
  workflow.Processor → workflow.Engine.HandleEvent
    → fanOut (label binding match)
        → runAgent (per-(agent, repo) runLock acquired)
            → memory load (when allow_memory)
            → ai.Runner.Run launches the claude/codex CLI subprocess
            → traceRec.RecordSpan + stepRec.RecordSteps (transcript)
            → memory write (when allow_memory && resp.Memory != "")
            → dispatcher.ProcessDispatches (chained agent.dispatch events)
        runLock released
    → UPDATE event_queue SET completed_at = now WHERE id = ?
```

### Cron tick at the top of the hour

```
robfig/cron fires the closure registered by scheduler.registerJobs
  → builds workflow.Event{Kind: "cron", Payload: {target_agent: <name>}}
  → channels.PushEvent
  ← returns immediately

async on a worker goroutine:
  workflow.Processor → workflow.Engine.HandleEvent
    → handleDispatchEvent
        → Dispatcher.TryMarkAutonomousRun (cron-namespace dedup)
        → runAgent (same path as everything else)
        → on completion: dispatcher.FinalizeAutonomousRun
                       + lastRunRec.RecordLastRun
                         (scheduler.lastRuns map → /agents schedule view)
    → UPDATE event_queue SET completed_at = now WHERE id = ?
```

### `POST /agents` — CRUD create

```
mux router
  → daemonfleet.Handler.HandleAgentsCreate
      h.UpsertAgent(req.toConfig())
        → store.UpsertAgent       single SQLite UPSERT
      ← canonical agent JSON
  ← canonical agent JSON
```

There is no reload step. The runtime reads from SQLite on every event — no in-memory cfg cache to invalidate. The next webhook, cron tick, or dispatch picks up the new agent state directly from the store. The scheduler reconciles cron bindings against SQLite on a polling interval (default 60s); the `Reconcile` method reads the repos table, diffs against the registered cron entries, and adds/removes as needed. CRUD writes don't push to the runtime — the next read picks them up.

### `POST /mcp` — `update_agent` tool

```
mcp/server → toolUpdateAgent
  → deps.Fleet.UpdateAgentPatch     (same *daemonfleet.Handler the REST surface uses)
  → store.UpsertAgent
  ← same canonical shape as REST
```

REST and MCP converge at the handler layer — one set of methods, one persistence path, the same normalisation rules. Whether the change comes from `curl`, the `/ui/` dashboard, or a Claude tool call, the wire shape and the side effects are identical.

## Race-prevention invariants

1. **Memory races on (agent, repo)** — `Engine.runLock` (per-key `*sync.Mutex`, lazily created) is held across the read-run-write sequence in `runAgent`. Single process means no second writer; the legacy CLI execution mode was removed precisely because it created a second-process race surface the run-lock can't close.
2. **Duplicate-fire dedup** — `Dispatcher.dedup` keyed by namespace × (agent, repo, number). Three claim contexts: webhook + on-demand share a "dispatch" namespace (`TryClaimForDispatch`); cron has a separate "autonomous" namespace (`TryMarkAutonomousRun`); inter-agent dispatch is claimed at enqueue, not re-claimed at handle. A near-simultaneous webhook and cron tick for the same target both consult the cross-namespace state at gate time, so one self-suppresses.
3. **Durable enqueue / channel coherence** — `PushEvent` inserts into `event_queue` *before* sending on the channel, and rolls back the row if the channel push fails. There is no window where a row sits in SQLite but never reaches a worker, and no window where the channel holds a `QueuedEvent` whose row was never persisted.
4. **Replay idempotency boundary** — replay only re-pushes rows whose `completed_at` is `NULL`. Workers stamp `completed_at` regardless of the agent's success, so a deterministically-failing event is removed from the queue's view (it appears in `/traces` instead of replaying forever).
5. **Cron schedule view freshness** — `Engine.runAgent` calls `lastRunRec.RecordLastRun` after every `Kind=="cron"` event; scheduler's `lastRuns` map carries the latest outcome to `AgentStatuses()`, which feeds `/agents`.

## Observability surface

A single `*observe.Store` records everything; no buffering layer between the engine and SQLite for synchronous inserts.

- `RecordEvent` — events table (async insert) → `/events` + `/events/stream` SSE
- `RecordSpan` — traces table (async insert) → `/traces` + `/traces/{root_event_id}` + SSE
- `RecordSteps` — trace_steps table (**sync** insert; UI accordion needs to read freshly committed rows) → `/traces/{span_id}/steps`
- `RecordDispatch` — dispatch_history table (async insert) → `/graph`
- `ActiveRuns` — in-memory tracker, `IsRunning(agent)` → `/agents.current_status`
- Memory: writes go through `Engine.memory.WriteMemory` (production: `*store.MemoryBackend`); change notifications fan out via the observe store's pub-sub → `/memory/stream`

The observability store is the single recorder for all of these. The engine wires it up via `WithTraceRecorder` / `WithStepRecorder` / `WithRunTracker` / `WithGraphRecorder`. The store has no awareness of HTTP — `internal/daemon/observe` is a thin handler layer on top.

## Why this shape

Three constraints drove the layout.

**SQLite is the source of truth, not an in-memory cache.** Every runtime component reads CRUD-mutable state — agents, repos, skills, backends — from the store on every event. There is no reload protocol because there is no cache to invalidate. A CRUD write is a single SQLite UPSERT; the next read sees the new state. This collapsed an entire layer of plumbing (write coordinator, reload recipe, hot-swap atomicity) and makes the system meaningfully easier to reason about.

**REST and MCP must not drift.** The MCP tool surface uses the same handler instances the HTTP router uses. Any code path that updates an agent goes through `*daemonfleet.Handler.UpsertAgent` regardless of how it was triggered. There is structurally no place for the surfaces to disagree.

**One execution path.** Cron, webhook, on-demand, and dispatch all converge on `engine.runAgent` via the durable event queue. Run-lock, dispatch dedup, run-tracker, transcript recording, and trace span correlation are wired in one place — drift between paths is structurally impossible.

The result is that `cmd/agents/main.go` is six lines of real work, the composing root is one constructor, each domain handler is a self-contained package, and the persistence layer carries every load-bearing invariant.

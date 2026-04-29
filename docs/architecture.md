# Architecture

How the Go code is laid out, why, and how a request flows through it. Written for someone who has cloned the repo and wants the package map before reading code. Pairs with [`mental-model.md`](mental-model.md), which is about how *agents* work; this one is about how *the daemon* works.

## The thirty-second model

The daemon is a single binary, a single runtime, with a few moving parts:

- A **runtime engine** that turns events into agent invocations.
- A **central HTTP server** that owns the router lifecycle and a few aggregator endpoints.
- A handful of **domain handlers** (fleet, repos, config, observe, webhook) that own everything else.
- A **cron scheduler** that produces events on a schedule — it doesn't run agents itself.
- An **MCP server** at `/mcp` that exposes the same handlers as fleet-management tools.
- A **composition root** in `cmd/agents/main.go` that wires it all together and holds the hot-reload recipe.

There is exactly one place that runs an agent: `engine.runAgent`. Cron tick, GitHub webhook, `POST /run`, MCP `trigger_agent`, and inter-agent dispatch all converge there through a single in-memory event queue. There is no out-of-band CLI execution mode and no second runtime — the run-lock and dispatch-dedup are race-free by construction.

## Layer cake

The packages stack like this:

```
cmd/agents/main.go              entry point + composition root + daemonReloader

internal/
├─ fleet/                       domain entities — Agent, Repo, Skill, Backend, Binding
├─ config/                      *Config + YAML loader + cross-entity validators
├─ store/                       SQLite schema, migrations, CRUD primitives
│
├─ workflow/                    Engine, Processor, Dispatcher, DataChannels, dispatch dedup
├─ scheduler/                   cron registration + event producer
├─ ai/                          prompt composition, CLI runner (stdin in, JSON out)
├─ backends/                    backend discovery (CLI probing, MCP health, model catalog)
├─ anthropic_proxy/             Anthropic↔OpenAI translation proxy
├─ observe/                     events/traces/spans/steps/memory persistence + SSE hubs
│
├─ server/                      central HTTP server: lifecycle, router, /status, /run,
│   │                             proxy/UI/MCP mounts; cross-cutting types
│   ├─ fleet/                   /agents (CRUD + view + orphans), /skills, /backends
│   ├─ repos/                   /repos, /repos/{}/bindings
│   ├─ config/                  /config snapshot, /export, /import
│   └─ observe/                 /events, /traces, /graph, /dispatches, /memory + SSE
│
├─ webhook/                     /webhooks/github only — HMAC, delivery dedup, event parsing
├─ mcp/                         MCP server; one Deps struct of concrete pointers
├─ ui/                          embedded Next.js dist/, served at /ui/
└─ logging/, setup/             zerolog wiring, interactive setup
```

The tiers, from bottom to top:

**Domain (zero or near-zero deps):** `fleet`, `config`, `store`, `logging`. Pure data shapes and persistence. `fleet` has no transitive deps at all — it's structs and pure functions like `NormalizeAgent`. `config` and `store` import `fleet`.

**Runtime engine:** `workflow`, `scheduler`, `ai`, `backends`, `anthropic_proxy`, `observe`. The actual fleet runtime. An event arrives on the queue, the processor pulls it, the engine looks up the right binding (or resolves the target agent from the payload), the AI runner invokes the CLI, the response is parsed, traces and steps are recorded, and any returned `dispatch` array is enqueued as new events.

**HTTP layer:** `server` and its sub-packages, plus `webhook` and `mcp`. Each domain handler exposes the same shape: a constructor and a `RegisterRoutes(router, withTimeout)` method. The central server doesn't import the domain handlers — it accepts them through `HandlerRegister` and friends, declared in `internal/server/types.go`.

**Entry:** `cmd/agents/main.go` constructs everything in order, defines the `daemonReloader`, hands handlers to the server, and starts three goroutines — processor, scheduler, server.

## Composing root

`cmd/agents/main.go` is the only file in the codebase that knows how every piece fits. It reads roughly:

```go
// 1. domain layer
cfg, db, err := loadConfig(...)

// 2. runtime engine (one queue, one engine, one observe store)
runners := setupRunners(cfg, logger)
sched, _ := scheduler.NewScheduler(cfg, logger)

dataChannels := workflow.NewDataChannels(cfg.Daemon.Processor.EventQueueBuffer)
engine := workflow.NewEngine(cfg, runners, dataChannels, logger)
engine.WithMemory(memBackend)

obs := observe.NewStore(db)
engine.WithTraceRecorder(obs)
engine.WithStepRecorder(obs)
engine.WithGraphRecorder(obs)
engine.WithRunTracker(obs.ActiveRuns)

// scheduler is a producer, engine notifies it back via LastRunRecorder
sched.WithEventQueue(dataChannels)
engine.WithLastRunRecorder(sched)

// 3. hot-reload coordinator — the recipe lives here, not in scheduler
reloader := &daemonReloader{cfg, engine, sched, makeRunnerBuilder(logger), logger}

// 4. processor (worker pool that drains the queue)
processor := workflow.NewProcessor(dataChannels, engine, workers, shutdown, logger)
processor.WithEventRecorder(obs)

// 5. central HTTP server (knows nothing about domain handlers yet)
srv := server.NewServer(cfg, dataChannels, sched, engine, logger)
srv.WithUI(ui.FS)
srv.WithStore(db, reloader)
srv.WithWebhook(webhook.NewHandler(deliveryStore, dataChannels, srv, logger))

// 6. domain handlers — each constructed externally, then wired in
fleetHandler := serverfleet.New(db, srv, srv, sched, obs, logger)
fleetHandler.RefreshOrphansFromCfg(cfg)
srv.WithFleet(fleetHandler, dispatcher, fleetOrphansAdapter{...}, ...)

reposHandler := serverrepos.New(db, srv, srv, logger)
srv.WithRepos(reposHandler)

configHandler := serverconfig.New(db, srv, srv, logger)
srv.WithConfig(configHandler)

srv.WithObserveRegister(func(r, withTimeout) { ... })

// 7. MCP — concrete pointers to the same instances the REST handlers use
srv.WithMCP(mcpserver.New(mcpserver.Deps{
    DB:      db,
    Server:  srv,
    Queue:   dataChannels,
    Observe: obs,
    Engine:  engine,
    Fleet:   fleetHandler,
    Repos:   reposHandler,
    Config:  configHandler,
    Logger:  logger,
}))

// 8. start everything
group.Go(srv.Run)
group.Go(processor.Run)
group.Go(sched.Run)
group.Wait()
```

The "external construction" pattern is deliberate. `internal/server/server.go` does not import any of `internal/server/{fleet,repos,config,observe}` or `internal/webhook`. That import boundary is what lets the central server move without dragging four sub-packages with it, and what keeps each domain handler independently testable.

The MCP `Deps` struct holds **concrete pointers**, not interfaces. Past sessions removed twelve abstraction interfaces from MCP — they only existed for test-stub injection, and tests now use a real fixture (real SQLite tempdir, real handlers). Coupling is fine; the daemon ships as one binary.

## Trigger surfaces — every run goes through one engine path

| Surface | Push site | Event Kind | Pre-run dedup gate |
|---|---|---|---|
| Cron tick | `scheduler.makeCronJob` | `cron` | `Dispatcher.TryMarkAutonomousRun` (cron-namespace) |
| GitHub webhook | `webhook.Handler.ServeHTTP` | `issues.*`, `pull_request.*`, `push`, … | per-(agent, repo, number) `TryClaimForDispatch` (in `fanOut`) |
| `POST /run` | `server.handleAgentsRun` | `agents.run` | per-(agent, repo, 0) `TryClaimForDispatch` |
| MCP `trigger_agent` | `mcp/tools_fleet.go: toolTriggerAgent` | `agents.run` | same as `POST /run` |
| Inter-agent dispatch | `Dispatcher.ProcessDispatches` (called from `runAgent`) | `agent.dispatch` | claim taken at enqueue time; handler skips re-claim |

`engine.HandleEvent` routes `agent.dispatch | agents.run | cron` through `handleDispatchEvent` (which resolves the target agent from `payload.target_agent` and bypasses binding lookup). Webhook events go through `fanOut` for label/event-binding match, then both paths converge on `runAgent`.

## How a request flows

Three traced examples, one per surface.

### `POST /webhooks/github` — issue labeled

```
mux router
  → webhook.Handler.ServeHTTP
      verifies HMAC SHA-256 against cfg.Daemon.HTTP.WebhookSecret
      dedupes by X-GitHub-Delivery (DeliveryStore TTL cache)
      parses payload, builds workflow.Event
      pushes onto dataChannels.PushEvent
  ← 202 Accepted

async on the worker goroutine:
  workflow.Processor → workflow.Engine.HandleEvent
    → fanOut (label binding match)
        → runAgent (per-(agent, repo) runLock acquired)
            → memory load (if AllowMemory)
            → ai.Runner.Run launches the claude/codex CLI subprocess
            → traceRec.RecordSpan + stepRec.RecordSteps (transcript)
            → memory write (if AllowMemory && resp.Memory != "")
            → dispatcher.ProcessDispatches (chained agent.dispatch events)
        runLock released
```

### Cron tick at the top of the hour

```
robfig/cron fires the closure registered by scheduler.registerJobs
  → builds workflow.Event{Kind: "cron", Payload: {target_agent: <name>}}
  → dataChannels.PushEvent
  ← returns immediately

async on the worker goroutine:
  workflow.Processor → workflow.Engine.HandleEvent
    → handleDispatchEvent
        → Dispatcher.TryMarkAutonomousRun (cron-namespace dedup)
        → runAgent (same path as everything else)
        → on completion: dispatcher.FinalizeAutonomousRun
                       + lastRunRec.RecordLastRun
                         (scheduler.lastRuns map → /agents schedule view)
```

### `POST /agents` — CRUD create (with hot-reload)

```
mux router → server.Server (just routing)
  → server/fleet.Handler.HandleAgentsCreate
      coord.Do(func() error {
          return store.UpsertAgent(...)
      })
        ↓ acquires storeMu (one mutex for the whole reload epoch)
        ↓ runs the upsert
        ↓ on success: server.reloadCron
            ↓ store.ReadSnapshot (one transaction)
            ↓ daemonReloader.Reload (see next section)
            ↓ swap server.cfg under cfgMu
            ↓ fleet.RefreshOrphansFromCfg (orphan cache refreshes)
        ↑ releases storeMu
  ← canonical agent JSON
```

### `POST /mcp` — `update_agent` tool

```
mcp/server → toolUpdateAgent
  → deps.Fleet.UpdateAgentPatch  (same *serverfleet.Handler the REST surface uses)
  → coord.Do(func() error { return store.UpsertAgent(...) })
  ← same canonical shape as REST
```

REST and MCP converge at `coord.Do`. Hot-reload, orphan refresh, and lock discipline apply identically to both.

## Hot-reload coordination

Every CRUD write triggers the same reload recipe. The recipe lives in one place: `daemonReloader.Reload` in `cmd/agents/main.go`.

```
HTTP CRUD write (agent / skill / backend / repo / binding)
     │
     ▼
server.WriteCoordinator.Do(fn)        ← one mutex, the reload epoch boundary
     ├ acquires storeMu
     ├ fn()  // user's write to SQLite
     ▼
server.reloadCron()                    ← server_crud.go
     ├ store.ReadSnapshot(db)         // single tx → consistent (agents,repos,skills,backends)
     ├ cronReloader.Reload(...)        // = daemonReloader.Reload
     │     ├ build new runners via runnerBuilder
     │     ├ engine.UpdateConfigAndRunners(cfg, runners)
     │     │     // atomic swap, lock order matches readers in runAgent;
     │     │     // concurrent runs see either the old pair fully or the new
     │     │     // pair fully, never a torn snapshot.
     │     ├ engine.Dispatcher().UpdateAgents(agents)
     │     │     // dispatch allow-list refresh
     │     └ scheduler.RebuildCron(repos, agents, skills, backends)
     │           // swap cron entries; rolls back on registration failure
     ├ swap server.cfg under cfgMu
     └ onConfigReload(newCfg)          // fleet.RefreshOrphansFromCfg
```

The scheduler used to do all four steps inside its own `Reload`. Now it does one (cron rebind); `daemonReloader` orchestrates the rest. Each component has a single concern: scheduler does cron, engine does runs + memory + dispatch, observe does persistence.

## Cross-cutting glue (`internal/server/types.go`)

Each entry has an explicit reason — cycle break, polymorphism, or a documented test stub.

- **`HandlerRegister`** — the polymorphic shape every domain handler implements. `*serverfleet.Handler`, `*serverrepos.Handler`, `*serverconfig.Handler`, `*webhook.Handler` all satisfy it. The central server iterates over them in `buildHandler` without type knowledge.
- **`WriteCoordinator`** — `*server.Server` satisfies it via `Do`. Domain handlers consume it for the storeMu epoch. Single funnel for every CRUD write across REST and MCP.
- **`CronReloader`** — single-method interface (`Reload(repos, agents, skills, backends) error`). Production wires `*daemonReloader`; tests use `errCronReloader` for failure paths.
- **`StatusProvider`**, **`DispatchStatsProvider`**, **`RuntimeStateProvider`** — runtime info `/status` and `/agents` consume. Production uses `*scheduler.Scheduler`, `*workflow.Engine`, `*observe.Store` directly via the type alias trick (`type AgentStatus = scheduler.AgentStatus`).
- **`OrphansSource`** — bridges the fleet handler's typed orphan snapshot to the cross-package shape `/status` returns.
- **`MemoryReader`** — what `/api/memory/{agent}/{repo}` reads through. Production: `*sqliteWebhookReader` in `cmd/agents`.

## Race-prevention invariants

1. **Memory races on (agent, repo)** — `Engine.runLock` (per-key `*sync.Mutex`, lazily created) is held across the read-run-write sequence in `runAgent`. Single process means no second writer; previous CLI mode (`--run-agent`) was removed precisely because it created a second-process race surface the run-lock can't close.
2. **Duplicate-fire dedup** — `Dispatcher.dedup` keyed by namespace × (agent, repo, number). Three claim contexts: webhook + on-demand share a "dispatch" namespace (`TryClaimForDispatch`); cron has a separate "autonomous" namespace (`TryMarkAutonomousRun`); inter-agent dispatch is claimed at enqueue, not re-claimed at handle. A near-simultaneous webhook and cron tick for the same target both consult the cross-namespace state at gate time, so one self-suppresses.
3. **Hot-reload atomicity** — `Engine.UpdateConfigAndRunners` takes both `cfgMu.Lock` and `runnersMu.Lock` in the same order readers do. A concurrent `runAgent` snapshots both under their respective `RLock`s in one critical section, so it sees the old pair or the new pair, never a mix.
4. **Cron schedule view freshness** — `Engine.runAgent` calls `lastRunRec.RecordLastRun` after every `Kind=="cron"` event; scheduler's `lastRuns` map carries the latest outcome to `AgentStatuses()`, which feeds `/agents`.

## Observability surface

A single `*observe.Store` records everything; no buffering layer between the engine and SQLite for synchronous inserts.

- `RecordEvent` — events table (async insert) → `/events` + `/events/stream` SSE
- `RecordSpan` — traces table (async insert) → `/traces` + `/traces/{root_event_id}` + SSE
- `RecordSteps` — trace_steps table (**sync** insert; UI accordion needs to read freshly committed rows) → `/traces/{span_id}/steps`
- `RecordDispatch` — dispatch_history table (async insert) → `/graph`
- `ActiveRuns` — in-memory tracker, `IsRunning(agent)` → `/agents.current_status`
- Memory: writes go through `Engine.memory.WriteMemory` (production: `*sqliteMemory` in `cmd/agents`); change notifications fan out via `MemorySSE` → `/memory/stream`

The observability store is the single recorder for all of these. The engine wires it up via `WithTraceRecorder` / `WithStepRecorder` / `WithRunTracker` / `WithGraphRecorder`. The store has no awareness of HTTP — `internal/server/observe` is a thin handler layer on top.

## Why this shape

Four constraints drove the layout.

**No torn writes across domains.** When a CRUD write changes (say) a binding, the cron scheduler, the engine's config snapshot, the dispatcher's agent map, and the in-memory routing config must all see the new state before the response goes out. `WriteCoordinator` makes this the *only* way to write — every domain handler funnels through it, the reload recipe runs while the storeMu is held, and on failure the lock prevents any other writer from observing a partial epoch.

**REST and MCP must not drift.** The MCP tool surface uses the same handler instances the HTTP router uses. Any code path that updates an agent goes through `*serverfleet.Handler.UpsertAgent` regardless of how it was triggered. There is structurally no place for the surfaces to disagree.

**One execution path.** Cron, webhook, on-demand, and dispatch all converge on `engine.runAgent` via the event queue. Run-lock, dispatch dedup, run-tracker, transcript recording, and trace span correlation are wired in one place — drift between paths is structurally impossible. The `--run-agent` CLI mode was deleted because it stood up a second runtime that didn't share these guarantees.

**Domain handlers must be independently understandable.** `server/fleet` only knows about agents, skills, and backends. `server/repos` only knows about repos and bindings. They share a write lock through an interface and otherwise never reference each other. Adding a new domain (say, a `server/billing` if this becomes a SaaS) means adding one package, one constructor, one `RegisterRoutes` call in `main.go`, and one line in `daemonReloader` if reload needs to touch the new component. Nothing in the existing handlers needs to change.

The result is that the central server is small, each domain handler is a self-contained package, the reload recipe lives in one ~25-line method, and the composition root is the only place where the picture is assembled.

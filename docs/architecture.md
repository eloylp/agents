# Architecture

How the Go code is laid out, why, and how a request flows through it. Written for someone who has cloned the repo and wants the package map before reading code. Pairs with [`mental-model.md`](mental-model.md), which is about how *agents* work; this one is about how *the daemon* works.

## The thirty-second model

The daemon is a single binary with a few moving parts:

- A **runtime engine** that turns events into agent invocations.
- A **central HTTP server** that owns the router lifecycle and a few aggregator endpoints.
- A handful of **domain handlers** (fleet, repos, config, observe, webhook) that own everything else.
- A **composition root** in `cmd/agents/main.go` that wires it all together.

Domain handlers don't know about each other. The central server doesn't know about its domain handlers' types either — it only sees them through small interfaces. The composition root is the only place that knows the shape of the whole graph.

## Layer cake

The packages stack like this:

```
cmd/agents/main.go              entry point + composition root

internal/
├─ fleet/                       domain entities — Agent, Repo, Skill, Backend, Binding
├─ config/                      *Config + YAML loader + cross-entity validators
├─ store/                       SQLite schema, migrations, CRUD primitives
│
├─ workflow/                    event queue, processor, dispatcher, dispatch dedup
├─ scheduler/                   cron scheduler, agent memory (sqlite-backed)
├─ ai/                          prompt composition, CLI runner (stdin in, JSON out)
├─ backends/                    backend discovery (CLI probing, MCP health, model catalog)
├─ anthropic_proxy/             Anthropic↔OpenAI translation proxy
├─ observe/                     events/traces/graph/memory persistence + SSE hubs
│
├─ server/                      central HTTP server: lifecycle, router, /status, /run,
│   │                             proxy/UI/MCP mounts; cross-cutting types
│   ├─ fleet/                   /agents (CRUD + view + orphans), /skills, /backends
│   ├─ repos/                   /repos, /repos/{}/bindings
│   ├─ config/                  /config snapshot, /export, /import
│   └─ observe/                 /events, /traces, /graph, /dispatches, /memory + SSE
│
├─ webhook/                     /webhooks/github only — HMAC, delivery dedup, event parsing
├─ mcp/                         MCP (Model Context Protocol) server exposing fleet tools
├─ ui/                          embedded Next.js dist/, served at /ui/
└─ logging/, setup/             zerolog wiring, interactive setup
```

The tiers, from bottom to top:

**Domain (zero deps):** `fleet`, `config`, `store`. Pure data shapes and persistence. Anyone in the codebase can import these without dragging the world along. `fleet` has no transitive deps at all — it's just structs and pure functions like `NormalizeAgent`.

**Runtime engine:** `workflow`, `scheduler`, `ai`, `backends`, `anthropic_proxy`, `observe`. The actual fleet runtime. An event arrives on the queue, the processor pulls it, the engine looks up the right binding, the AI runner invokes the CLI, the response is parsed and traced, and any returned `dispatch` array is enqueued as new events.

**HTTP layer:** `server` and its sub-packages, plus `webhook`. Each domain handler exposes the same shape: a constructor and a `RegisterRoutes(router, withTimeout)` method. The central server doesn't import them — it accepts them through interfaces and calls `RegisterRoutes` on whatever's been wired.

**Entry:** `cmd/agents/main.go` constructs everything in order, hands handlers to the server, and calls `srv.Run(ctx)`.

## Composing root

`cmd/agents/main.go` is the only file in the codebase that knows how every piece fits. It reads roughly:

```go
// 1. domain layer
cfg, db, err := loadConfig(...)

// 2. runtime engine
engine := workflow.NewEngine(...)
sched := scheduler.NewScheduler(...)
processor := workflow.NewProcessor(...)
obs := observe.NewStore(db)

// 3. central HTTP server (knows nothing about domain handlers yet)
srv := server.NewServer(cfg, dataChannels, ..., logger)
srv.WithUI(ui.FS)
srv.WithStore(db, scheduler)
srv.WithWebhook(webhook.NewHandler(deliveryStore, dataChannels, srv, logger))

// 4. domain handlers — each constructed externally, then wired in
fleetHandler := serverfleet.New(srv, srv, ..., logger)
fleetHandler.SetDB(db)
fleetHandler.RefreshOrphansFromCfg(cfg)
srv.WithFleet(fleetHandler, dispatcher, fleetOrphansAdapter{...}, ...)

reposHandler := serverrepos.New(db, srv, srv, logger)
srv.WithRepos(reposHandler)

configHandler := serverconfig.New(srv, srv, logger)
configHandler.SetDB(db)
srv.WithConfig(configHandler)

srv.WithObserveRegister(func(r, withTimeout) { ... })

// 5. MCP — same handler instances satisfy MCP's writer interfaces
srv.WithMCP(mcpserver.New(mcpserver.Deps{
    AgentWrite:   fleetHandler,
    RepoWrite:    reposHandler,
    ConfigImport: configHandler,
    ...
}))

// 6. start everything
group.Go(srv.Run)
group.Go(processor.Run)
group.Go(scheduler.Run)
group.Wait()
```

The "external construction" pattern is deliberate. `internal/server/server.go` doesn't import any of `internal/server/{fleet,repos,config,observe}` or `internal/webhook`. That import boundary is what lets the central server move without dragging four sub-packages with it, and what keeps each domain handler independently testable.

## Cross-cutting glue

Two interfaces in `internal/server/types.go` carry most of the weight.

### `HandlerRegister`

```go
type HandlerRegister interface {
    RegisterRoutes(r *mux.Router, withTimeout func(http.Handler) http.Handler)
}
```

Every domain handler implements this. `server.Server` stores them as `HandlerRegister`, iterates in `buildHandler`. No type knowledge needed.

### `WriteCoordinator`

```go
type WriteCoordinator interface {
    Do(fn func() error) error
}
```

`server.Server.Do` implements it: holds `storeMu`, runs `fn` (the actual `store.UpsertX`), and on success runs the reload sequence — `store.ReadSnapshot` → `scheduler.Reload` → swap the in-memory `cfg` under `cfgMu` → refresh the orphan cache. Domain handlers (`server/fleet`, `server/repos`, `server/config`) all funnel CRUD writes through this single method, so cross-domain writes share one lock and one reload epoch. There is no second code path: REST and MCP both end here.

A third interface, `OrphansSource`, lets `/status` query the fleet handler's orphan cache without importing the fleet package — a small adapter in `cmd/agents` bridges the concrete type to the cross-package shape.

## How a request flows

Three traced examples, one per surface.

### `POST /webhooks/github` — issue labeled

```
mux router
  → server.Server.Handler() built once with all RegisterRoutes mounted
  → webhook.Handler.handleGitHubWebhook
      verifies HMAC SHA-256 against cfg.Daemon.HTTP.WebhookSecret
      dedupes by X-GitHub-Delivery (DeliveryStore TTL cache)
      parses payload, builds workflow.Event
      pushes onto channels.PushEvent
  ← 202 Accepted

async on the worker goroutine:
  workflow.Processor → workflow.Engine.HandleEvent
    → ai.Runner.Run launches the AI CLI subprocess
    → response parsed, observe writes trace + memory
    → any dispatch[] entries enqueued as agent.dispatch events
```

### `POST /agents` — CRUD create

```
mux router → server.Server (just routing)
  → server/fleet.Handler.HandleAgentsCreate
      coord.Do(func() error {
          return store.UpsertAgent(...)
      })
        ↓ acquires storeMu
        ↓ runs the upsert
        ↓ on success: server.reloadCron
            ↓ store.ReadSnapshot (one transaction)
            ↓ scheduler.Reload (cron rebinds)
            ↓ swap server.cfg under cfgMu
            ↓ fleet.RefreshOrphansFromCfg (orphan cache refreshes)
        ↑ releases storeMu
  ← canonical agent JSON
```

### `POST /mcp` — `update_agent` tool

```
mcp/server → toolUpdateAgent
  → fleet.Handler.UpdateAgentPatch
      (same instance as the REST surface — wired in cmd/agents)
  → coord.Do(func() error { return store.UpsertAgent(...) })
  ← same canonical shape as REST
```

REST and MCP converge at `coord.Do`. Hot-reload, orphan refresh, and lock discipline apply identically to both.

## Why this shape

Three constraints drove the layout.

**No torn writes across domains.** When a CRUD write changes (say) a binding, the cron scheduler and the in-memory routing config must both see the new state before the response goes out. `WriteCoordinator` makes this the *only* way to write — every domain handler has to funnel through it.

**REST and MCP must not drift.** The MCP tool surface is built on the same handler instances the HTTP router uses. Any code path that updates an agent goes through `fleet.Handler.UpsertAgent` regardless of how it was triggered. There is structurally no place for the surfaces to disagree.

**Domain handlers must be independently understandable.** `server/fleet` only knows about agents, skills, and backends. `server/repos` only knows about repos and bindings. They share a write lock through an interface and otherwise never reference each other. Adding a new domain (say, a `server/billing` if this becomes a SaaS) means adding one package, one constructor, one `RegisterRoutes` call in `main.go`. Nothing in the existing handlers needs to change.

The result is that the central server is small (one file, ~500 lines), each domain handler is a self-contained package, and the composition root is the only place where the picture is assembled.

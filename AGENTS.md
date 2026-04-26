# AGENTS.md

Repo-specific guidance for any coding agent (Claude Code, Codex, Cursor, Aider, …) working in this project. Cross-references [`CLAUDE.md`](CLAUDE.md) for tool-specific notes and [`docs/local-models.md`](docs/local-models.md) for the local-model integration story.

## What this repo is

**agents** is a self-hosted Go daemon that dispatches AI CLIs (Claude Code, Codex, or any CLI pointed at a local LLM via the built-in proxy) to work on GitHub repos. Agents are declared in YAML, bound to repos via **labels**, **GitHub event subscriptions**, or **cron schedules**, and execute inside the AI CLI — which in turn uses GitHub MCP tools for all writes. The daemon itself is strictly read-only against GitHub.

Agents can also invoke each other at runtime via the **reactive inter-agent dispatcher**: an agent returns `dispatch: [{agent, number, reason}]` in its response JSON, the daemon validates against per-agent whitelists and safety limits, then enqueues a synthetic `agent.dispatch` event that runs the target agent.

Key numbers:
- Language: **Go 1.25** (check `go.mod`).
- Binary entrypoint: `cmd/agents/main.go`.
- Single-binary deployment; no required runtime dependencies beyond the AI CLIs (GitHub access flows through the GitHub MCP server configured on each AI CLI).

## Quick commands

```bash
go test ./... -race                             # run all tests
go build -o agents ./cmd/agents                 # build the daemon
./agents --db agents.db --import config.yaml    # import config + start
./agents --db agents.db                         # start (after import)
./agents --db agents.db \                       # one-shot synchronous pass
  --run-agent <agent-name> --repo owner/repo   # (drains any dispatch chain)
docker compose up -d                            # containerised run
```

## Code map (current)

```
cmd/agents/main.go              # wires config, logger, runners, scheduler, webhook server, proxy
internal/
  config/                       # YAML parsing, defaults, validation, prompt/skill file resolution
  ai/                           # prompt composition + CLI runner (hardcoded backend args + schema enforcement)
  anthropic_proxy/              # built-in Anthropic Messages ↔ OpenAI Chat Completions translation
  observe/                      # observability store: events, traces, dispatch graph, SSE hubs
  autonomous/                   # cron scheduler + agent memory (SQLite-backed)
  backends/                     # backend discovery: CLI probing, GitHub MCP health checks, orphan detection
  store/                        # SQLite-backed config store: Open, Import, Load, CRUD
  workflow/                     # event routing engine (single event queue), processor, dispatcher
  server/                       # shared HTTP server types (cross-cutting interfaces, error sentinels)
  server/observe/               # observability HTTP handlers (events, traces, graph, dispatches, memory, SSE)
  webhook/                      # HTTP server, HMAC signature verification, delivery dedupe, CRUD API handlers
  mcp/                          # MCP server exposing fleet-management tools at /mcp
  ui/                           # embedded Next.js web dashboard (static assets served at /ui/)
  setup/                        # interactive first-time setup command
  logging/                      # zerolog configuration
docs/local-models.md            # full recipe for running the fleet on a local LLM
config.example.yaml             # shipping example, kept in sync with config schema
internal/ai/response-schema.json # embedded JSON schema for structured output (codex --output-schema)
```

## Conceptual model

- **Agent** — a named capability: `backend` + `skills: []` + `prompt`. An agent is a pure definition. It does not run by itself. Prompts are stored in SQLite (seeded via `--import` from YAML, or created directly in the UI).
- **Skill** — a reusable chunk of guidance referenced by name in multiple agents. Skill text is concatenated before the agent's own prompt at render time.
- **Binding** — `repos[*].use[*]`: pairs one agent with exactly one trigger (`labels:`, `events:`, or `cron:`). The same agent can have multiple bindings on the same repo with different triggers.
- **Backend** — explicit backend selection per agent (no `auto`). Built-ins are `claude` and `codex`; additional named local backends are supported via `local_model_url`.
- **Proxy** — optional in-daemon Anthropic↔OpenAI translator mounted at `/v1/messages` and `/v1/models`. Disabled by default. When enabled, set `local_model_url` on the backend entry to the proxy's URL; the daemon injects `ANTHROPIC_BASE_URL` for that backend automatically.
- **Dispatcher** — the runtime mechanism by which agents invoke each other. See "Reactive dispatch" below.

## Reactive dispatch — the model you must keep in mind

- Each agent's YAML may declare `allow_dispatch: true` (opt-in as a target) and `can_dispatch: [name, ...]` (whitelist of targets).
- A target named in any `can_dispatch` list must also declare a `description` — it is rendered into the originating agent's prompt as part of an `## Available experts` roster.
- An agent's response JSON may include a `dispatch: []` array. Each element names a target and a reason.
- The dispatcher validates every request against: whitelist match, target's opt-in, chain depth, fan-out per run, and a dedup window keyed on `(target_agent, repo, number)`. Safety limits live under `daemon.processor.dispatch.{max_depth, max_fanout, dedup_window_seconds}` and all three must be positive integers.
- Accepted requests are enqueued as synthetic `agent.dispatch` events with payload fields `target_agent`, `reason`, `invoked_by`, `root_event_id`, `dispatch_depth`. They flow through the same single event queue as webhook events and cron-fired events.
- Rejection modes log at `WARN` (whitelist/opt-in/depth/fanout breaches) or `DEBUG` (dedup skip).

## Behavioral guardrails

These constraints are load-bearing. Read them before changing the listed areas.

- **The daemon never writes to GitHub directly.** All writes go through the AI backend's MCP tools. If you introduce a new feature that seems to need a direct GitHub API call, raise it in an issue first — there's almost always a way to keep the daemon read-only.
- **Agents must not mention external GitHub users.** Do NOT request reviews from, assign to, or @mention any GitHub user in PRs, comments, or issue descriptions. All review routing is handled by the daemon's dispatch system. Unsolicited pings to external contributors from an automated agent are a trust and reputation risk — the GitHub account could be flagged. This rule applies to every agent prompt.
- **Prompts are never logged in plaintext.** Only their salted hash and length are recorded. If you add new log lines near prompt handling, preserve this property.
- **Structured output is enforced at the CLI level.** Claude uses hardcoded `--output-format stream-json --json-schema <embedded-schema>` args; codex uses hardcoded `--output-schema <temp-file>`. The daemon embeds `internal/ai/response-schema.json` and appends the correct flags automatically. When changing the response contract, update `internal/ai/response-schema.json` alongside `internal/ai/types.go`.
- **The runner contract is stdin-in, single-JSON-object-out.**
  - `internal/ai/cmdrunner.go` sends the composed prompt on stdin and parses the last top-level JSON object from stdout.
  - Agents emit `{"summary": "...", "artifacts": [...], "dispatch": [...], "memory": "..."}`. `dispatch` and `memory` are optional fields but all four keys are present in the schema. A missing JSON object, an empty response, or a response where `summary`, `artifacts`, and `dispatch` are all empty fails the run with a clear error.
  - `memory` is the agent's full updated memory state. The daemon writes it back to the SQLite store after each autonomous run. An empty string clears the memory. Event-driven runs do not receive or persist memory.
  - Small prose outputs with no JSON are an agent-prompt issue, not a runner bug — don't relax the parser to cover them; fix the prompt.
- **Subprocess env is filtered.** `internal/ai/cmdrunner.go::allowCommandEnvKey` is an explicit allowlist. When adding a new env-var-driven integration, add the variable to the allowlist **and** document why (see `ANTHROPIC_BASE_URL` / `OPENAI_*` for precedent).
- **Backend args are daemon-managed.** User/runtime edits are limited to `timeout_seconds`, `max_prompt_chars`, and (for local backends) `local_model_url`. Do not reintroduce user-configurable runner args.
- **Model pinning safety.** Config may contain pinned models that become unavailable after discovery. These agents are treated as orphaned in diagnostics/UI and fail fast at runtime until remapped or cleared.
- **Dispatch validation is belt-and-braces.** `can_dispatch` is validated at config load time (targets must exist, no self-reference, targets require `description`). Runtime validation in `internal/workflow/dispatch.go` enforces the same invariants again so config-only checks can't be bypassed by agent-generated dispatch requests.
- **Webhook HMAC verification runs before any parsing.** Don't read the body before verifying the signature.

## Editing checklist

When making common classes of changes, update all of these at once:

| Change | Update |
|---|---|
| Config schema (types in `internal/config/config.go`) | Validation, `normalize()`, defaults, `config.example.yaml`, README, tests in `internal/config/config_test.go` |
| New webhook event kind | Decoder in `internal/webhook/server.go`, acceptance in `internal/workflow/engine.go`, README event table, validation in `internal/config/config.go` |
| New AI backend behavior | `internal/ai/cmdrunner.go`, allowlist if new env vars, backend registration in `cmd/agents/main.go`, config example |
| Agent prompt contract | Prompts in SQLite (edit via UI or CRUD API), runner parser in `internal/ai/cmdrunner.go`, `internal/ai/types.go`, `internal/ai/response-schema.json`, AGENTS.md runner-contract section, tests |
| Memory contract | `internal/autonomous/memory.go` (MemoryBackend interface), `internal/store/store.go` (SQLite path), `cmd/agents/main.go` (wiring), agent prompts "Memory hygiene" sections, `internal/ai/types.go` |
| Dispatch semantics | `internal/workflow/dispatch.go` (runtime), `internal/config/config.go` (load-time validation), agent response schema in `internal/ai/types.go`, README dispatch section, all prompt "Response format" sections, tests on both paths |
| SQLite store schema | `internal/store/migrations/`, `internal/store/store.go`, `internal/store/crud.go`, `internal/webhook/crud.go`, tests |
| Proxy translation behavior | `internal/anthropic_proxy/{types,translate,handler}.go`, unit tests for the affected shape, `docs/local-models.md` if user-visible |
| Anything in the README | Also check `CLAUDE.md`, `AGENTS.md`, `config.example.yaml` — these four should stay in sync |

## Testing expectations

- **Run `go test ./... -race` before every commit.** Race detection is cheap and catches real bugs in the concurrent event processing and dispatch paths.
- **Table-driven tests** for anything with more than two interesting input shapes (config validation, label parsing, event decoding, translation, dispatch rejection reasons). `t.Parallel()` where independent; **not** when using `t.Setenv`.
- **Use `httptest.Server` for HTTP integration tests.** See `internal/webhook/server_test.go` and `internal/server/observe/observe_test.go` for the patterns.
- **No `-short` or skipped tests on main.** If a test needs external services, gate it behind a build tag or an explicit env var check.
- **Do not mock what you do not own.** Wrap third-party clients behind an interface you control, then mock that interface. `internal/ai.Runner` is the canonical example.
- **Test error paths, not just the happy path.** Dispatch rejection modes each deserve a dedicated test.
- **No commented-out or skipped tests on main.** Delete them or fix them.

## Operational notes

- **`.env` is auto-loaded on startup** (`godotenv.Load()`). Required runtime secret: `GITHUB_WEBHOOK_SECRET`. Optional: `LOG_SALT`.
- **Config is loaded from SQLite at startup.** Use `--import config.yaml` to seed the database, then manage changes via the CRUD API or the web dashboard. Prompt and skill content is stored in the database; changes via the API or UI take effect on the next agent run without a restart.
- **Backend discovery lifecycle.** Startup auto-discovery runs only when the backends table is empty. Manual refresh is explicit via `POST /backends/discover`; `GET /backends/status` is diagnostics-only.
- **Orphan visibility.** `GET /agents/orphans/status` and `/status` (`orphaned_agents.count`) expose model/backend drift requiring user remediation.
- **Autonomous agent memory** is stored in SQLite (in the `memory` table), keyed by `(agent, repo)`. It's the agent's job to return updated memory in its response; the daemon writes it back to the store unchanged.
- **Dispatch dedup is process-local and in-memory.** It's shared across cron-fired runs, event-fired runs, and `--run-agent` invocations within one process. Restarting the daemon clears the dedup state.
- **`--run-agent` drains dispatch chains synchronously.** When invoking an agent on demand via the CLI flag, the process waits for the originating agent and all dispatched children to finish before exiting. The in-memory event queue is sized to hold `MaxFanout^MaxDepth` events so deep chains don't silently drop.
- **Avoid `--no-verify` on commits.** Pre-commit hooks exist for a reason. If a hook fails, fix the underlying issue.
- **The `ports: "8080:8080"` in `docker-compose.yaml`** publishes the daemon port on the host. In production, consider restricting access via a reverse proxy (e.g. Traefik with basic auth) or binding to `127.0.0.1:8080:8080`.

## Common anti-patterns to avoid

- **Parsing Markdown / prose responses to extract structured data.** The runner contract is JSON. If you need more structure, extend the JSON schema.
- **Relying on GitHub state in the middle of an agent run.** GitHub is eventually consistent. If you need a claim about GitHub state (e.g. "PR is merged"), fetch it again via the agent's tool call just before asserting, don't rely on memory.
- **Making the daemon's event queue depth dependent on backend response time.** The queue and the workers are decoupled on purpose. Slow backends should accumulate queue depth, not block new events from arriving.
- **Spawning new goroutines inside an agent run that outlive the parent context.** Respect context cancellation so shutdown drains cleanly.
- **Dispatching to an agent the originator doesn't know about.** Any dispatch entry whose `agent` isn't in the originator's `can_dispatch` list is dropped with a WARN. Agents should only name targets they see in their `## Available experts` roster.
- **Fabricating facts inside populated response templates.** Less-capable local models will happily populate a "post status comment in this format" template with invented values (non-existent SHAs, false merge states). If you write prompts that use templated outputs, add verification steps — require SHAs to be fetched live within the same run, require CI status to be fetched via the GitHub MCP server's workflow-run tools, and so on.

## Local-model routing

The daemon can route the `claude` CLI through its built-in proxy to any OpenAI-compatible backend without a sidecar process. Relevant when:

- A user wants to run part of their fleet on a local LLM for privacy or cost reasons.
- A test needs to exercise a specific model's behaviour against the full Claude Code tool stack.

Pattern: two backend entries using the same `claude` binary, different `local_model_url` values. See [`docs/local-models.md`](docs/local-models.md) for the full recipe, measured performance numbers, VRAM-tier model recommendations, and honest caveats about the disposition gap between Claude and local models on action-taking agents.

When contributing in this area:
- Proxy changes live in `internal/anthropic_proxy/`. Keep translation rules pure (no I/O in the `translate*` functions) and test them directly.
- Local-model routing uses `local_model_url` and is translated into `ANTHROPIC_BASE_URL` at runner construction time (`cmd/agents/main.go::backendEnvOverrides`).
- Don't assume local models behave like Claude. They are more cautious with write tools and more prone to hallucinating facts inside templated outputs. Design agent prompts and guardrails for the least capable backend you want to support.

## Contribution model

The project accepts both issues and pull requests from external contributors. The agent fleet runs alongside human contributors, but only acts on items the maintainer has explicitly authorised. See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the full flow. Key points for agents working in this repo:

- The `ai ready` label is the maintainer's authorisation. Agents only pick up issues and PRs that carry this label. No `ai ready`, no agent action.
- Do NOT push directly to main. Always create a branch and open a PR.
- High-priority `ai ready` issues are additionally labelled `high priority` and must be processed first.
- Agent-authored PRs are reviewed by the pr-reviewer agent. On approval, pr-reviewer applies the `human review ready` label, signalling a human can merge.

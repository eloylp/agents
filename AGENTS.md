# AGENTS.md

Repo-specific guidance for any coding agent (Claude Code, Codex, Cursor, Aider, …) working in this project. Cross-references [`CLAUDE.md`](CLAUDE.md) for tool-specific notes and [`docs/local-models.md`](docs/local-models.md) for the local-model integration story.

## What this repo is

**agents** is a self-hosted Go daemon that dispatches AI CLIs (Claude Code, Codex, or any CLI pointed at a local LLM via the built-in proxy) to work on GitHub repos. Agents are declared in YAML, bound to repos via **labels**, **GitHub event subscriptions**, or **cron schedules**, and execute inside the AI CLI — which in turn uses GitHub MCP tools for all writes. The daemon itself is strictly read-only against GitHub.

Agents can also invoke each other at runtime via the **reactive inter-agent dispatcher**: an agent returns `dispatch: [{agent, number, reason}]` in its response JSON, the daemon validates against per-agent whitelists and safety limits, then enqueues a synthetic `agent.dispatch` event that runs the target agent.

Key numbers:
- Language: **Go 1.24** (check `go.mod`).
- Binary entrypoint: `cmd/agents/main.go`.
- Single-binary deployment; no required runtime dependencies beyond the AI CLI and `gh`.

## Quick commands

```bash
go test ./... -race                             # run all tests
go build -o agents ./cmd/agents                 # build the daemon
go run ./cmd/agents -config config.yaml         # start the daemon
./agents -config config.yaml \                  # one-shot synchronous pass
  --run-agent <agent-name> --repo owner/repo   # (drains any dispatch chain)
docker compose up -d                            # containerised run
```

## Code map (current)

```
cmd/agents/main.go              # wires config, logger, runners, scheduler, webhook server, proxy
internal/
  config/                       # YAML parsing, defaults, validation, prompt/skill file resolution
  ai/                           # prompt composition + CLI runner (supports per-backend env overrides)
  anthropic_proxy/              # built-in Anthropic Messages ↔ OpenAI Chat Completions translation
  autonomous/                   # cron scheduler + per-agent/per-repo markdown memory
  workflow/                     # event routing engine (single event queue), processor, dispatcher
  webhook/                      # HTTP server, HMAC signature verification, delivery dedupe
  setup/                        # interactive first-time setup command
  logging/                      # zerolog configuration
prompts/                        # prompt files referenced by agent prompt_file:
skills/                         # skill files referenced by skill prompt_file:
docs/local-models.md            # full recipe for running the fleet on a local LLM
config.example.yaml             # shipping example, kept in sync with config schema
```

## Conceptual model

- **Agent** — a named capability: `backend` + `skills: []` + `prompt` (or `prompt_file`). An agent is a pure definition. It does not run by itself.
- **Skill** — a reusable chunk of guidance referenced by name in multiple agents. Skill text is concatenated before the agent's own prompt at render time.
- **Binding** — `repos[*].use[*]`: pairs one agent with exactly one trigger (`labels:`, `events:`, or `cron:`). The same agent can have multiple bindings on the same repo with different triggers.
- **Backend** — one of `claude`, `codex`, or `auto` (picks the first configured in preference order). Two separate backend entries can point at the **same CLI binary** with different `env:` — this is the mechanism for routing the `claude` CLI through the built-in proxy to a local LLM.
- **Proxy** — optional in-daemon Anthropic↔OpenAI translator mounted at `/v1/messages` and `/v1/models`. Disabled by default. When enabled, the `claude` CLI can be pointed at it via `ANTHROPIC_BASE_URL` in the backend's `env:` map.
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
- **Prompts are never logged in plaintext.** Only their salted hash and length are recorded. If you add new log lines near prompt handling, preserve this property.
- **The runner contract is stdin-in, single-JSON-object-out.**
  - `internal/ai/cmdrunner.go` sends the composed prompt on stdin and parses the last top-level JSON object from stdout.
  - Agents emit `{"summary": "...", "artifacts": [...], "dispatch": [...]}`. `dispatch` is optional. A missing JSON object, an empty response, or a response with all three fields empty fails the run with a clear error.
  - Small prose outputs with no JSON are an agent-prompt issue, not a runner bug — don't relax the parser to cover them; fix the prompt.
- **Subprocess env is filtered.** `internal/ai/cmdrunner.go::allowCommandEnvKey` is an explicit allowlist. When adding a new env-var-driven integration, add the variable to the allowlist **and** document why (see `ANTHROPIC_BASE_URL` / `OPENAI_*` for precedent).
- **Backend config supports per-backend `env` overrides.** When introducing a feature that requires environment variables for a specific backend, use the `AIBackendConfig.Env` map instead of the global container env — it keeps the container config clean and lets users define multiple backends pointing at different endpoints with the same CLI.
- **Dispatch validation is belt-and-braces.** `can_dispatch` is validated at config load time (targets must exist, no self-reference, targets require `description`). Runtime validation in `internal/workflow/dispatch.go` enforces the same invariants again so config-only checks can't be bypassed by agent-generated dispatch requests.
- **Webhook HMAC verification runs before any parsing.** Don't read the body before verifying the signature.

## Editing checklist

When making common classes of changes, update all of these at once:

| Change | Update |
|---|---|
| Config schema (types in `internal/config/config.go`) | Validation, `normalize()`, defaults, `config.example.yaml`, README, tests in `internal/config/config_test.go` |
| New webhook event kind | Decoder in `internal/webhook/server.go`, acceptance in `internal/workflow/engine.go`, README event table, validation in `internal/config/config.go` |
| New AI backend behavior | `internal/ai/cmdrunner.go`, allowlist if new env vars, backend registration in `cmd/agents/main.go`, config example |
| Agent prompt contract | Prompt templates in `prompts/`, runner parser in `internal/ai/cmdrunner.go`, AGENTS.md runner-contract section, tests |
| Dispatch semantics | `internal/workflow/dispatch.go` (runtime), `internal/config/config.go` (load-time validation), agent response schema in `internal/ai/types.go`, README dispatch section, all prompt "Response format" sections, tests on both paths |
| Proxy translation behavior | `internal/anthropic_proxy/{types,translate,handler}.go`, unit tests for the affected shape, `docs/local-models.md` if user-visible |
| Anything in the README | Also check `CLAUDE.md`, `AGENTS.md`, `config.example.yaml` — these four should stay in sync |

## Testing expectations

- **Run `go test ./... -race` before every commit.** Race detection is cheap and catches real bugs in the concurrent event processing and dispatch paths.
- **Table-driven tests** for anything with more than two interesting input shapes (config validation, label parsing, event decoding, translation, dispatch rejection reasons). `t.Parallel()` where independent; **not** when using `t.Setenv`.
- **Use `httptest.Server` for HTTP integration tests.** See `internal/webhook/server_test.go` and `internal/anthropic_proxy/handler_test.go` for the patterns.
- **No `-short` or skipped tests on main.** If a test needs external services, gate it behind a build tag or an explicit env var check.
- **Do not mock what you do not own.** Wrap third-party clients behind an interface you control, then mock that interface. `internal/ai.Runner` is the canonical example.
- **Test error paths, not just the happy path.** Dispatch rejection modes each deserve a dedicated test.
- **No commented-out or skipped tests on main.** Delete them or fix them.

## Operational notes

- **`.env` is auto-loaded on startup** (`godotenv.Load()`). Required runtime secret: `GITHUB_WEBHOOK_SECRET`. Optional: `AGENTS_API_KEY`, `LOG_SALT`.
- **Config is read once at daemon startup.** Changing `config.yaml` or any `prompt_file` / skill file requires a daemon restart. If you're testing prompt changes interactively, expect to `docker compose restart agents`.
- **Autonomous agent memory** lives under `daemon.memory_dir` (default `/var/lib/agents/memory`), as one markdown file per `(agent, repo)` pair. It's the agent's job to update it in its response; the daemon just reads/writes the file unchanged.
- **Dispatch dedup is process-local and in-memory.** It's shared across cron-fired runs, event-fired runs, and `--run-agent` invocations within one process. Restarting the daemon clears the dedup state.
- **`--run-agent` drains dispatch chains synchronously.** When invoking an agent on demand via the CLI flag, the process waits for the originating agent and all dispatched children to finish before exiting. The in-memory event queue is sized to hold `MaxFanout^MaxDepth` events so deep chains don't silently drop.
- **Avoid `--no-verify` on commits.** Pre-commit hooks exist for a reason. If a hook fails, fix the underlying issue.
- **The `expose: 8080` in `docker-compose.yml` is deliberate** — the daemon port is only reachable inside the docker network (via `traefik` at the labelled host). Don't publish it publicly by default.

## Common anti-patterns to avoid

- **Parsing Markdown / prose responses to extract structured data.** The runner contract is JSON. If you need more structure, extend the JSON schema.
- **Relying on GitHub state in the middle of an agent run.** GitHub is eventually consistent. If you need a claim about GitHub state (e.g. "PR is merged"), fetch it again via the agent's tool call just before asserting, don't rely on memory.
- **Making the daemon's event queue depth dependent on backend response time.** The queue and the workers are decoupled on purpose. Slow backends should accumulate queue depth, not block new events from arriving.
- **Spawning new goroutines inside an agent run that outlive the parent context.** Respect context cancellation so shutdown drains cleanly.
- **Dispatching to an agent the originator doesn't know about.** Any dispatch entry whose `agent` isn't in the originator's `can_dispatch` list is dropped with a WARN. Agents should only name targets they see in their `## Available experts` roster.
- **Fabricating facts inside populated response templates.** Less-capable local models will happily populate a "post status comment in this format" template with invented values (non-existent SHAs, false merge states). If you write prompts that use templated outputs, add verification steps — require SHAs to be fetched live within the same run, require CI status to be fetched via `gh run view`, and so on.

## Local-model routing

The daemon can route the `claude` CLI through its built-in proxy to any OpenAI-compatible backend without a sidecar process. Relevant when:

- A user wants to run part of their fleet on a local LLM for privacy or cost reasons.
- A test needs to exercise a specific model's behaviour against the full Claude Code tool stack.

Pattern: two backend entries using the same `claude` binary, different `env:` maps. See [`docs/local-models.md`](docs/local-models.md) for the full recipe, measured performance numbers, VRAM-tier model recommendations, and honest caveats about the disposition gap between Claude and local models on action-taking agents.

When contributing in this area:
- Proxy changes live in `internal/anthropic_proxy/`. Keep translation rules pure (no I/O in the `translate*` functions) and test them directly.
- Per-backend env overrides live on `AIBackendConfig.Env` and are merged after the host-env allowlist in `buildCommandEnv`.
- Don't assume local models behave like Claude. They are more cautious with write tools and more prone to hallucinating facts inside templated outputs. Design agent prompts and guardrails for the least capable backend you want to support.

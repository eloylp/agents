# Agents

![agents](docs/agents.jpg)

**A self-hosted Go daemon that turns GitHub into an AI-driven development pipeline.**

Define your agents once. Wire them to repos with labels, cron schedules, or both. The daemon dispatches them via AI CLIs ([Claude Code](https://docs.anthropic.com/en/docs/claude-code), [Codex](https://github.com/openai/codex)) and lets them work through native GitHub primitives — issues, PRs, reviews, comments.

---

## Why?

- **Self-hosted, no SaaS** — your code and prompts stay on your infrastructure.
- **Multi-backend** — Claude, Codex, or any CLI that speaks MCP. Pick different backends for different agents.
- **One agent model, many triggers** — label events, cron schedules, on-demand API calls. Same agent, wired however you want.
- **Composable skills** — reusable guidance blocks (architecture, security, testing, DX, …) merged into any agent.
- **Transparent** — every agent action is a GitHub comment, issue, or PR. Reviewable. Revertable.
- **Secure by default** — HMAC-verified webhooks, API-key-gated trigger endpoint, hashed prompt logs, no direct GitHub writes from the daemon.

---

## How it works

```mermaid
sequenceDiagram
    actor Dev as Developer
    participant GH as GitHub
    participant D as agents
    participant AI as AI Backend<br/>(Claude / Codex)

    Note over D: Two trigger kinds
    Dev->>GH: Add label  ai:review:arch-reviewer
    GH->>D: Webhook (pull_request:labeled)
    Note over D: Verify signature,<br/>deduplicate delivery,<br/>match repo binding,<br/>queue event

    Note over D: ... or scheduled ...
    D-->>D: Cron fires (e.g. hourly)

    D->>AI: Compose prompt = skills + agent prompt + context
    AI->>GH: Read context via MCP tools
    AI->>GH: Post comment / review / open PR via MCP tools
    AI-->>D: Return artifacts JSON
    Note over D: Persist agent memory<br/>for next scheduled run
```

The daemon is event-driven for label-based workflows and runs a cron scheduler for autonomous agents. Both paths resolve to the same agent definitions — only the trigger differs.

---

## Configuration at a glance

The config file is split into four conceptual domains:

```yaml
daemon:    # how the service runs: log, http, processor, backends, optional proxy
skills:    # reusable guidance blocks, keyed by name
agents:    # named capabilities: backend + skills + prompt + dispatch wiring
repos:     # wiring: which agents run on which repo, and when
```

Full walkthrough below. The shortest useful config is ~30 lines.

---

## Label architecture

Labels are plain strings matched against each binding's `labels` list. There is **no magic format** — you choose the labels. Convention across the example config is `ai:review:<agent-name>`, but any string works.

```yaml
repos:
  - name: "eloylp/myrepo"
    enabled: true
    use:
      # Label-triggered reviewer
      - agent: arch-reviewer
        labels: ["ai:review:arch-reviewer"]

      # Multiple agents firing on the same label (fan-out)
      - agent: arch-reviewer
        labels: ["ai:review:all"]
      - agent: sec-reviewer
        labels: ["ai:review:all"]

      # Cron-scheduled agent on the same repo
      - agent: coder
        cron: "0,30 8-18 * * *"

      # Event-triggered agent (react to any new comment)
      - agent: coder
        events: ["issue_comment.created"]
```

Rules:

- Labels are case-insensitive and trimmed. Only `labeled` actions fire (not `unlabeled`).
- The trigger label comes from the webhook event payload, not the issue/PR's current label set.
- Draft PRs skip `pull_request.labeled` for both `labels:` and `events:` bindings; they may still receive other event kinds such as `pull_request.opened` and `pull_request.synchronize`.
- `events:` bindings fire on the exact event kinds listed, with no additional filtering.
- Multiple bindings matching the same event fan out in parallel (capped by `daemon.processor.max_concurrent_agents`).

---

## Requirements

| Dependency | Purpose |
|---|---|
| **Go 1.22+** | Build the daemon |
| **GitHub CLI** (`gh`) | Authenticated access used by the AI CLIs' GitHub MCP tools |
| **AI CLI** (Claude Code and/or Codex) | The actual AI backend, with GitHub MCP server configured |

> **Why `gh` when the daemon never calls it?** The daemon only spawns the AI CLI and passes a prompt. The CLI uses GitHub MCP tools to read and write; those tools rely on `gh` authentication under the hood.

### Setup

```bash
# GitHub CLI
brew install gh
gh auth login
```

Then follow the official setup guides:
- [Claude Code](https://code.claude.com/docs/en/setup) + [GitHub MCP](https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-claude.md)
- [Codex](https://github.com/openai/codex) + [GitHub MCP](https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-codex.md)

---

## Configuration

Copy `config.example.yaml` to `config.yaml` and adapt it.

### `daemon` — how the service runs

```yaml
daemon:
  log:
    level: info            # trace, debug, info, warn, error, fatal
    format: text           # text (human) or json (structured)

  http:
    listen_addr: ":8080"
    status_path: /status
    webhook_path: /webhooks/github
    webhook_secret_env: GITHUB_WEBHOOK_SECRET
    shutdown_timeout_seconds: 15

  processor:
    event_queue_buffer: 256
    max_concurrent_agents: 4                # cap on per-event fan-out

  dispatch:
    max_depth: 3                            # max chain length before drop + WARN
    max_fanout: 4                           # max dispatches per single agent run
    dedup_window_seconds: 300               # suppress duplicate (target, repo, number) within window

  memory_dir: /var/lib/agents/memory        # persistent autonomous agent memory

  ai_backends:
    claude:
      command: claude
      args: ["-p", "--dangerously-skip-permissions"]
      timeout_seconds: 1500
      max_prompt_chars: 12000
      redaction_salt_env: LOG_SALT

    codex:
      command: codex
      args: ["exec", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox"]
      timeout_seconds: 600
      max_prompt_chars: 12000
      redaction_salt_env: LOG_SALT
```

### `skills` — reusable guidance blocks

```yaml
skills:
  architect:
    prompt: |
      Focus on architecture boundaries, coupling, extensibility, and maintainability risks.

  security:
    prompt: |
      Focus on authn/authz, secrets exposure, injection vectors, and unsafe defaults.
```

Skills are referenced by name from agents. You can also use `prompt_file: path/to/file.md` instead of inline `prompt`.

### `agents` — named capabilities

```yaml
agents:
  # Short inline prompt — reviewer that never opens PRs (default)
  - name: arch-reviewer
    backend: auto              # auto | claude | codex
    skills: [architect]
    prompt: |
      You are an architecture-focused PR reviewer. Post one high-signal review comment.

  # Prompt loaded from a file (recommended for longer prompts)
  # allow_prs: true lets this agent open pull requests
  - name: coder
    backend: claude
    skills: [architect, testing]
    prompt_file: prompts/coder.md
    allow_prs: true            # required for agents that open PRs

  # Dispatch target — can be invoked by pr-reviewer
  - name: sec-reviewer
    description: "Deep-dive security reviewer for risky changes"
    backend: claude
    allow_dispatch: true       # opt-in to being dispatched
    prompt_file: prompts/sec-reviewer.md

  # Agent that may dispatch to sec-reviewer
  - name: pr-reviewer
    backend: claude
    can_dispatch: [sec-reviewer]   # whitelist of agents this agent may dispatch
    prompt_file: prompts/pr-reviewer.md
```

Each agent is a pure capability definition: backend + skills + prompt. Agents don't run until a repo binds them to a trigger.

- `backend: auto` picks the first configured backend in `daemon.ai_backends` (claude before codex).
- `prompt_file` paths are resolved relative to the config file's directory.
- Agent names must be unique.
- `allow_prs` (default `false`) — when `false`, the scheduler prepends a hard instruction
  forbidding the agent from opening pull requests, regardless of what the prompt says. Set
  `allow_prs: true` only on agents that are explicitly meant to author PRs (e.g. coders,
  refactorers). Reviewer-only agents should leave this unset.
- `allow_dispatch` (default `false`) — opt-in gate. An agent must have `allow_dispatch: true`
  for any other agent to dispatch it. Agents without this flag silently drop any incoming
  dispatch requests.
- `can_dispatch` — whitelist of agent names this agent is allowed to dispatch. A dispatch
  to an agent not on this list is silently dropped. Entries must reference real agents in
  the same config and must not include the agent itself.
- `description` — required when an agent appears in any `can_dispatch` list. Used by the
  dispatcher to include context about the target in the originating agent's prompt roster.

### `repos` — wiring

```yaml
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: arch-reviewer
        labels: ["ai:review:arch-reviewer"]

      - agent: coder
        cron: "0,30 8-18 * * *"             # standard 5-field cron

      - agent: nightly-scout
        cron: "0 7 * * *"
        enabled: false                       # temporarily off without deletion
```

Each `use` entry binds one agent to one trigger. An agent can appear multiple times with different triggers. A binding must have at least one of `labels:`, `events:`, or `cron:`.

```yaml
repos:
  - name: "owner/repo"
    enabled: true
    use:
      # Label-triggered reviewer
      - agent: arch-reviewer
        labels: ["ai:review:arch-reviewer"]

      # React to every new issue comment (issues and PRs alike)
      - agent: coder
        events: ["issue_comment.created"]

      # React to new commits pushed to any branch
      - agent: sec-reviewer
        events: ["push"]

      # Multiple event kinds in one binding (fan-out fires the agent once per match)
      - agent: pr-reviewer
        events: ["pull_request.opened", "pull_request.synchronize"]
```

A binding must set exactly one of `labels:`, `events:`, or `cron:` — mixing trigger types in a single binding is rejected at startup.

#### Supported event kinds

The `events:` field accepts any of the following GitHub event kinds. Each event delivers a `## Runtime context` block into the agent's prompt with `Event`, `Actor` (the GitHub login that triggered it), an issue/PR number where applicable, and the payload fields listed below.

| Kind | When | Payload fields |
|------|------|----------------|
| `issues.labeled` | Issue receives any label | `label` |
| `issues.opened` | Issue opened | `title`, `body` |
| `issues.edited` | Issue body or title edited | `title`, `body` |
| `issues.reopened` | Issue reopened | `title`, `body` |
| `issues.closed` | Issue closed | `title`, `body` |
| `pull_request.labeled` | PR receives any label (draft PRs are skipped) | `label` |
| `pull_request.opened` | PR opened | `title`, `draft` |
| `pull_request.synchronize` | New commit pushed to PR branch | `title`, `draft` |
| `pull_request.ready_for_review` | Draft PR marked ready | `title`, `draft` |
| `pull_request.closed` | PR closed or merged | `title`, `draft`, `merged` (`true` when PR was merged, `false` when closed without merge) |
| `issue_comment.created` | Comment posted on an issue or PR | `body` |
| `pull_request_review.submitted` | Formal GitHub review submitted | `state`, `body` |
| `pull_request_review_comment.created` | Inline review comment posted on a PR diff | `body` |
| `push` | Commit pushed to a branch | `ref` (e.g. `refs/heads/main`), `head_sha` |
| `agents.run` | On-demand trigger via `POST /run` or `--run-agent` CLI | `target_agent` |
| `agent.dispatch` | Another agent dispatched this agent | `target_agent`, `reason`, `root_event_id`, `dispatch_depth`, `invoked_by` |

> **`push` scope:** only branch pushes fire the event. Tag pushes, branch deletions, and pushes to non-`refs/heads/` refs are silently dropped. The agent receives the branch ref and the resulting head SHA — there is no PR number in the context.

Additional rules:

- `issues.*` events that originate from a PR-backed GitHub issue are dropped; the corresponding `pull_request.*` event covers them instead.
- `pull_request.labeled` events on draft PRs are dropped at the webhook boundary. Use `events: ["pull_request.ready_for_review"]` to act when a draft is marked ready.
- Unknown event kinds are rejected at config load time with a clear error listing the supported set.

### Reactive inter-agent dispatch

Agents can invoke each other at runtime. When an agent's AI run returns a `dispatch[]` field in its JSON response, the daemon validates and enqueues a synthetic `agent.dispatch` event for each entry. The target agent then runs with the full event payload as its runtime context.

#### Agent response contract (extended)

```json
{
  "summary": "Reviewed PR — escalating to sec-reviewer for crypto usage",
  "artifacts": [],
  "dispatch": [
    {
      "agent": "sec-reviewer",
      "number": 42,
      "reason": "PR introduces custom crypto primitives — needs security review"
    }
  ]
}
```

- `agent` — name of the target agent (must be in the originator's `can_dispatch` list and have `allow_dispatch: true`).
- `number` — issue/PR number to associate with the dispatched run. If omitted, the originating event's number is used.
- `reason` — human-readable rationale, included in the target agent's prompt context.

#### Runtime context for dispatched agents

The dispatched agent receives an `agent.dispatch` event with these payload fields:

| Field | Value |
|-------|-------|
| `target_agent` | Name of the agent being invoked (this agent) |
| `reason` | Reason string supplied by the originator |
| `root_event_id` | ID of the original triggering event (stable across the full chain) |
| `dispatch_depth` | How many hops deep in the chain this invocation is |
| `invoked_by` | Name of the agent that dispatched this run |

#### Safety limits (`daemon.dispatch`)

| Field | Default | Meaning |
|-------|---------|---------|
| `max_depth` | 3 | Maximum dispatch chain length. Requests that would exceed this are dropped with a warning. |
| `max_fanout` | 4 | Maximum number of dispatches a single agent run may enqueue. Additional requests are dropped. |
| `dedup_window_seconds` | 300 | Suppress duplicate `(target, repo, number)` dispatch requests within this window (seconds). |

All three fields must be positive integers; the daemon rejects non-positive values at startup.

#### Dispatch flow

```
Agent A runs → returns dispatch[{agent:"B", number:42, reason:"..."}]
    │
    ▼
Dispatcher checks:
  1. B is in A's can_dispatch list
  2. B has allow_dispatch: true
  3. depth ≤ max_depth, fanout ≤ max_fanout
  4. (B, repo, 42) not seen within dedup_window_seconds
    │
    ▼
agent.dispatch event enqueued → Agent B runs with full payload
```

Dispatch chains work across both event-driven and cron/`--run-agent` paths, and the shared dedup store prevents a cron-triggered run and a near-simultaneous dispatch from running the same target twice within the window.

### Environment variables

Create a `.env` file in the project root (loaded automatically):

```bash
GITHUB_WEBHOOK_SECRET=your-webhook-secret
LOG_SALT=optional-prompt-hash-salt
```

---

## Running

```bash
# Directly
go run ./cmd/agents -config config.yaml

# Or build first
go build -o agents ./cmd/agents
./agents -config config.yaml
```

### SQLite mode (`--db`)

An optional SQLite-backed config store lets you manage the fleet over the API instead of editing YAML files. Import once, then drop the `--config` flag entirely:

```bash
# Import from existing YAML (one-time)
./agents --db agents.db --import config.yaml
# → "import: imported 2 backends, 6 skills, 11 agents, 1 repos, 14 bindings"

# All subsequent starts — no config.yaml needed
./agents --db agents.db
```

When started with `--db`, the daemon registers CRUD endpoints for `/skills`, `/backends`, and `/repos`. For `/agents`, `POST /agents` and `GET|DELETE /agents/{name}` are CRUD write endpoints, but `GET /agents` always returns the live fleet snapshot (not the stored agent list). The daemon auto-reloads cron schedules after any repo or agent write. Agent memory is also stored in SQLite instead of the filesystem. The YAML path remains fully supported — both modes are first-class.

### On-demand agent pass

Run one autonomous agent synchronously and exit (useful for testing):

```bash
./agents -config config.yaml --run-agent coder --repo owner/repo
```

Or via HTTP on the running daemon:

```bash
curl -X POST https://<your-host>/run \
  -H "Content-Type: application/json" \
  -d '{"agent":"coder","repo":"owner/repo"}'
```

The agent must be bound to the target repo (any trigger kind works).

### Docker

```bash
docker compose up -d
docker compose up -d --build   # rebuild after code changes
docker compose logs -f agents
docker compose down
```

The compose file expects:
- `config.yaml` in the project root (mounted read-only at `/etc/agents/config.yaml`)
- `.env` in the project root with `GITHUB_WEBHOOK_SECRET` (and optionally `LOG_SALT` and `GITHUB_PAT_TOKEN`)

#### Volume mounts

| Host path | Container path | Purpose |
|---|---|---|
| `config.yaml` | `/etc/agents/config.yaml` (read-only) | Main daemon config (used for `--import`; optional once DB is seeded) |
| `./agents` | `/etc/agents/agents` (read-only) | Optional: prompt/skill files when agent `prompt_file:` paths point here |
| `~/.claude` | `/home/agents/.claude` | Claude Code session data |
| `~/.claude.json` | `/home/agents/.claude.json` | Claude Code main config |
| `~/.codex` | `/home/agents/.codex` | Codex configuration |
| `~/.config/gh` | `/home/agents/.config/gh` (read-only) | GitHub CLI auth tokens |
| `agents-memory` (volume) | `/var/lib/agents/memory` | Autonomous agent memory across restarts |

#### MCP server configuration

Claude Code stores MCP config per-project, keyed by working directory in `~/.claude.json`. Since the container's working directory is `/`, ensure `~/.claude.json` has a project entry for `/` with the GitHub MCP server configured. Verify with:

```bash
docker exec agents claude mcp list
```

---

## GitHub webhook setup

1. Go to **Settings → Webhooks → Add webhook** in your repository.
2. **Payload URL**: `https://<your-host>/webhooks/github`
3. **Content type**: `application/json`
4. **Secret**: same value as `GITHUB_WEBHOOK_SECRET`.
5. **Events**: the daemon accepts **Issues**, **Pull requests**, **Issue comments**, **Pull request reviews**, **Pull request review comments**, and **Pushes**. Enable whichever ones you want to trigger agents on. Unused events are silently dropped.
6. **Active**: checked.

GitHub sends a ping immediately; the daemon will log the delivery.

---

## HTTP endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/status` | Health check: JSON with uptime, event queue depth, agent schedules, dispatch counters |
| `POST` | `/webhooks/github` | GitHub webhook receiver (`X-Hub-Signature-256` HMAC verified) |
| `POST` | `/run` | On-demand agent trigger |
| `POST` | `/v1/messages` | Anthropic↔OpenAI translation proxy (opt-in via `proxy.enabled`) |
| `GET` | `/v1/models` | Companion stub for `/v1/messages`; lists the configured upstream model |
| `GET` | `/agents` | Fleet snapshot: per-agent status, bindings, dispatch wiring |
| `GET` | `/events` | Recent webhook events (time-windowed) |
| `GET` | `/events/stream` | Live event firehose (SSE) |
| `GET` | `/traces` | Recent agent run traces with timing |
| `GET` | `/traces/stream` | Live trace updates (SSE) |
| `GET` | `/traces/{root_event_id}` | All spans for a single root event |
| `GET` | `/traces/{span_id}/steps` | Tool-loop transcript for a completed agent span |
| `GET` | `/graph` | Agent interaction graph (dispatch edges) |
| `GET` | `/dispatches` | Dispatch dedup store contents + counters |
| `GET` | `/memory/{agent}/{repo}` | Raw agent memory markdown |
| `GET` | `/memory/stream` | Memory file change notifications (SSE) |
| `GET` | `/config` | Effective parsed config (secrets redacted) |
| `GET` | `/ui/` | Built-in web dashboard (static assets; embedded in binary) |
| `GET` | `/{resource}` | List all entries for a resource type (`skills`, `backends`, `repos`). Only registered when `--db` is set. (`GET /agents` is the fleet snapshot above, not the CRUD list.) |
| `GET` | `/{resource}/{name}` | Fetch one entry. Repos use two path segments: `/repos/{owner}/{repo}`. Only with `--db`. |
| `POST` | `/{resource}` | Create or replace an entry (write API; requires `--db`). Resources: `agents`, `skills`, `backends`, `repos`. |
| `DELETE` | `/{resource}/{name}` | Remove an entry (requires `--db`). |
| `GET` | `/export` | Export full fleet config as YAML (requires `--db`). |
| `POST` | `/import` | Import a YAML config into the SQLite store (requires `--db`). |

The `/run` body is `{"agent": "<name>", "repo": "owner/repo"}`. It returns `202 Accepted` immediately with an `event_id`; the agent runs asynchronously. All endpoints are unauthenticated at the daemon level — access control is the reverse proxy's responsibility.

Duplicate webhook deliveries are suppressed via `X-GitHub-Delivery` with a TTL cache.

---

## Local models — run your fleet on your own LLM

Point the agents daemon at any OpenAI-compatible endpoint (`llama.cpp`, Ollama, vLLM, hosted Qwen on Together/Alibaba, anything else) and run the entire fleet without paying per token or sending code to a vendor. No sidecar processes, no Python dependencies — a built-in Go proxy inside the daemon translates Anthropic Messages format to OpenAI Chat Completions and keeps Claude Code's full tool stack working on top of whatever model you pick.

Quick wire-up in `config.yaml`:

```yaml
daemon:
  proxy:
    enabled: true
    upstream:
      url: http://localhost:18000/v1   # your llama.cpp / Ollama / vLLM / hosted endpoint
      model: qwen                      # anything; most servers ignore this
      timeout_seconds: 3600
      extra_body:                      # merged into every upstream request
        chat_template_kwargs:
          enable_thinking: false       # Qwen 3.5: skip reasoning-token waste

  ai_backends:
    claude:                            # default: hosted Anthropic
      command: claude
      args: [-p, --dangerously-skip-permissions]
    claude_local:                      # same binary, different env → proxy
      command: claude
      args: [-p, --dangerously-skip-permissions]
      env:
        ANTHROPIC_BASE_URL: http://localhost:8080
        ANTHROPIC_API_KEY: sk-not-needed
        ANTHROPIC_MODEL: qwen

agents:
  - { name: pr-reviewer, backend: claude_local }    # Qwen-backed
  - { name: coder,        backend: claude }         # hosted Claude
```

**Measured on our own infra** — Qwen3.5-35B-A3B at Q5 on a rented RTX 5090: **~75 tok/s decode, 5000+ tok/s prefill, 90+ tool-loop round-trips per run without a single translation error**. Same ballpark as hosted Claude Sonnet on a GPU that rents for `$0.60/hr`.

See **[docs/local-models.md](docs/local-models.md)** for the full setup recipe, model picks by VRAM tier, recommended `llama.cpp` tuning flags (prefix caching, KV quantization, batch sizing), cost math, and honest caveats about capability gaps on action-taking agents.

---

## AI runner contract

The daemon spawns the configured CLI, sends the composed prompt on **stdin**, and expects a **single JSON object on stdout**:

```json
{
  "summary": "Reviewed PR for security vulnerabilities",
  "artifacts": [
    {
      "type": "pr_review",
      "part_key": "review/claude/security",
      "github_id": "123456",
      "url": "https://github.com/owner/repo/pull/1#pullrequestreview-123456"
    }
  ],
  "dispatch": [
    {
      "agent": "sec-reviewer",
      "number": 42,
      "reason": "Custom crypto primitives found — needs deeper security review"
    }
  ],
  "memory": "## 2026-04-21\n- Reviewed PR #42 — escalated crypto concerns to sec-reviewer."
}
```

The metadata is used for observability, logging, and run summaries. Agents that don't post anything still return an empty `artifacts: []`. The `dispatch` field is optional — omit it or leave it empty when the agent does not need to invoke another agent. See [Reactive inter-agent dispatch](#reactive-inter-agent-dispatch) for the full contract.

The `memory` field is how autonomous agents persist state across scheduled runs. The daemon reads the stored memory before each autonomous run and writes the `memory` value from the response back to the store (filesystem or SQLite depending on how the daemon was started). An empty string clears the memory. Event-driven runs (webhooks, label triggers) do not receive or persist memory.

---

## Contributing

This project is built by its own agent fleet. **You bring the ideas, the agents bring the implementation.** Open an issue with the `discussing` label, a maintainer triages it, and the autonomous coder agent implements accepted issues — reviewed by the pr-reviewer agent before merge.

See **[CONTRIBUTING.md](CONTRIBUTING.md)** for the full process, what makes a great issue, and the exceptions (doc typo PRs and security patches are accepted directly).

---

## Security

- **Webhook verification** — HMAC SHA-256 on every payload (`X-Hub-Signature-256`).
- **Reverse-proxy auth** — the daemon delegates access control to the reverse proxy (e.g. Traefik basic auth).
- **Read-only daemon** — all GitHub writes go through the AI backend's MCP tools.
- **Prompt redaction** — prompts are never logged in plaintext; only their hash and length.
- **`--dangerously-skip-permissions`** — required for headless Claude operation. Ensure the host is trusted.

---

## Logging

Two formats via `daemon.log.format`:

- **`text`** (default) — coloured, human-readable.
- **`json`** — structured for log aggregation (Loki, Datadog, etc).

Every entry includes `repo`, `issue_number` or `pr_number`, and `component` for filtering.

```json
{"level":"info","component":"workflow_engine","repo":"owner/repo","pr_number":42,"backend":"claude","message":"invoking ai agent"}
```

---

## Testing

```bash
go test ./... -race
```

---

## Project structure

```
cmd/agents/main.go          # Daemon entry point + --run-agent / --db / --import modes
internal/
  config/                   # YAML parsing, prompt/skill file resolution, validation
  ai/                       # Prompt composition + command-based CLI runner (per-backend env)
  anthropic_proxy/          # Built-in Anthropic↔OpenAI translation proxy (opt-in)
  observe/                  # Observability store (events, traces, dispatch graph, SSE hubs)
  autonomous/               # Cron scheduler + agent memory (filesystem or SQLite)
  store/                    # SQLite-backed config store (--db mode): schema migrations, CRUD helpers
  workflow/                 # Event routing engine, single event queue, processor, inter-agent dispatcher
  webhook/                  # HTTP server, signature verification, delivery dedupe, CRUD API handlers
  ui/                       # Embedded Next.js web dashboard (served at /ui/)
  setup/                    # Interactive first-time setup command
  logging/                  # zerolog setup
prompts/                    # Optional: prompt files referenced by agent prompt_file
skills/                     # Optional: skill files referenced by skill prompt_file
docs/                       # Long-form docs (docs/local-models.md, etc.)
```

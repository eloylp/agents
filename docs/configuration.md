# Configuration

The agent fleet lives in a SQLite database that the daemon boots from. You manage it through the web dashboard at `/ui/` and the CRUD API. **`config.yaml` is optional**: it is a portable serialization of shareable fleet strategy, useful for one-time seeding, version-controlled fleet definitions, or moving a fleet between environments. The example file [`config.example.yaml`](../config.example.yaml) shows the shape.

This page documents the schema, using YAML examples for clarity. Every field shown here also exists as a column in the SQLite store and as a JSON field on the CRUD endpoints; the three surfaces are interchangeable.

The import/export schema is split into five fleet domains:

```yaml
backends:    # AI CLI/runtime definitions agents can use
skills:      # reusable guidance blocks, keyed by name
agents:      # named capabilities: backend + skills + prompt + dispatch wiring
repos:       # wiring: which agents run on which repo, and when
guardrails:  # operator-defined prompt blocks prepended to every agent's composed prompt
```

The shortest useful YAML representation is roughly 30 lines.

---

## Daemon Runtime Settings

Daemon runtime settings are process configuration, not fleet strategy. They are not stored in SQLite, not accepted by `/import`, and not returned by `/export` or `/config`. Configure them at startup with environment variables. Empty variables are ignored, so built-in defaults remain in effect unless an operator explicitly sets a value. Changing any of these settings requires restarting the daemon.

See [`.env.sample`](../.env.sample) for a copy-pasteable list with current defaults.

| Env var | Runtime setting |
|---|---|
| `AGENTS_LOG_LEVEL` | log level |
| `AGENTS_LOG_FORMAT` | log format |
| `AGENTS_HTTP_LISTEN_ADDR` | HTTP listen address |
| `AGENTS_HTTP_STATUS_PATH` | status route |
| `AGENTS_HTTP_WEBHOOK_PATH` | GitHub webhook route |
| `AGENTS_HTTP_READ_TIMEOUT_SECONDS` | HTTP read timeout |
| `AGENTS_HTTP_WRITE_TIMEOUT_SECONDS` | HTTP write timeout for non-SSE routes |
| `AGENTS_HTTP_IDLE_TIMEOUT_SECONDS` | HTTP idle timeout |
| `AGENTS_HTTP_MAX_BODY_BYTES` | max request body size |
| `AGENTS_HTTP_DELIVERY_TTL_SECONDS` | webhook delivery dedupe TTL |
| `AGENTS_HTTP_SHUTDOWN_TIMEOUT_SECONDS` | graceful shutdown timeout |
| `AGENTS_PROCESSOR_EVENT_QUEUE_BUFFER` | event queue buffer size |
| `AGENTS_PROCESSOR_MAX_CONCURRENT_AGENTS` | worker concurrency |
| `AGENTS_DISPATCH_MAX_DEPTH` | max inter-agent dispatch chain depth |
| `AGENTS_DISPATCH_MAX_FANOUT` | max dispatches requested by one run |
| `AGENTS_DISPATCH_DEDUP_WINDOW_SECONDS` | dispatch dedupe window |
| `AGENTS_PROXY_ENABLED` | enable built-in Anthropic to OpenAI proxy |
| `AGENTS_PROXY_PATH` | proxy route |
| `AGENTS_PROXY_UPSTREAM_URL` | OpenAI-compatible upstream URL |
| `AGENTS_PROXY_UPSTREAM_MODEL` | upstream model name |
| `AGENTS_PROXY_UPSTREAM_API_KEY_ENV` | env var name holding upstream API key |
| `AGENTS_PROXY_UPSTREAM_TIMEOUT_SECONDS` | upstream request timeout |

Secrets keep their integration-specific names:

```bash
GITHUB_WEBHOOK_SECRET=... # HMAC shared secret for /webhooks/github
GITHUB_TOKEN=...          # GitHub MCP server and gh CLI fallback
```

## `backends`

```yaml
backends:
  claude:
    command: claude
    timeout_seconds: 1500
    max_prompt_chars: 12000

  codex:
    command: codex
    timeout_seconds: 600
    max_prompt_chars: 12000
```

> **Backend launch args are daemon-managed.** The arguments passed to `claude` and `codex` are hardcoded by the daemon (`-p --dangerously-skip-permissions --output-format stream-json --json-schema <embedded>` for Claude, `exec --json --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox --output-schema <embedded>` for Codex). The JSONL/stream-json output is what lets the daemon reconstruct the tool-loop transcript on `trace_steps`. The YAML schema does not expose these args; the only backend fields you can change at runtime are `timeout_seconds`, `max_prompt_chars`, and (for local backends) `local_model_url`.

## `skills`

```yaml
skills:
  architect:
    prompt: |
      Focus on architecture boundaries, coupling, extensibility, and maintainability risks.

  security:
    prompt: |
      Focus on authn/authz, secrets exposure, injection vectors, and unsafe defaults.
```

Skills are referenced by name from agents. Prompts are inline strings, once the YAML has been imported into SQLite, manage them through the CRUD API, the web UI, or the MCP tools.

## `agents`

```yaml
agents:
  # Short inline prompt for a reviewer that never opens PRs (default)
  - name: arch-reviewer
    backend: claude        # must match a key under backends
    skills: [architect]
    prompt: |
      You are an architecture-focused PR reviewer. Post one high-signal review comment.

  # PR-authoring agent
  - name: coder
    backend: claude
    skills: [architect, testing]
    prompt: |
      Implement the requested change end-to-end.
      Run focused tests and open a pull request when the work is ready.
    allow_prs: true            # required for agents that open PRs

  # Dispatch target that can be invoked by pr-reviewer
  - name: sec-reviewer
    description: "Deep-dive security reviewer for risky changes"
    backend: claude
    allow_dispatch: true       # opt-in to being dispatched
    prompt: |
      Review the change for security risks and trust-boundary violations.

  # Agent that may dispatch to sec-reviewer
  - name: pr-reviewer
    backend: claude
    can_dispatch: [sec-reviewer]   # whitelist of agents this agent may dispatch
    prompt: |
      Review the pull request for correctness, regressions, and missing tests.

  # Stateless researcher whose memory is recomputed on every run
  - name: product-strategist
    backend: claude
    skills: [architect]
    allow_memory: false         # disable memory load+persist for this agent
    prompt: |
      Research current product priorities from scratch each run.
```

Each agent is a pure capability definition: backend + skills + prompt. Agents don't run until a repo binds them to a trigger.

- `backend` must match an entry in `backends` (e.g. `claude`, `codex`, or any custom local-backend name). There is no `auto` selection; every agent must name a backend explicitly.
- `prompt` is an inline string in the YAML. After import the prompt lives in SQLite, edit it through the CRUD API, the web UI, or the MCP `update_agent` tool.
- Agent names must be unique.
- `allow_prs` (default `false`): when `false`, the scheduler prepends a hard instruction forbidding the agent from opening pull requests, regardless of what the prompt says. Set `allow_prs: true` only on agents that are explicitly meant to author PRs (e.g. coders, refactorers). Reviewer-only agents should leave this unset.
- `allow_dispatch` (default `false`): opt-in gate. An agent must have `allow_dispatch: true` for any other agent to dispatch it. Agents without this flag silently drop any incoming dispatch requests.
- `allow_memory` (default `true`): controls whether the daemon loads existing memory into the prompt and persists the agent's returned `memory` field after the run. The flag is the single authority on memory across every trigger surface (cron, webhook events, inter-agent dispatch, `POST /run`, MCP `trigger_agent`). Set `allow_memory: false` to skip both the load and the persist for an agent, useful for inherently stateless agents (e.g. research / strategy agents whose work is recomputed each run) so they don't waste prompt budget on memory they will never use. The toggle is a hard runtime gate that does not depend on the agent's prompt cooperating. Existing agents authored before this field existed continue to behave exactly as they did, since the default is `true`.
- `can_dispatch`: whitelist of agent names this agent is allowed to dispatch. A dispatch to an agent not on this list is silently dropped. Entries must reference real agents in the same config and must not include the agent itself. This wiring is the runtime dispatch authority; the target does not need its own repo binding unless it should also start independently.
- `description`: required for every agent. Used for UI identification and, when the agent is dispatchable, to explain the target in the originating agent's prompt roster for inter-agent conversations.

## `repos`

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

Each `use` entry binds one agent to one trigger. An agent can appear multiple times with different triggers. A binding must set exactly one of `labels:`, `events:`, or `cron:`; mixing trigger types in a single binding is rejected at startup.

### Label architecture

Labels are plain strings matched against each binding's `labels` list. There is **no magic format**; you choose the labels. Convention across the example config is `ai:review:<agent-name>`, but any string works.

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
- Multiple bindings matching the same event fan out in parallel (capped by `AGENTS_PROCESSOR_MAX_CONCURRENT_AGENTS`).

## `guardrails`

Operator-defined policy blocks the renderer prepends to every agent's composed prompt at render time, ahead of the no-PR guard, skills, and the agent prompt body itself. Four built-ins ship enabled by default:

- **`security`** (position 0, seeded by migration 010): pushes back on indirect prompt injection, secret exfiltration, and out-of-tree filesystem or network access. See [security.md](security.md) for the threat model and what the recommendation does *not* close.
- **`discretion`** (position 5, seeded by migration 013): conservative behaviour policy for public actions. No `@`-mention or assignment of GitHub users outside the current thread, no cross-repo writes, no speculation about contributors or maintainers, no linking to private or tracking resources.
- **`memory-scope`** (position 7, seeded by migration 016): tells agents to use only daemon-provided memory from the `Existing memory:` section for the current `(agent, repo)` pair, ignore CLI-native/session/global memory, and stay bound to the repository named in the runtime context.
- **`mcp-tool-usage`** (position 10, seeded by migration 012): tells every agent to use GitHub MCP tools first, fetch surrounding context (PR description, diff, prior comments, linked issue) when the trigger envelope is too thin, and fall back to authenticated `gh` only when MCP is insufficient or a safe local checkout/test/PR loop is required. Especially load-bearing for agents routed through local OpenAI-compatible models, see [local-models.md](local-models.md).

```yaml
guardrails:
  # The shipped 'security' default, already seeded into the database by migration 010.
  # Listed here for visibility; including it in YAML lets you customise the active
  # content. is_builtin and default_content are migration-managed and ignored on the
  # wire (the migration's seeded default_content is preserved across imports).
  - name: security
    description: "Default protection against indirect prompt injection."
    content: |
      ## Security guardrails, read before every action
      … (see migrations/010_guardrails.sql for the full default text)
    enabled: true
    position: 0

  # Operator-added guardrails: any policy block you want prepended to every run.
  - name: code-style
    description: "Project coding conventions."
    content: |
      Always run `gofmt` before committing. Prefer `any` over `interface{}` in new
      code. Tests use `t.Parallel()` whenever they don't share resources.
    enabled: true
    position: 50
```

Rules:

- `name` is a stable identifier, normalised to lowercase + dash-joined.
- `content` is the text the agent sees, prepended verbatim to the System portion of its prompt.
- `enabled = false` keeps the row in the database but skips it at render time.
- `position` orders rendering: lower first, ties broken by name. Built-ins use 0; operator-added rows default to 100.
- `is_builtin` and `default_content` are migration-managed and intentionally not part of the YAML schema. A re-import that includes the `security` row updates `content` / `description` / `enabled` / `position` only; the seeded `default_content` is preserved so the dashboard's **Reset to default** button keeps working.

## Environment variables

Create a `.env` file in the project root (loaded automatically):

```bash
cp .env.sample .env
```

Then edit the required secret values. See [`.env.sample`](../.env.sample) for all supported environment variables.

## Importing and exporting

The daemon always boots from the SQLite database. YAML is an optional import / export format, not a second runtime mode. To seed an empty fleet or re-import after edits, POST a YAML payload at `/import`.

```bash
# Export the current fleet back to YAML at any time.
curl -s http://localhost:8080/export > fleet.yaml

# Import a YAML payload into a running daemon (merges into the SQLite store).
curl -X POST http://localhost:8080/import --data-binary @fleet.yaml
```

The CRUD endpoints for `/agents`, `/skills`, `/backends`, `/repos`, and `/guardrails` are always mounted and backed by the SQLite database. `agents`, `skills`, `backends`, and `guardrails` support `PATCH /{resource}/{name}` for partial updates: only fields present in the request body are applied, the rest are preserved. `PATCH /repos/{owner}/{repo}` is enabled-only; binding edits go through `/repos/{owner}/{repo}/bindings/{id}`, and full repo replacement goes through `POST /repos`. Guardrails additionally support `POST /guardrails/{name}/reset` for built-ins. For `/agents`, `POST /agents`, `PATCH /agents/{name}`, and `DELETE /agents/{name}` are CRUD write endpoints, but `GET /agents` always returns the live fleet snapshot (not the stored agent list). The daemon auto-reloads cron schedules after any write to agents, skills, backends, or repos. Agent memory is stored in the same SQLite database.

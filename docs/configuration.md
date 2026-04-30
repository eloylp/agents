# Configuration

The fleet lives in a SQLite database that the daemon boots from. You manage it through the web dashboard at `/ui/` and the CRUD API. **`config.yaml` is optional**: it is a portable serialization of the same data the database holds, useful for one-time seeding, version-controlled fleet definitions, or moving a fleet between environments. The example file [`config.example.yaml`](../config.example.yaml) shows the shape.

This page documents the schema, using YAML examples for clarity. Every field shown here also exists as a column in the SQLite store and as a JSON field on the CRUD endpoints; the three surfaces are interchangeable.

The schema is split into four conceptual domains:

```yaml
daemon:    # how the service runs: log, http, processor, backends, optional proxy
skills:    # reusable guidance blocks, keyed by name
agents:    # named capabilities: backend + skills + prompt + dispatch wiring
repos:     # wiring: which agents run on which repo, and when
```

The shortest useful YAML representation is roughly 30 lines.

---

## `daemon`

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
      max_depth: 3                          # max chain length before drop + WARN
      max_fanout: 4                         # max dispatches per single agent run
      dedup_window_seconds: 300             # suppress duplicate (target, repo, number) within window

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

Skills are referenced by name from agents. Prompts are inline strings — once the YAML has been imported into SQLite, manage them through the CRUD API, the web UI, or the MCP tools.

## `agents`

```yaml
agents:
  # Short inline prompt for a reviewer that never opens PRs (default)
  - name: arch-reviewer
    backend: claude        # must match a key under daemon.ai_backends
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

- `backend` must match an entry in `daemon.ai_backends` (e.g. `claude`, `codex`, or any custom local-backend name). There is no `auto` selection; every agent must name a backend explicitly.
- `prompt` is an inline string in the YAML. After import the prompt lives in SQLite — edit it through the CRUD API, the web UI, or the MCP `update_agent` tool.
- Agent names must be unique.
- `allow_prs` (default `false`): when `false`, the scheduler prepends a hard instruction forbidding the agent from opening pull requests, regardless of what the prompt says. Set `allow_prs: true` only on agents that are explicitly meant to author PRs (e.g. coders, refactorers). Reviewer-only agents should leave this unset.
- `allow_dispatch` (default `false`): opt-in gate. An agent must have `allow_dispatch: true` for any other agent to dispatch it. Agents without this flag silently drop any incoming dispatch requests.
- `allow_memory` (default `true`): controls whether the daemon loads existing memory into the prompt and persists the agent's returned `memory` field after the run. The flag is the single authority on memory across every trigger surface (cron, webhook events, inter-agent dispatch, `POST /run`, MCP `trigger_agent`). Set `allow_memory: false` to skip both the load and the persist for an agent — useful for inherently stateless agents (e.g. research / strategy agents whose work is recomputed each run) so they don't waste prompt budget on memory they will never use. The toggle is a hard runtime gate that does not depend on the agent's prompt cooperating. Existing agents authored before this field existed continue to behave exactly as they did, since the default is `true`.
- `can_dispatch`: whitelist of agent names this agent is allowed to dispatch. A dispatch to an agent not on this list is silently dropped. Entries must reference real agents in the same config and must not include the agent itself.
- `description`: required when an agent appears in any `can_dispatch` list. Used by the dispatcher to include context about the target in the originating agent's prompt roster.

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
- Multiple bindings matching the same event fan out in parallel (capped by `daemon.processor.max_concurrent_agents`).

## Environment variables

Create a `.env` file in the project root (loaded automatically):

```bash
GITHUB_WEBHOOK_SECRET=your-webhook-secret
LOG_SALT=optional-prompt-hash-salt
```

## Importing and exporting

The daemon always boots from the SQLite database (`--db agents.db`). YAML is an optional import / export format, not a second runtime mode.

```bash
# Start with an empty database; create your fleet through /ui/ or the CRUD API
./agents --db agents.db

# Or seed the database from YAML at first start
./agents --db agents.db --import config.yaml

# Export the current fleet back to YAML at any time
curl -s http://localhost:8080/export > fleet.yaml

# Import a YAML payload into a running daemon
curl -X POST http://localhost:8080/import --data-binary @fleet.yaml
```

The CRUD endpoints for `/agents`, `/skills`, `/backends`, and `/repos` are always mounted and backed by the SQLite database. All four resource types support `PATCH /{resource}/{name}` for partial updates: only fields present in the request body are applied, the rest are preserved. For `/agents`, `POST /agents`, `PATCH /agents/{name}`, and `DELETE /agents/{name}` are CRUD write endpoints, but `GET /agents` always returns the live fleet snapshot (not the stored agent list). The daemon auto-reloads cron schedules after any write to agents, skills, backends, or repos. Agent memory is stored in the same SQLite database.

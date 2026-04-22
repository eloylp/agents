# Configuration

Copy `config.example.yaml` to `config.yaml` and adapt it. The config file is split into four conceptual domains:

```yaml
daemon:    # how the service runs: log, http, processor, backends, optional proxy
skills:    # reusable guidance blocks, keyed by name
agents:    # named capabilities: backend + skills + prompt + dispatch wiring
repos:     # wiring: which agents run on which repo, and when
```

The shortest useful config is ~30 lines.

---

## `daemon` -- how the service runs

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

## `skills` -- reusable guidance blocks

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

## `agents` -- named capabilities

```yaml
agents:
  # Short inline prompt -- reviewer that never opens PRs (default)
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

  # Dispatch target -- can be invoked by pr-reviewer
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
- `allow_prs` (default `false`) -- when `false`, the scheduler prepends a hard instruction forbidding the agent from opening pull requests, regardless of what the prompt says. Set `allow_prs: true` only on agents that are explicitly meant to author PRs (e.g. coders, refactorers). Reviewer-only agents should leave this unset.
- `allow_dispatch` (default `false`) -- opt-in gate. An agent must have `allow_dispatch: true` for any other agent to dispatch it. Agents without this flag silently drop any incoming dispatch requests.
- `can_dispatch` -- whitelist of agent names this agent is allowed to dispatch. A dispatch to an agent not on this list is silently dropped. Entries must reference real agents in the same config and must not include the agent itself.
- `description` -- required when an agent appears in any `can_dispatch` list. Used by the dispatcher to include context about the target in the originating agent's prompt roster.

## `repos` -- wiring

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

Each `use` entry binds one agent to one trigger. An agent can appear multiple times with different triggers. A binding must set exactly one of `labels:`, `events:`, or `cron:` -- mixing trigger types in a single binding is rejected at startup.

### Label architecture

Labels are plain strings matched against each binding's `labels` list. There is **no magic format** -- you choose the labels. Convention across the example config is `ai:review:<agent-name>`, but any string works.

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

## SQLite mode (`--db`)

An optional SQLite-backed config store lets you manage the fleet over the API instead of editing YAML files. Import once, then drop the `--config` flag entirely:

```bash
# Import from existing YAML (one-time)
./agents --db agents.db --import config.yaml

# All subsequent starts -- no config.yaml needed
./agents --db agents.db
```

The CRUD endpoints for `/skills`, `/backends`, and `/repos` are always mounted but require `--db` to function -- without it they return errors. For `/agents`, `POST /agents` and `GET|DELETE /agents/{name}` are CRUD write endpoints, but `GET /agents` always returns the live fleet snapshot (not the stored agent list). The daemon auto-reloads cron schedules after any repo or agent write. Agent memory is also stored in SQLite instead of the filesystem. The YAML path remains fully supported -- both modes are first-class.

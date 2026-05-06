# Mental model

How the daemon thinks about agents, what gets composed into every prompt, and the contracts an agent must satisfy in its response. Read this once before writing your first agent prompt; everything else in the docs assumes you have it.

## Agents are pure capability definitions

An agent is a backend, a list of skills, a prompt, and a few flags (`allow_prs`, `allow_dispatch`, `can_dispatch`). It does not run by itself. Agents only execute when a binding wires them to a trigger on a repo.

## How an agent fires

There are four trigger paths. They all end in the same execution model: the daemon composes a prompt, hands it to the AI CLI, and the CLI reads / writes GitHub through MCP tools first. Authenticated `gh` is available inside the container as a fallback when a complex task needs a safe local checkout, test, and PR loop.

### Label-triggered

```
Developer adds label "ai:review:arch-reviewer" to a PR
  → GitHub sends a webhook to the daemon
  → Daemon verifies the HMAC, deduplicates, matches the repo binding
  → Dispatches the bound agent via the AI CLI
  → AI reads the PR, posts a review comment
```

### Cron-scheduled (autonomous)

```
Cron fires (e.g. every 30 minutes)
  → Daemon injects the agent's persisted memory into the prompt
  → AI CLI picks up where it left off, checks open issues, continues work
  → Returns updated memory + artifacts for the next cycle
```

### Event-subscribed

```
Developer opens an issue, pushes to a branch, or submits a review
  → GitHub sends the matching webhook event
  → Daemon routes it to every agent subscribed to that event kind
  → Each agent runs independently with the event payload as context
```

### Reactive dispatch (agent-to-agent)

Any agent can invoke another at runtime by returning a `dispatch` array in its response. The daemon enqueues the target as a new event with depth, fanout, and dedup safety limits. See [dispatch.md](dispatch.md) for the full contract.

## What gets composed into the prompt

Every run, the daemon assembles the prompt from these pieces, in this order:

1. **Operator guardrails.** Every row from the `guardrails` table where `enabled = true`, ordered by `position ASC, name ASC`, prepended verbatim. Shipped built-ins include `security` (indirect prompt injection, secret exfiltration, out-of-tree filesystem/network access), `discretion` (public-action conservatism), `memory-scope` (only daemon-provided memory for the current `(agent, repo)` pair; ignore CLI-native memory), and `mcp-tool-usage` (use GitHub MCP tools first, authenticated `gh` fallback only when MCP is insufficient or a local checkout/test loop is required). Operators can edit, disable, replace, or add their own via the Guardrails tab in `/ui/config`. See [security.md](security.md) for the threat model and what prompt-level controls do *not* close.
2. **Hard agent-flag guards.** Code-level clauses inserted based on the agent's flags. The most visible example: when `allow_prs: false`, a clause forbidding the agent from opening pull requests is inserted before the skills, so the gate is code-level rather than relying on the agent's prompt remembering it.
3. **Composed skills.** Every skill in the agent's `skills:` list, concatenated. Skills are reusable guidance blocks (architecture, testing, security, ...) that compose orthogonally.
4. **The agent's own prompt.** The agent-specific instructions you wrote in `prompt:`.
5. **Available experts roster.** When the agent has valid dispatch targets in `can_dispatch:`, the daemon injects an `## Available experts` section listing targets that exist and have `allow_dispatch: true`. Every agent has a required `description`, and dispatchable targets use it as routing context in the roster.
6. **Runtime context.** A `## Runtime context` block carrying event details: `Event` kind, `Actor` (the GitHub login that triggered it), an issue or PR number where applicable, and the payload fields documented per event kind in [events.md](events.md).
7. **Memory.** When the agent has `allow_memory: true` (the default), the daemon reads its persisted memory before the run and appends it to the prompt; the response's `memory` field is persisted back after a successful run. This applies uniformly across every trigger surface: cron, webhook events, dispatch, `POST /run`, and the `trigger_agent` MCP tool. Setting `allow_memory: false` skips both the load and the persist regardless of how the run was triggered. CLI-native memory is not part of the daemon contract; the built-in `memory-scope` guardrail tells agents to ignore it and use only the daemon-rendered `Existing memory:` section.

The order matters: guardrails before everything (so untrusted text the agent reads later cannot retroactively unset them), skills before the agent's own prompt, runtime context last. The agent's prompt can reference its skills, and runtime details come pre-loaded so the prompt does not need to ask "what triggered this?"

### What it looks like assembled

The composed prompt has two parts. Backends that support a system channel (Claude's `--append-system-prompt`) get the stable part as system and the per-run part as user; other backends get the two concatenated. The split keeps the long, repeated content in the cacheable side and the volatile per-run content in the user turn.

For a `pr-reviewer` agent with `skills: [discretion, pr_lifecycle]`, `allow_prs: false`, `can_dispatch: [sec-reviewer]`, fired by `pull_request.labeled` on PR #42:

**System part** (identical across runs of this agent):

```
Do not open or create pull requests under any circumstances.

You operate inside an autonomous fleet. Do NOT @-mention external GitHub
users. Do NOT make cross-repo writes. ...

When reviewing a pull request, fetch the diff and the linked issue,
check the contribution guidelines in the repo, and post one consolidated
review. ...

You are the pr-reviewer agent. Your job is to read the PR end-to-end,
identify correctness and clarity issues, and post a single review with
your findings. Use the GitHub MCP tools available to you. ...

## Available experts

- **sec-reviewer**: Deep security review for crypto, auth, supply chain. [dispatchable]
```

**User part** (changes every run):

```
## Runtime context

Repository: owner/repo
Issue/PR number: 42
Backend: claude
Event: pull_request.labeled
Actor: alice
label: ai:review:pr-reviewer
title: Refactor token refresh
draft: false
Existing memory:
## 2026-04-21
- Reviewed PR #38; flagged retry storm. Followed up with author.
```

Notes on the layout:
- The two skill bodies (`discretion`, `pr_lifecycle`) are concatenated verbatim, separated by a blank line. The daemon does not edit them.
- The agent's own prompt comes immediately after the last skill, with no separator headline.
- The roster only appears when `can_dispatch` is non-empty.
- Payload keys are sorted alphabetically; multi-line string values are indented.
- Memory is appended last, with a literal `Existing memory:` label. An empty memory still shows the label so the agent knows to start fresh.

## What the agent must return

Every agent run produces a single top-level JSON object on stdout:

```json
{
  "summary": "Reviewed PR #42 for race conditions",
  "artifacts": [
    {
      "type": "pr_review",
      "part_key": "review/claude/correctness",
      "github_id": "123456",
      "url": "https://github.com/owner/repo/pull/42#pullrequestreview-123456"
    }
  ],
  "dispatch": [
    {
      "agent": "sec-reviewer",
      "number": 42,
      "reason": "Custom crypto primitives found; needs deeper security review"
    }
  ],
  "memory": "## 2026-04-21\n- Reviewed PR #42; escalated crypto concerns to sec-reviewer."
}
```

| Field | Required | Meaning |
|---|---|---|
| `summary` | yes | One-line overall outcome. Surfaced in observability views and logs. |
| `artifacts` | yes (may be `[]`) | Every GitHub object the agent created or updated (comments, PRs, issues, labels). Used for observability and audit. |
| `dispatch` | yes (may be `[]`) | Inter-agent dispatch requests. See [dispatch.md](dispatch.md) for the contract. Use `[]` when no dispatch is needed. |
| `memory` | yes (may be `""`) | The agent's full updated memory. Persisted after every run when the agent has `allow_memory: true` (the default), uniformly across cron, webhooks, dispatch, `POST /run`, and `trigger_agent`. An empty string clears the memory. |

A run that returns no JSON, an empty response, or a response where `summary`, `artifacts`, and `dispatch` are all empty fails with a clear error.

## How the contract is enforced

The daemon does not trust the AI CLI to format its output correctly. The schema is enforced at the CLI level:

- **Claude Code.** The daemon spawns `claude` with `--output-format stream-json --json-schema <embedded-schema>`. Claude is constrained to emit a stream-JSON response matching the schema.
- **Codex.** The daemon spawns `codex` with `--output-schema <temp-file>` pointing at the same schema serialized to disk. Codex enforces the schema in the same way.
- The schema lives at `internal/ai/response-schema.json`, embedded in the binary at build time.
- The daemon parses the **last top-level JSON object** from stdout. Anything before it (free-text analysis, tool-call transcripts, debug output) is ignored.

If you change the response shape (new field, removed field, type change), update `internal/ai/response-schema.json` alongside `internal/ai/types.go`. The two are always read together.

## Why this matters when you write prompts

Two implications to keep in mind:

1. **Your prompt is one slice of a bigger composition.** Skills come before it; runtime context comes after it. Do not repeat skill guidance in the agent prompt. Do not try to inject runtime details yourself; the daemon does that.

2. **You must end with the JSON.** Whatever free-text the AI emits in tool calls or chain-of-thought is fine, but the response stream must end with a single top-level JSON object that satisfies the schema. The CLI flags already constrain this, so your prompt does not need to restate the schema, but it must not contradict it. Asking the agent to "write a long prose summary" when the response field is one line will produce a model that struggles or fails the schema check.

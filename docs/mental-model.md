# Mental model

How the daemon thinks about agents, what gets composed into every prompt, and the contracts an agent must satisfy in its response. Read this once before writing your first agent prompt; everything else in the docs assumes you have it.

## Agents are pure capability definitions

An agent is a backend, a list of skills, a prompt, and a few flags (`allow_prs`, `allow_dispatch`, `can_dispatch`). It does not run by itself. Agents only execute when a binding wires them to a trigger on a repo.

## How an agent fires

There are four trigger paths. They all end in the same execution model: the daemon composes a prompt, hands it to the AI CLI, and the CLI reads / writes GitHub through its MCP tools.

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

1. **Hard guardrails.** The scheduler prepends fixed instructions based on the agent's flags. The most visible example: when `allow_prs: false`, a clause forbidding the agent from opening pull requests is inserted before anything else, so the gate is code-level rather than relying on the agent's prompt remembering it.
2. **Composed skills.** Every skill in the agent's `skills:` list, concatenated. Skills are reusable guidance blocks (architecture, testing, security, ...) that compose orthogonally.
3. **The agent's own prompt.** The agent-specific instructions you wrote in `prompt:` or `prompt_file:`.
4. **Available experts roster.** When the agent has a non-empty `can_dispatch:` list, the daemon injects an `## Available experts` section listing each dispatchable target with its `description`.
5. **Runtime context.** A `## Runtime context` block carrying event details: `Event` kind, `Actor` (the GitHub login that triggered it), an issue or PR number where applicable, and the payload fields documented per event kind in [events.md](events.md).
6. **Memory.** For autonomous (cron) runs, the agent's persisted memory markdown is appended. Event-driven runs do not receive memory.

The order matters: guardrails before skills, skills before the agent's own prompt, runtime context last. The agent's prompt can reference its skills, and runtime details come pre-loaded so the prompt does not need to ask "what triggered this?"

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
| `artifacts` | yes (may be empty) | Every GitHub object the agent created or updated (comments, PRs, issues, labels). Used for observability and audit. |
| `dispatch` | no | Inter-agent dispatch requests. See [dispatch.md](dispatch.md) for the contract. Omit or use `[]` when no dispatch is needed. |
| `memory` | no | The agent's full updated memory. Persisted only for autonomous runs and only when the agent's memory persistence flag is true. An empty string clears the memory. |

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

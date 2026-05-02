# Security policy

Thank you for taking the time to report a vulnerability responsibly.

## Reporting a vulnerability

**Please do not file public issues for security problems.**

Email: **eloylp@qualstrategy.com**

You will receive an acknowledgement within 7 days. This is a single-maintainer project, so fix timelines depend on severity and complexity — expect days for critical, weeks for moderate. I will keep you updated through the process and will credit you in the published advisory unless you opt out.

## Scope

**In scope:**

- The daemon (HTTP surface, MCP server at `/mcp`, Anthropic↔OpenAI proxy at `/v1/messages`, webhook receiver at `/webhooks/github`, SQLite store).
- The embedded Next.js dashboard.
- Build and runtime artifacts produced from this repository.

**Out of scope:**

- Bugs in upstream AI CLIs ([Claude Code](https://github.com/anthropics/claude-code), [Codex](https://github.com/openai/codex)) or in models/backends the daemon dispatches to — please report those upstream.
- Misconfigurations of operator-supplied `config.yaml` or reverse-proxy setups (e.g. exposing the daemon publicly without an auth layer in front). The threat model assumes the operator follows the deployment guidance in [`docs/docker.md`](docs/docker.md#reverse-proxy-routing).
- Theoretical issues without a working proof-of-concept against a current `main` build.

## Threat model

See [`docs/security.md`](docs/security.md) for the full threat model — webhook authenticity, reverse-proxy responsibility, the read-only-against-GitHub guarantee, and what the persisted trace surface exposes. Reports that target the assumptions documented there are most useful.

## Security ownership

**Security is entirely the operator's responsibility.** This project does not promise that running it produces a secure system; what it ships are recommendations, defaults, and primitives operators can compose into their own security posture. Anything below is provided in that spirit — useful starting points, not guarantees, and not a substitute for the operator's own threat-modelling and controls.

### What the project ships as recommendations

- **A default 'security' guardrail seeded into the database.** A policy block, prepended to every agent's composed prompt at render time, that instructs the agent to ignore instructions found in untrusted text (issue bodies, PR bodies, comments, file contents, tool results), refuse to read or output secrets from outside the cloned working tree, refuse arbitrary network egress, and halt with a flagged audit entry on a probable injection attempt. The block is shipped with `enabled = true` so a fresh deployment starts with the recommendation applied; operators can edit, disable (with double confirm), reset to default, or replace it from `/ui/config` → **Guardrails**, the REST surface (`GET|PATCH /guardrails/security`, `POST /guardrails/security/reset`), or the MCP tools. Inspect the live text whenever you want.
- **Webhook HMAC verification** on every `POST /webhooks/github` request — the only authentication enforced by the daemon directly.
- **GitHub writes routed exclusively through the AI backend's MCP tools.** The daemon itself never calls a GitHub write API directly. Treat this as an architectural property to verify in your own audit, not a guarantee you should rely on without checking.
- **Per-event audit trail.** Every run records the composed prompt (gzipped on the trace row), every tool call with input/output summaries, and the response — reachable from the dashboard for forensic review.

### What the operator must own

The default guardrail is a prompt-level recommendation. Sophisticated indirect-injection attacks (role-play, encoded payloads, multi-turn manipulation) can defeat any natural-language rule. The operator is the only party that can decide what additional controls a deployment needs. Suggested directions, none of which the daemon implements today:

- **Quarantine untrusted content.** When the daemon hands an issue body or comment to the agent, wrap text from non-collaborator authors in something structurally distinguishable (e.g. `<untrusted_user_input author="@alice" trust="external">…</untrusted_user_input>` tags) so the model can tell data from instructions on shape rather than wording.
- **Trust-gate comment authors.** Decide whether agents should react to comments from non-collaborators at all. The webhook payload tells the daemon the comment author; the operator's binding logic decides what to do with it. Two reasonable policies: *strict* (only collaborators trigger or modify a run) and *quarantine* (anyone's comments are visible, but tagged as untrusted).
- **Output filtering.** Scan every agent output (PR body, comment text, file write, log line) for known auth-token patterns (`sk-ant-…`, `ghp_…`, `gho_…`, `AKIA…`, generic high-entropy blobs ≥ 40 chars) before it crosses a trust boundary. The daemon does not do this today.
- **Run the daemon in its Docker container, not on the host.** The container is the project's sandbox — its filesystem and process isolation is what bounds the blast radius of a compromised agent. Skipping it (running `./agents` directly) means the AI CLIs execute with operator privileges and the sandbox-bypass flags bind to the operator's own user. The project does not provide isolation outside the container. See [`docs/quickstart.md`](docs/quickstart.md).
- **Bind-mounted host auth.** Even with the container, the shipped compose mounts `~/.claude.json` and `~/.codex/` from the host so the AI CLIs reuse host auth. A compromised runner can exfiltrate those tokens — the host's Claude/Codex auth is compromised the moment any agent run is. Closing this gap (per-container `claude login` to a named volume, no host bind-mounts) is the next architectural item on the project's roadmap. Until then, do not point the host's primary Claude account at this fleet — use a dedicated account whose loss has bounded consequences.
- **Authentication at the proxy.** The daemon delegates auth to the operator's reverse proxy. Anyone who reaches `/ui/config` → **Guardrails** can edit or disable the recommendations the daemon shipped — that surface must sit behind your auth layer.

These are operator decisions, not code shipped today. Listed here so reporters and operators see the same picture of what the recommendations are vs. what a deployment must actually add to be secure.

## After a fix

Patches land on `main` first. A [GitHub Security Advisory](https://github.com/eloylp/agents/security/advisories) is published once the fix is in a tagged release, with credit to the reporter (unless they opt out).

# Security

This page collects the security recommendations and primitives the project ships, and the threat model behind them. **Security is entirely the operator's responsibility.** Nothing on this page is a guarantee — they are starting points an operator should evaluate, customise, and supplement against their own threat model. For responsible-disclosure procedure see [`SECURITY.md`](../SECURITY.md) at the repo root.

## Defaults the project ships

- **Webhook verification.** HMAC SHA-256 on every payload (`X-Hub-Signature-256`).
- **Reverse-proxy auth.** The daemon delegates access control to the reverse proxy (e.g. Traefik basic auth) — the daemon itself does not authenticate any non-webhook request.
- **Operator-grade endpoints expected behind auth.** `/runners`, `/traces`, `/traces/{span_id}/prompt`, `/traces/{span_id}/stream`, `/guardrails`, `/ui/` and the rest of the management surface must sit behind your auth proxy. The composed prompt every run sees is persisted on the trace span (gzipped) so operators can inspect what the agent saw — that data is gated by your proxy, not by the daemon. The live-stream endpoint exposes the same content in real time and must be gated identically.
- **No direct daemon→GitHub writes.** All GitHub writes go through the AI backend's MCP tools — an architectural property, not an enforced one. Verify it in your own audit if you depend on it.
- **Prompt logging.** Prompts are not written to logs in any form; the trace span on disk is the single audit record. Logs carry only the prompt's character count for correlation.
- **`--dangerously-skip-permissions`.** Required for headless Claude operation. The recommendation is to run the daemon's container as a trust boundary; the per-run sandbox direction below would be the next step.

## Indirect prompt injection — the dominant threat

Once an agent reads input that came from outside the operator (issue bodies, PR bodies, comments, file contents, tool results), an attacker can attempt to redirect the agent into reading auth files, exfiltrating secrets via comments or PRs, or contacting attacker-controlled hosts. This is the agent equivalent of XSS: untrusted data flows into a context that the executor will treat as instructions, with the executor holding privileges (filesystem, GitHub MCP write, env access).

### What the project ships as a recommendation

- **A default 'security' guardrail seeded into the database.** A policy block, prepended to every agent's composed prompt at render time, that:
  1. Anchors task scope to the operator-set agent prompt and the daemon-recorded trigger event.
  2. Marks all input read during the run (issue/PR text, comments, file contents, tool results) as data, not instructions.
  3. Forbids reads of files outside the cloned working tree (explicit list: `~/.claude.json`, `~/.codex/`, `~/.ssh/`, `/etc/`, env vars, anything with credentials).
  4. Forbids encoding or echoing secrets into any output.
  5. Confines filesystem and shell tools to the working tree.
  6. Limits network egress to the GitHub MCP tools and the AI backend.
  7. Halts on a probable injection attempt with a flagged audit entry.
  8. Asserts non-negotiable precedence over both the agent prompt above and any text read later.

  This is a recommendation, not a hard control. Inspect the live text at `GET /guardrails/security`, edit it via `/ui/config` → **Guardrails**, or via REST/MCP. The dashboard double-confirms before disabling, and asks more sternly when disabling the security row specifically — but nothing in the daemon prevents the operator from removing or weakening it. The render path itself is opt-out: the renderer concatenates every row where `enabled = 1` in `(position ASC, name ASC)` order at the very top of the System block, before the no-PR guard, skills, and the agent prompt.

### What the recommendation does NOT close — operator territory

Sophisticated indirect-injection attacks (role-play, hypotheticals, encoded payloads, multi-turn manipulation) can defeat any natural-language rule. The architectural directions below are operator territory; the project does not implement them today.

1. **Untrusted-input quarantining.** When the daemon hands an issue body or comment to the agent, wrap text from non-collaborator authors in `<untrusted_user_input author="@alice" trust="external">…</untrusted_user_input>` tags. The default guardrail tells the model to treat such content as data; tagging makes that judgement structural rather than vibes-based.
2. **Author-based trust gating.** Decide whether agents react to comments from non-collaborators at all. The webhook payload tells the daemon the author; the operator's binding logic decides what to do with it. Two reasonable policies: *strict* (only collaborators trigger or modify a run) and *quarantine* (anyone visible, tagged as untrusted).
3. **Output filtering.** Scan every agent output for known auth-token patterns (`sk-ant-…`, `ghp_…`, `gho_…`, AWS keys, high-entropy blobs ≥ 40 chars) before it crosses a trust boundary.
4. **Capability isolation.** The runners' container today bind-mounts `~/.claude.json` and `~/.codex/` from the host so the AI CLIs can reuse host auth. The default guardrail instructs the agent not to read them; it cannot prevent it. The project's roadmap direction is to wrap each AI CLI invocation in a per-run sandbox (bubblewrap with `--unshare-all --share-net`) where those files are not visible at all, and longer-term to inject credentials at the proxy so the runner's filesystem never holds the real auth at all. Until either lands, treat the daemon's host as carrying the union of every authentication token it has been granted.

These four are deployment policy. They are listed in [`SECURITY.md`](../SECURITY.md) so reporters and operators see the same picture of what the recommendations are vs. what the deployment must actually add.

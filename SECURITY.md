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

## After a fix

Patches land on `main` first. A [GitHub Security Advisory](https://github.com/eloylp/agents/security/advisories) is published once the fix is in a tagged release, with credit to the reporter (unless they opt out).

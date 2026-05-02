# Security policy

Please do not file public issues for security problems.

**Reporting:** open a private security advisory at <https://github.com/eloylp/agents/security/advisories/new>. Acknowledgement within 7 days. Single-maintainer project, so fix timelines depend on severity. You will be credited in the published advisory unless you opt out.

## Scope

**In scope:** the daemon (HTTP surface, MCP server at `/mcp`, Anthropic↔OpenAI proxy at `/v1/messages`, webhook receiver, SQLite store), the embedded Next.js dashboard, and build / runtime artifacts produced from this repository.

**Out of scope:** bugs in upstream AI CLIs ([Claude Code](https://github.com/anthropics/claude-code), [Codex](https://github.com/openai/codex)) or in the models the daemon dispatches to (please report those upstream); misconfigurations of `config.yaml` or operator reverse-proxy setups; theoretical issues without a working proof-of-concept against a current `main` build.

## Threat model and recommendations

**Security is entirely the operator's responsibility.** The project ships recommendations and primitives, not guarantees. See [`docs/security.md`](docs/security.md) for the threat model, the defaults shipped, the reverse-proxy routing pattern, and what an operator must own on top.

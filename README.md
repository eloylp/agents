# Agents

![agents](docs/agents.jpg)

[![CI](https://img.shields.io/github/actions/workflow/status/eloylp/agents/ci.yml?label=CI)](https://github.com/eloylp/agents/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/eloylp/agents)](go.mod)
[![License](https://img.shields.io/github/license/eloylp/agents)](LICENSE)
[![MCP](https://img.shields.io/badge/MCP-compatible-7c3aed)](docs/mcp.md)

**A self-hosted, observable agent orchestrator for running multi-agent workflows on your repos.**

Build and take ownership of your agentic universe. Create your agents and compose them with skills, memory, and triggers: repo events/labels, cron, or inter-agent dispatch.

The daemon schedules each agent and runs the AI CLI ([Claude Code](https://docs.anthropic.com/en/docs/claude-code), [Codex](https://github.com/openai/codex), or your own local LLM) inside a fresh ephemeral runner container. Agents work through your repo host's native primitives: issues, PRs, reviews, comments. GitHub MCP tools are preferred; `gh` is available in the runner as fallback for complex local checkout/test/PR loops. GitHub today; [GitLab](https://github.com/eloylp/agents/issues/359) under discussion.

## See it in action

<table>
  <tr>
    <td align="center">
      <video src="https://github.com/user-attachments/assets/d85a5214-14e5-4ed2-9e7b-b8878c769985" controls></video>
    </td>
    <td align="center">
      <video src="https://github.com/user-attachments/assets/f55ab9d5-f65d-4990-a432-ad9738b3725f" controls></video>
    </td>
    <td align="center">
      <video src="https://github.com/user-attachments/assets/7e5e0a50-fc31-4992-83fa-64bbf244ee50" controls></video>
    </td>
  </tr>
</table>

## Get started

See [`docs/quickstart.md`](docs/quickstart.md) to get the daemon running on a repo in a few minutes from the published `ghcr.io/eloylp/agents` image. For five import-ready fleet scenarios (solo coder, coder + reviewer, autonomous fleet, local LLM, multi-repo) see [`config_examples/`](config_examples/).

## Features

- **Three interfaces**: [web dashboard](docs/ui.md) (graph-first workflow designer, live event/trace/memory viewer), [MCP server](docs/mcp.md) (control the fleet from Claude Code, Cursor, Cline, or any MCP client), [REST API](docs/api.md) (scriptable; the dashboard runs on top of it).
- **[Self-improving intelligence catalog](docs/self-improvement-feedback.md)**: mark review feedback with `/agents improve`; the daemon links it to signed run attribution, runs the catalog analyst, and presents editable prompt/skill/guardrail proposal bundles for human approval.
- **[Reactive inter-agent dispatch](docs/dispatch.md)**: agents invoke each other at runtime with depth, fanout, and dedup safety limits.
- **[Observable](docs/ui.md)**: full event chain in realtime, from webhook receipt to runner to trace with tool-loop transcript, to facilitate prompt tuning.
- **[Self-hosted](docs/quickstart.md)**: your code and prompts stay on your infrastructure.
- **[Security guardrails](docs/security.md)**: built-in prompt guardrails for injection resistance, public-action discretion, daemon-only memory scope, and GitHub tool usage (MCP first, `gh` fallback).
- **Daemon auth**: first-user bootstrap, `HttpOnly` browser sessions, additional user management, revocable named bearer tokens for API/MCP clients.
- **Multi-backend**: pick Claude, Codex, or a [local model](docs/local-models.md) per agent; different agents in the same fleet can use different providers.
- **[One agent, many triggers](docs/events.md)**: label events, cron schedules, GitHub event subscriptions, on-demand API/MCP calls -- same agent definition, wired however you want.
- **Composable skills**: reusable guidance blocks (architecture, security, testing, ...) attached to agents by stable public catalog reference.
- **[Scoped, versioned catalogs](docs/catalog-versioning.md)**: prompts, skills, and guardrails are global, workspace-, or repo-scoped; edits publish immutable versions so traces record the exact text used per run.
- **Token budgets and leaderboard**: daily/weekly/monthly caps enforced before each run, scoped globally or by workspace/repo/agent/backend, with NavBar alert banner and per-agent usage leaderboard.
- **[SQLite-backed](docs/configuration.md)**: single-file state, no external dependencies; YAML is an optional export/import format, not a runtime requirement.

## How it works

Every run, regardless of trigger, goes through the same pipeline:

1. **Compose the prompt**: workspace guardrails + skills + selected prompt + runtime context + memory.
2. **Start a runner container** from the configured `agents-runner` image, configure the operator-provided git identity, and spawn the AI CLI (`claude`, `codex`, or your local model) with JSON-schema-enforced output and repository tools available inside that container.
3. **Parse the structured response**: artifacts, dispatch requests, updated memory.
4. **Persist the trace**, fan out any dispatches, write back memory.

Read [`docs/mental-model.md`](docs/mental-model.md) before writing your first prompt; the rest of the docs assume you have the model. For the daemon's package layout and how a request flows through the Go code, see [`docs/architecture.md`](docs/architecture.md).

## Security

Security is the operator's responsibility; this project ships defaults and recommendations to start from, not guarantees. See [`docs/security.md`](docs/security.md) for the threat model, what the daemon does and does not protect against, and the additional controls operators should layer for production. Vulnerability disclosure: [`SECURITY.md`](SECURITY.md).

## Contributing

Both human and agent contributions are welcome: issues, PRs, doc fixes, prompts, ideas. The autonomous fleet picks up issues and PRs labeled `ai ready` (the maintainer's opt-in signal); everything else is reviewed and merged by humans. See [CONTRIBUTING.md](CONTRIBUTING.md) for the full flow and [docs/architecture.md](docs/architecture.md) for the Go package layout and how a request flows through it.

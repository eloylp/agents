# Agents

![agents](docs/agents.jpg)

[![CI](https://img.shields.io/github/actions/workflow/status/eloylp/agents/ci.yml?label=CI)](https://github.com/eloylp/agents/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/eloylp/agents)](go.mod)
[![License](https://img.shields.io/github/license/eloylp/agents)](LICENSE)
[![MCP](https://img.shields.io/badge/MCP-compatible-7c3aed)](docs/mcp.md)

**A self-hosted, observable orchestrator for multi-agent workflows on your repos.**

Create agents and compose them with skills, memory, and triggers: repo events/labels, cron, or inter-agent dispatch. Route runs through any local OpenAI-compatible model via the built-in proxy. Your fleet, your prompts, your traces, all on your hardware.

The daemon dispatches each agent via an AI CLI ([Claude Code](https://docs.anthropic.com/en/docs/claude-code), [Codex](https://github.com/openai/codex), or your own local LLM) and lets it work through your repo host's native primitives: issues, PRs, reviews, comments. GitHub today; [GitLab](https://github.com/eloylp/agents/issues/359) under discussion.

## Features

- **Three ways to interact with your agent fleet**:
  - **[Web dashboard](docs/ui.md)**: graphical. Manage agents, skills, repos, and bindings; watch the live event firehose, agent traces with tool-loop transcripts, dispatch graph, and memory viewer.
  - **[MCP server](docs/mcp.md)**: conversational. Control agents and trigger runs straight from Claude Code in your terminal (or Cursor, Cline, or any MCP client).
  - **[REST API](docs/api.md)**: programmatic. Scriptable from any HTTP client; the dashboard itself runs on top of it.
- **[Self-hosted](docs/quickstart.md)**: your code and prompts stay on your infrastructure. No SaaS dependency.
- **[Security recommendations](docs/security.md)**: ships a default `security` guardrail prepended to every agent prompt to push back against indirect prompt injection. The threat model and the controls the daemon does *not* implement (untrusted-input quarantining, author trust gating, output filtering, sandboxing) are documented openly so operators can decide what to layer on top.
- **[Multi-backend](docs/configuration.md)**: pick Claude, Codex, or a custom backend per agent. Different agents in the same fleet can use different providers.
- **[Discovery and diagnostics](docs/configuration.md)**: the daemon detects backends and tools, validates CLI health, and persists discovery snapshots.
- **[Local-model support](docs/local-models.md)**: run any agent through `llama.cpp`, Ollama, vLLM, or any OpenAI-compatible endpoint. The daemon ships a built-in Anthropic-to-OpenAI translation proxy so the existing `claude` CLI works unchanged against your own LLM.
- **[One agent model, many triggers](docs/events.md)**: label events, cron schedules, GitHub event subscriptions, on-demand API calls. Same agent, wired however you want.
- **[Composable skills](docs/configuration.md)**: reusable guidance blocks (architecture, security, testing, DX, ...) composed into any agent.
- **[Reactive inter-agent dispatch](docs/dispatch.md)**: agents invoke each other at runtime with depth, fanout, and dedup safety limits.
- **[SQLite-backed fleet](docs/configuration.md)**: state lives in a SQLite database, managed through the three interfaces above. `config.yaml` is an optional export/import format, not a runtime dependency.
- **Transparent**: every agent action is a GitHub comment, issue, or PR. Reviewable. Revertable.

## How it works

Every run, regardless of trigger, goes through the same pipeline:

1. **Compose the prompt**: guardrails + skills + agent prompt + runtime context + memory.
2. **Spawn the AI CLI** (`claude`, `codex`, or your local model) with JSON-schema-enforced output.
3. **Parse the structured response**: artifacts, dispatch requests, updated memory.
4. **Persist the trace**, fan out any dispatches, write back memory.

Read [`docs/mental-model.md`](docs/mental-model.md) before writing your first prompt; the rest of the docs assume you have the model. For the daemon's package layout and how a request flows through the Go code, see [`docs/architecture.md`](docs/architecture.md).

## Get started

See [`docs/quickstart.md`](docs/quickstart.md) to get the daemon running on a repo in a few minutes, `docker compose up -d` is the recommended path. For five import-ready fleet scenarios (solo coder, coder + reviewer, autonomous fleet, local LLM, multi-repo) see [`config_examples/`](config_examples/).

## Security

Security is the operator's responsibility; this project ships defaults and recommendations to start from, not guarantees. See [`docs/security.md`](docs/security.md) for the threat model, what the daemon does and does not protect against, and the additional controls operators should layer for production. Vulnerability disclosure: [`SECURITY.md`](SECURITY.md).

## Contributing

Both human and agent contributions are welcome: issues, PRs, doc fixes, prompts, ideas. The autonomous fleet picks up issues and PRs labeled `ai ready` (the maintainer's opt-in signal); everything else is reviewed and merged by humans. See [CONTRIBUTING.md](CONTRIBUTING.md) for the full flow and [docs/architecture.md](docs/architecture.md) for the Go package layout and how a request flows through it.

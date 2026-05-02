# Agents

![agents](docs/agents.jpg)

**A self-hosted, observable orchestrator for multi-agent workflows on your repos.**

Create agents and compose them with skills, memory, and triggers — repo events, cron, or inter-agent dispatch. Route runs through any local OpenAI-compatible model via the built-in proxy. Your fleet, your prompts, your traces — all on your hardware.

The daemon dispatches each agent via an AI CLI ([Claude Code](https://docs.anthropic.com/en/docs/claude-code), [Codex](https://github.com/openai/codex), or your own local LLM) and lets it work through your repo host's native primitives — issues, PRs, reviews, comments. GitHub today; [GitLab](https://github.com/eloylp/agents/issues/359) under discussion.

## Features

- **Three ways to interact with your fleet**:
  - **[Web dashboard](docs/ui.md)**: graphical. Manage agents, skills, repos, and bindings; watch the live event firehose, agent traces with tool-loop transcripts, dispatch graph, and memory viewer.
  - **[MCP server](docs/mcp.md)**: conversational. Control agents and trigger runs straight from Claude Code in your terminal (or Cursor, Cline, or any MCP client).
  - **[REST API](docs/api.md)**: programmatic. Scriptable from any HTTP client; the dashboard itself runs on top of it.
- **[Self-hosted](docs/docker.md)**: your code and prompts stay on your infrastructure. No SaaS dependency.
- **[Multi-backend](docs/configuration.md)**: pick Claude, Codex, or a custom backend per agent. Different agents in the same fleet can use different providers.
- **[Discovery and diagnostics](docs/configuration.md)**: the daemon detects backends and tools, validates CLI health, and persists discovery snapshots.
- **[Local-model support](docs/local-models.md)**: run any agent through `llama.cpp`, Ollama, vLLM, or any OpenAI-compatible endpoint. The daemon ships a built-in Anthropic-to-OpenAI translation proxy so the existing `claude` CLI works unchanged against your own LLM.
- **[One agent model, many triggers](docs/events.md)**: label events, cron schedules, GitHub event subscriptions, on-demand API calls. Same agent, wired however you want.
- **[Composable skills](docs/configuration.md)**: reusable guidance blocks (architecture, security, testing, DX, ...) composed into any agent.
- **[Reactive inter-agent dispatch](docs/dispatch.md)**: agents invoke each other at runtime with depth, fanout, and dedup safety limits.
- **[SQLite-backed fleet](docs/configuration.md)**: state lives in a SQLite database, managed through the three interfaces above. `config.yaml` is an optional export/import format, not a runtime dependency.
- **Transparent**: every agent action is a GitHub comment, issue, or PR. Reviewable. Revertable.

## Get started

See [`docs/quickstart.md`](docs/quickstart.md) to get the daemon running on a repo in a few minutes — `docker compose up -d` is the recommended path.

## Contributing

Both human and agent contributions are welcome: issues, PRs, doc fixes, prompts, ideas. The autonomous fleet picks up issues and PRs labeled `ai ready` (the maintainer's opt-in signal); everything else is reviewed and merged by humans. See [CONTRIBUTING.md](CONTRIBUTING.md) for the full flow and [docs/architecture.md](docs/architecture.md) for the Go package layout and how a request flows through it.

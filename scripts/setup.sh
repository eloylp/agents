#!/usr/bin/env bash
# agents-setup — interactive AI-CLI auth bootstrap for the agents daemon.
#
# Run inside the daemon's container:
#
#     docker compose exec -it agents agents-setup
#
# The script does only what genuinely benefits from interactive shell
# access: log into the AI CLIs (Claude / Codex), wire up the GitHub MCP
# server on each, and refresh backend discovery so the daemon sees the
# newly authenticated tooling. Fleet configuration (agents, skills,
# repos, bindings, webhooks) is the operator's job through the
# dashboard at /ui/ or the CRUD / MCP surfaces — those are graphical
# tasks that don't fit a bash prompt loop.

set -euo pipefail

DAEMON_URL="${DAEMON_URL:-http://localhost:8080}"
GITHUB_MCP_URL="${GITHUB_MCP_URL:-https://api.githubcopilot.com/mcp}"

# ── helpers ──────────────────────────────────────────────────────────

c_bold=$'\033[1m'; c_dim=$'\033[2m'; c_red=$'\033[31m'; c_grn=$'\033[32m'; c_ylw=$'\033[33m'; c_rst=$'\033[0m'

say()  { printf '%s\n' "$*"; }
hdr()  { printf '\n%s%s%s\n' "$c_bold" "$*" "$c_rst"; }
ok()   { printf '%s✓%s %s\n' "$c_grn" "$c_rst" "$*"; }
warn() { printf '%s!%s %s\n' "$c_ylw" "$c_rst" "$*"; }
err()  { printf '%s✗%s %s\n' "$c_red" "$c_rst" "$*"; }
ask()  { local prompt="$1" varname="$2"; printf '%s ' "$prompt"; read -r "$varname"; }

require_cmd() { command -v "$1" >/dev/null 2>&1 || { err "missing required command: $1"; exit 1; }; }

require_in_container() {
  if [[ ! -f /.dockerenv ]] && [[ "${container:-}" != "docker" ]]; then
    err "agents-setup must run inside the daemon container."
    say "    invoke it with:  docker compose exec -it agents agents-setup"
    exit 1
  fi
}

require_daemon_up() {
  if ! curl -fsS -m 3 "$DAEMON_URL/status" >/dev/null 2>&1; then
    err "daemon not reachable at $DAEMON_URL/status"
    say "    is the agents container running?  docker compose ps"
    exit 1
  fi
  ok "daemon reachable at $DAEMON_URL"
}

# ── phase 1 — sanity checks ─────────────────────────────────────────

phase_sanity() {
  hdr "Phase 1 — sanity checks"
  require_in_container
  require_cmd curl
  require_cmd jq
  require_cmd claude
  require_cmd codex
  require_daemon_up
}

# ── phase 2 — backend selection ─────────────────────────────────────

phase_pick_backends() {
  hdr "Phase 2 — pick AI backend(s) to authenticate"
  say "Options:"
  say "  c — Claude Code only"
  say "  d — Codex only"
  say "  b — both (default)"
  local choice
  ask "Choice [c/d/b]:" choice
  case "${choice:-b}" in
    c|C) BACKENDS=(claude) ;;
    d|D) BACKENDS=(codex) ;;
    b|B|"") BACKENDS=(claude codex) ;;
    *) err "unknown choice: $choice"; exit 1 ;;
  esac
  ok "will set up: ${BACKENDS[*]}"
}

# ── phase 3 — per-backend login + GitHub MCP wiring ─────────────────

setup_claude() {
  hdr "Claude — log in"
  say "${c_dim}A browser OAuth flow follows. Complete it in your browser, then return here.${c_rst}"
  claude login || { err "claude login failed"; return 1; }
  ok "claude logged in"

  hdr "Claude — register GitHub MCP server"
  if claude mcp list 2>/dev/null | grep -q '^github\b'; then
    ok "GitHub MCP already registered"
  else
    claude mcp add -t http -s user github "$GITHUB_MCP_URL" \
      || { err "failed to register GitHub MCP for claude"; return 1; }
    ok "GitHub MCP registered"
  fi

  hdr "Claude — verify"
  claude mcp list || { err "claude mcp list failed"; return 1; }
}

setup_codex() {
  hdr "Codex — log in"
  say "${c_dim}A browser OAuth flow follows. Complete it in your browser, then return here.${c_rst}"
  codex login || { err "codex login failed"; return 1; }
  ok "codex logged in"

  hdr "Codex — register GitHub MCP server"
  warn "Codex MCP registration UX differs between versions. Follow:"
  say  "    https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-codex.md"
  say  "    GitHub MCP URL: $GITHUB_MCP_URL"
  local cont
  ask  "Press enter when codex MCP is registered (or 's' to skip):" cont
  if [[ "${cont:-}" != "s" && "${cont:-}" != "S" ]]; then
    ok "codex MCP registration acknowledged"
  else
    warn "codex MCP registration skipped — run codex's mcp-add command later"
  fi
}

phase_per_backend() {
  for b in "${BACKENDS[@]}"; do
    case "$b" in
      claude) setup_claude || exit 1 ;;
      codex)  setup_codex  || exit 1 ;;
    esac
  done
}

# ── phase 4 — refresh backend discovery ─────────────────────────────

phase_refresh_discovery() {
  hdr "Phase 4 — refresh backend discovery"
  curl -fsS -X POST "$DAEMON_URL/backends/discover" >/dev/null \
    || { err "POST /backends/discover failed"; return 1; }
  ok "discovery refreshed"
}

# ── phase 5 — diagnostics ───────────────────────────────────────────

phase_diagnostics() {
  hdr "Phase 5 — diagnostics"

  say "${c_bold}/status${c_rst}"
  curl -fsS "$DAEMON_URL/status" | jq '{status, uptime_seconds, queue: .queues.events, orphaned_agents}'

  say "\n${c_bold}/backends/status${c_rst}"
  curl -fsS "$DAEMON_URL/backends/status" | jq '.backends | map({name, healthy, models, health_detail})'

  say "\n${c_bold}/agents/orphans/status${c_rst}"
  curl -fsS "$DAEMON_URL/agents/orphans/status" | jq '{count, agents}'
}

# ── done ────────────────────────────────────────────────────────────

phase_done() {
  hdr "Setup complete"
  ok "AI CLIs authenticated and persisted in the agents-home volume"
  ok "backend discovery refreshed"
  say ""
  say "Next steps:"
  say "  • Open the dashboard:        $DAEMON_URL/ui/"
  say "  • Configure agents/skills/repos via the UI or CRUD API"
  say "  • Wire GitHub webhooks to:   <your-public-base-url>/webhooks/github"
  say "                               (use the GITHUB_WEBHOOK_SECRET from your .env)"
}

# ── main ────────────────────────────────────────────────────────────

main() {
  phase_sanity
  phase_pick_backends
  phase_per_backend
  phase_refresh_discovery
  phase_diagnostics
  phase_done
}

main "$@"

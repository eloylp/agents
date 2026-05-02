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

# ── colors ───────────────────────────────────────────────────────────

if [[ -t 1 ]] && [[ "${NO_COLOR:-}" == "" ]]; then
  c_bold=$'\033[1m'; c_dim=$'\033[2m'
  c_red=$'\033[31m'; c_grn=$'\033[32m'; c_ylw=$'\033[33m'
  c_blu=$'\033[34m'; c_mag=$'\033[35m'; c_cyn=$'\033[36m'
  c_rst=$'\033[0m'
else
  c_bold=""; c_dim=""; c_red=""; c_grn=""; c_ylw=""
  c_blu=""; c_mag=""; c_cyn=""; c_rst=""
fi

# ── output helpers ───────────────────────────────────────────────────

say()   { printf '%s\n' "$*"; }
info()  { printf '  %s→%s %s\n' "$c_cyn" "$c_rst" "$*"; }
ok()    { printf '  %s✓%s %s\n' "$c_grn" "$c_rst" "$*"; }
warn()  { printf '  %s!%s %s\n' "$c_ylw" "$c_rst" "$*"; }
err()   { printf '  %s✗%s %s\n' "$c_red" "$c_rst" "$*" >&2; }
note()  { printf '  %s%s%s\n'   "$c_dim" "$*" "$c_rst"; }
ask()   { local prompt="$1" varname="$2"; printf '  %s?%s %s ' "$c_mag" "$c_rst" "$prompt"; read -r "$varname"; }

phase() {
  local n="$1" title="$2"
  printf '\n%s━━━ Phase %s ━━━ %s %s%s\n\n' \
    "$c_blu" "$n" "$title" "$(printf '━%.0s' $(seq 1 $((50 - ${#title} - 14))))" "$c_rst"
}

banner() {
  printf '%s' "$c_blu"
  cat <<'BANNER'

   ╭───────────────────────────────────────────────────────╮
   │                                                       │
   │     a g e n t s   —   interactive setup wizard        │
   │                                                       │
   │     authenticates AI CLIs · wires GitHub MCP ·        │
   │     refreshes backend discovery                       │
   │                                                       │
   ╰───────────────────────────────────────────────────────╯
BANNER
  printf '%s\n' "$c_rst"
  note "fleet config (agents, skills, repos, webhooks) lives in $DAEMON_URL/ui/"
  note "this script only handles what needs a real terminal."
}

farewell() {
  printf '\n%s' "$c_grn"
  cat <<'BANNER'
   ╭───────────────────────────────────────────────────────╮
   │   ✓  s e t u p   c o m p l e t e                      │
   ╰───────────────────────────────────────────────────────╯
BANNER
  printf '%s\n' "$c_rst"
}

# ── prerequisites ────────────────────────────────────────────────────

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { err "missing required command: $1"; exit 1; }
}

require_in_container() {
  if [[ ! -f /.dockerenv ]] && [[ "${container:-}" != "docker" ]]; then
    err "agents-setup must run inside the daemon container."
    say  "    invoke it with:  ${c_bold}docker compose exec -it agents agents-setup${c_rst}"
    exit 1
  fi
}

require_daemon_up() {
  info "pinging daemon at $DAEMON_URL/status..."
  if ! curl -fsS -m 3 "$DAEMON_URL/status" >/dev/null 2>&1; then
    err "daemon not reachable at $DAEMON_URL/status"
    say "    is the agents container running?  ${c_bold}docker compose ps${c_rst}"
    exit 1
  fi
  local uptime
  uptime=$(curl -fsS "$DAEMON_URL/status" | jq -r '.uptime_seconds')
  ok "daemon reachable (uptime ${uptime}s)"
}

# ── phase 1 — sanity checks ──────────────────────────────────────────

phase_sanity() {
  phase 1 "sanity checks"
  info "checking container context (looking for /.dockerenv)..."
  require_in_container
  ok   "running inside the agents container"

  info "verifying required tooling is on PATH..."
  for cmd in curl jq claude codex; do
    require_cmd "$cmd"
  done
  ok   "curl, jq, claude, codex all present"

  require_daemon_up
}

# ── phase 2 — backend selection ──────────────────────────────────────

phase_pick_backends() {
  phase 2 "pick AI backend(s) to authenticate"
  say "  ${c_bold}Most operators only have an account with one of the two.${c_rst}"
  say "  Pick whichever you actually have — you can always add the other later."
  say ""
  say "    ${c_bold}c${c_rst}   Claude Code  (Anthropic)"
  say "    ${c_bold}d${c_rst}   Codex        (OpenAI)"
  say "    ${c_bold}b${c_rst}   both         ${c_dim}(default — press Enter)${c_rst}"
  say ""
  local choice
  ask "Choice [c/d/b]:" choice
  case "${choice:-b}" in
    c|C) BACKENDS=(claude) ;;
    d|D) BACKENDS=(codex) ;;
    b|B|"") BACKENDS=(claude codex) ;;
    *) err "unknown choice: '$choice'"; exit 1 ;;
  esac
  ok "will authenticate: ${c_bold}${BACKENDS[*]}${c_rst}"
}

# ── phase 3 — per-backend login + GitHub MCP wiring ──────────────────

setup_claude() {
  phase 3 "claude — log in + wire GitHub MCP"
  info "starting claude OAuth flow..."
  note "claude prints a URL. Open it in any browser, sign in, paste the"
  note "returned code back into this terminal. Auth lands in /home/agents"
  note "(the agents-home named volume) and is reused on every run."
  say ""
  claude login || { err "claude login failed"; return 1; }
  ok "claude logged in"

  info "checking whether GitHub MCP is already registered..."
  if claude mcp list 2>/dev/null | grep -qE '^github\b'; then
    ok "GitHub MCP already registered (skipping add)"
  else
    info "registering GitHub MCP server (user-scope) at $GITHUB_MCP_URL..."
    claude mcp add -t http -s user github "$GITHUB_MCP_URL" \
      || { err "failed to register GitHub MCP for claude"; return 1; }
    ok "GitHub MCP registered"
  fi

  info "verifying with claude mcp list..."
  say ""
  claude mcp list || { err "claude mcp list failed"; return 1; }
  say ""
}

setup_codex() {
  phase 3 "codex — log in + wire GitHub MCP"
  info "starting codex OAuth flow (device-auth mode for headless)..."
  note "codex prints a URL and a one-time code. Open the URL in any"
  note "browser (your laptop, your phone — anything), enter the code,"
  note "approve, return here. --device-auth avoids needing a localhost"
  note "callback that the container can't expose to your browser."
  say ""
  codex login --device-auth || { err "codex login failed"; return 1; }
  ok "codex logged in"

  info "registering GitHub MCP for codex..."
  warn "codex's MCP registration UX varies by version — please follow:"
  say  "    ${c_bold}https://github.com/github/github-mcp-server/blob/main/docs/installation-guides/install-codex.md${c_rst}"
  say  "    GitHub MCP URL to use: ${c_bold}$GITHUB_MCP_URL${c_rst}"
  local cont
  ask  "Press Enter when codex MCP is registered (or 's' to skip):" cont
  if [[ "${cont:-}" == "s" || "${cont:-}" == "S" ]]; then
    warn "codex MCP registration skipped — agents on codex won't reach GitHub until you wire it"
  else
    ok "codex MCP registration acknowledged"
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

# ── phase 4 — refresh backend discovery ──────────────────────────────

phase_refresh_discovery() {
  phase 4 "refresh backend discovery"
  info "POST $DAEMON_URL/backends/discover (probes each CLI, persists model catalog)..."
  curl -fsS -X POST "$DAEMON_URL/backends/discover" >/dev/null \
    || { err "POST /backends/discover failed"; return 1; }
  ok "discovery refreshed — daemon sees the freshly-authenticated CLIs"
}

# ── phase 5 — diagnostics ────────────────────────────────────────────

phase_diagnostics() {
  phase 5 "diagnostics"

  info "fetching ${c_bold}/status${c_rst} (uptime, queue depth, orphans)..."
  curl -fsS "$DAEMON_URL/status" \
    | jq '{status, uptime_seconds, queue: .queues.events, orphaned_agents}'

  info "fetching ${c_bold}/backends/status${c_rst} (per-backend health + model catalog)..."
  curl -fsS "$DAEMON_URL/backends/status" \
    | jq '.backends | map({name, healthy, models, health_detail})'

  info "fetching ${c_bold}/agents/orphans/status${c_rst} (agents pinning unavailable models)..."
  curl -fsS "$DAEMON_URL/agents/orphans/status" \
    | jq '{count, agents}'
}

# ── done ─────────────────────────────────────────────────────────────

phase_done() {
  farewell
  ok "AI CLIs authenticated; auth persists in the agents-home volume"
  ok "backend discovery refreshed; daemon's catalog is current"
  say ""
  say "  ${c_bold}Next steps:${c_rst}"
  say "    • open the dashboard:        ${c_cyn}$DAEMON_URL/ui/${c_rst}"
  say "    • configure agents/skills/repos via the UI or CRUD API"
  say "    • wire GitHub webhooks to:   ${c_cyn}<your-public-base-url>/webhooks/github${c_rst}"
  say "                                 ${c_dim}(use GITHUB_WEBHOOK_SECRET from your .env)${c_rst}"
  say ""
}

# ── main ─────────────────────────────────────────────────────────────

main() {
  banner
  phase_sanity
  phase_pick_backends
  phase_per_backend
  phase_refresh_discovery
  phase_diagnostics
  phase_done
}

main "$@"

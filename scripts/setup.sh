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

# ── phase 2.5 — GitHub PAT presence check ───────────────────────────

phase_check_pat() {
  phase "2.5" "verify GitHub Personal Access Token"
  info "looking for GITHUB_PAT_TOKEN in the container environment..."
  if [[ -z "${GITHUB_PAT_TOKEN:-}" ]]; then
    err "GITHUB_PAT_TOKEN is not set."
    say ""
    say "  The GitHub MCP server needs a Personal Access Token to talk to"
    say "  GitHub on behalf of your agents. Generate one at:"
    say "      ${c_cyn}https://github.com/settings/tokens${c_rst}"
    say "  with at least the ${c_bold}repo${c_rst} scope (and ${c_bold}workflow${c_rst} if your agents will touch CI)."
    say ""
    say "  Then add it to your .env (next to docker-compose.yaml):"
    say "      ${c_bold}GITHUB_PAT_TOKEN=ghp_...${c_rst}"
    say ""
    say "  Restart the container so the env var is visible inside:"
    say "      ${c_bold}docker compose up -d${c_rst}"
    say ""
    say "  Then re-run agents-setup."
    exit 1
  fi
  ok "GITHUB_PAT_TOKEN is present (${#GITHUB_PAT_TOKEN} chars)"
  note "claude stores the token in ~/.claude.json on the agents-home volume."
  note "codex resolves it from \$GITHUB_PAT_TOKEN at runtime (token never on disk)."
}

# ── phase 3 — per-backend login + GitHub MCP wiring ──────────────────

setup_claude() {
  phase 3 "claude — log in + wire GitHub MCP"

  info "running ${c_bold}claude auth login${c_rst} (clean auth, no REPL drop)..."
  note "claude prints a URL. Open it in any browser, sign in, paste the"
  note "returned code back into this terminal. Auth lands in /home/agents"
  note "(the agents-home named volume) and is reused on every run."
  say ""
  claude auth login || { err "claude auth login failed"; return 1; }
  ok "claude authenticated"

  info "checking whether GitHub MCP is already registered for claude..."
  if claude mcp list 2>/dev/null | grep -qE '^github\b.*Connected'; then
    ok "GitHub MCP already registered and connected (skipping add)"
  else
    # Remove any prior failed registration so the add doesn't conflict.
    claude mcp remove github 2>/dev/null || true
    info "registering GitHub MCP via add-json (HTTP + Bearer PAT)..."
    local mcp_json
    mcp_json=$(jq -nc \
      --arg url "$GITHUB_MCP_URL" \
      --arg auth "Bearer $GITHUB_PAT_TOKEN" \
      '{type:"http", url:$url, headers:{Authorization:$auth}}')
    claude mcp add-json github "$mcp_json" \
      || { err "claude mcp add-json failed"; return 1; }
    ok "GitHub MCP registered with PAT-based Bearer auth"
  fi

  info "verifying GitHub MCP connectivity..."
  local listing
  listing=$(claude mcp list 2>&1)
  if printf '%s' "$listing" | grep -qE '^github\b.*Connected'; then
    ok "github MCP shows Connected"
  else
    err "github MCP did not connect — claude mcp list said:"
    printf '%s\n' "$listing" | grep -E '^github\b' | sed 's/^/      /'
    say "    Likely causes: invalid PAT, missing scopes, or rate limit."
    say "    Check the token has 'repo' scope, then re-run agents-setup."
    return 1
  fi
}

setup_codex() {
  phase 3 "codex — log in + wire GitHub MCP"

  info "running ${c_bold}codex login --device-auth${c_rst} (headless device-auth flow)..."
  note "codex prints a URL and a one-time code. Open the URL in any"
  note "browser (your laptop, your phone — anything), enter the code,"
  note "approve, return here. --device-auth avoids needing a localhost"
  note "callback that the container can't expose to your browser."
  say ""
  codex login --device-auth || { err "codex login failed"; return 1; }
  ok "codex authenticated"

  info "checking whether GitHub MCP is already registered for codex..."
  if codex mcp list 2>/dev/null | grep -qE '^github\b'; then
    ok "GitHub MCP already registered (skipping add)"
  else
    info "registering GitHub MCP via codex mcp add (HTTP + bearer-token-env-var)..."
    note "codex resolves the PAT from \$GITHUB_PAT_TOKEN at runtime, not at rest."
    codex mcp add github \
      --url "$GITHUB_MCP_URL/" \
      --bearer-token-env-var GITHUB_PAT_TOKEN \
      || { err "codex mcp add failed"; return 1; }
    ok "GitHub MCP registered for codex"
  fi

  info "verifying codex MCP listing..."
  say ""
  codex mcp list || { err "codex mcp list failed"; return 1; }
  say ""
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
  phase_check_pat
  phase_per_backend
  phase_refresh_discovery
  phase_diagnostics
  phase_done
}

main "$@"

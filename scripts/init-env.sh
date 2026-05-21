#!/bin/sh
set -eu

ENV_FILE=${AGENTS_ENV_FILE:-.env}
SAMPLE_FILE=${AGENTS_ENV_SAMPLE:-.env.sample}

if [ -t 1 ] && command -v tput >/dev/null 2>&1; then
  BOLD=$(tput bold 2>/dev/null || true)
  DIM=$(tput dim 2>/dev/null || true)
  RESET=$(tput sgr0 2>/dev/null || true)
  CYAN=$(tput setaf 6 2>/dev/null || true)
  GREEN=$(tput setaf 2 2>/dev/null || true)
else
  BOLD=
  DIM=
  RESET=
  CYAN=
  GREEN=
fi

say() {
  printf '%s\n' "$*"
}

section() {
  say ""
  printf '%s%s%s\n' "$BOLD$CYAN" "$*" "$RESET"
  say ""
}

ok() {
  printf '%sOK%s %s\n' "$GREEN" "$RESET" "$*"
}

prompt() {
  printf '%s? %s%s' "$BOLD" "$*" "$RESET" >&2
}

read_answer() {
  message=$1
  secret=${2:-false}
  value=

  prompt "$message"
  if [ -t 0 ]; then
    if [ "$secret" = "true" ] && stty -echo 2>/dev/null; then
      IFS= read -r value || value=
      stty echo 2>/dev/null || true
      printf '\n' >&2
    else
      IFS= read -r value || value=
    fi
  else
    IFS= read -r value || value=
  fi

  printf '%s' "$value"
}

get_env() {
  key=$1
  [ -f "$ENV_FILE" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      "$key"=*) printf '%s\n' "${line#*=}"; return 0 ;;
    esac
  done < "$ENV_FILE"
}

set_env() {
  key=$1
  value=$2
  tmp="$ENV_FILE.tmp.$$"
  found=0
  : > "$tmp"
  if [ -f "$ENV_FILE" ]; then
    while IFS= read -r line || [ -n "$line" ]; do
      case "$line" in
        "$key"=*)
          printf '%s=%s\n' "$key" "$value" >> "$tmp"
          found=1
          ;;
        *)
          printf '%s\n' "$line" >> "$tmp"
          ;;
      esac
    done < "$ENV_FILE"
  fi
  if [ "$found" -eq 0 ]; then
    printf '%s=%s\n' "$key" "$value" >> "$tmp"
  fi
  chmod 600 "$tmp"
  mv "$tmp" "$ENV_FILE"
}

generate_secret() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  else
    od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
  fi
}

ensure_env_file() {
  if [ -f "$ENV_FILE" ]; then
    chmod 600 "$ENV_FILE" 2>/dev/null || true
    return
  fi
  if [ -f "$SAMPLE_FILE" ]; then
    cp "$SAMPLE_FILE" "$ENV_FILE"
  else
    : > "$ENV_FILE"
  fi
  chmod 600 "$ENV_FILE" 2>/dev/null || true
}

prompt_optional() {
  key=$1
  message=$2
  secret=${3:-true}
  current=$(get_env "$key" || true)
  if [ -n "$current" ]; then
    say "${DIM}$key is already set. Leave blank to keep it.${RESET}"
  fi
  value=$(read_answer "$message" "$secret")
  if [ -n "$value" ]; then
    set_env "$key" "$value"
    ok "Saved $key"
  fi
}

prompt_choice() {
  message=$1
  while :; do
    choice=$(read_answer "$message" false)
    case "$choice" in
      ""|1|2|3) printf '%s' "$choice"; return 0 ;;
      *)
        printf '%s\n' "Please enter 1, 2, or 3." >&2
        if [ ! -t 0 ]; then
          printf '3'
          return 0
        fi
        ;;
    esac
  done
}

ensure_env_file

section "Webhook secret"
if [ -z "$(get_env GITHUB_WEBHOOK_SECRET || true)" ]; then
  set_env GITHUB_WEBHOOK_SECRET "$(generate_secret)"
  ok "Generated GITHUB_WEBHOOK_SECRET"
else
  ok "GITHUB_WEBHOOK_SECRET is already set"
fi

section "GitHub access"
say "Create a GitHub token with repo scope. Add workflow scope if agents will touch CI."
say "Token page: https://github.com/settings/tokens"
say ""
prompt_optional GITHUB_TOKEN "Paste GITHUB_TOKEN and press Enter (input hidden; blank to skip): " true

section "Git commit identity"
say "These values are configured inside each runner container before the AI CLI starts."
say "Set them explicitly so agents do not invent commit authors."
say ""

current_name=$(get_env AGENTS_GIT_USER_NAME || true)
if [ -z "$current_name" ]; then
  set_env AGENTS_GIT_USER_NAME "Agents Bot"
  ok "Defaulted AGENTS_GIT_USER_NAME to Agents Bot"
fi

current_email=$(get_env AGENTS_GIT_USER_EMAIL || true)
if [ -z "$current_email" ]; then
  set_env AGENTS_GIT_USER_EMAIL "agents@example.com"
  ok "Defaulted AGENTS_GIT_USER_EMAIL to agents@example.com"
fi

prompt_optional AGENTS_GIT_USER_NAME "Type git user name and press Enter (blank to keep current): " false
prompt_optional AGENTS_GIT_USER_EMAIL "Type git user email and press Enter (blank to keep current): " false

section "Claude credentials"
say "Preferred path: run 'claude setup-token' locally and paste the returned token."
say "This sets CLAUDE_CODE_OAUTH_TOKEN for runner containers."
say ""
prompt_optional CLAUDE_CODE_OAUTH_TOKEN "Paste CLAUDE_CODE_OAUTH_TOKEN and press Enter (input hidden; blank to skip): " true

section "Codex credentials"
say "Select one Codex authentication mode:"
say "  1. ChatGPT/Codex subscription auth (CODEX_AUTH_JSON_BASE64)."
say "     Uses ~/.codex/auth.json. Caveat: refreshed session state is not persisted out of ephemeral runners."
say "  2. OpenAI Platform API billing (OPENAI_API_KEY)."
say "     Better for stateless, parallel automation."
say "  3. Skip Codex credentials for now."
say ""
codex_choice=$(prompt_choice "Type 1, 2, or 3 and press Enter (blank to keep/skip): ")

case "$codex_choice" in
  1)
    say ""
    say "Prepare this value with:"
    say "  1. Add 'cli_auth_credentials_store = \"file\"' to ~/.codex/config.toml"
    say "  2. Run 'codex login' locally"
    say "  3. Run: base64 < ~/.codex/auth.json | tr -d '\\n'"
    say ""
    say "Paste the base64 value below. It is secret-equivalent to a password."
    if [ -n "$(get_env OPENAI_API_KEY || true)" ]; then
      set_env OPENAI_API_KEY ""
      ok "Cleared OPENAI_API_KEY so CODEX_AUTH_JSON_BASE64 can be used"
    fi
    say ""
    prompt_optional CODEX_AUTH_JSON_BASE64 "Paste CODEX_AUTH_JSON_BASE64 and press Enter (input hidden; blank to skip): " true
    ;;
  2)
    say ""
    say "This stores your OpenAI Platform API credential as OPENAI_API_KEY."
    if [ -n "$(get_env CODEX_AUTH_JSON_BASE64 || true)" ]; then
      set_env CODEX_AUTH_JSON_BASE64 ""
      ok "Cleared CODEX_AUTH_JSON_BASE64 so OPENAI_API_KEY can be used"
    fi
    say ""
    prompt_optional OPENAI_API_KEY "Paste OPENAI_API_KEY and press Enter (input hidden; blank to skip): " true
    ;;
  3)
    ok "Skipped Codex credentials"
    ;;
  "")
    ok "Kept existing Codex credential settings"
    ;;
esac

say ""
ok "Wrote $ENV_FILE"

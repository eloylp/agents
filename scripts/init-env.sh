#!/bin/sh
set -eu

ENV_FILE=${AGENTS_ENV_FILE:-.env}
SAMPLE_FILE=${AGENTS_ENV_SAMPLE:-.env.sample}

say() {
  printf '%s\n' "$*"
}

read_secret() {
  prompt=$1
  if [ -t 0 ]; then
    printf '%s' "$prompt"
    if stty -echo 2>/dev/null; then
      IFS= read -r value || value=
      stty echo 2>/dev/null || true
      printf '\n'
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
  prompt=$2
  current=$(get_env "$key" || true)
  if [ -n "$current" ]; then
    say "$key is already set. Leave blank to keep it."
  fi
  value=$(read_secret "$prompt")
  if [ -n "$value" ]; then
    set_env "$key" "$value"
  fi
}

ensure_env_file

if [ -z "$(get_env GITHUB_WEBHOOK_SECRET || true)" ]; then
  set_env GITHUB_WEBHOOK_SECRET "$(generate_secret)"
  say "Generated GITHUB_WEBHOOK_SECRET."
else
  say "GITHUB_WEBHOOK_SECRET is already set."
fi

say ""
say "GitHub token"
say "Create a GitHub token with repo scope. Add workflow scope if agents will touch CI."
say "Token page: https://github.com/settings/tokens"
prompt_optional GITHUB_TOKEN "Paste GITHUB_TOKEN (blank to keep/skip): "

say ""
say "Claude credentials"
say "Preferred path: run 'claude setup-token' locally and paste the returned token."
say "This sets CLAUDE_CODE_OAUTH_TOKEN for runner containers."
prompt_optional CLAUDE_CODE_OAUTH_TOKEN "Paste CLAUDE_CODE_OAUTH_TOKEN (blank to keep/skip): "

say ""
say "Codex credentials"
say "Preferred path for ChatGPT/Plus/Pro subscription usage:"
say "  1. Add 'cli_auth_credentials_store = \"file\"' to ~/.codex/config.toml"
say "  2. Run 'codex login' locally"
say "  3. Run: base64 < ~/.codex/auth.json | tr -d '\\n'"
say "Paste that value below. It is secret-equivalent to a password."
prompt_optional CODEX_AUTH_JSON_BASE64 "Paste CODEX_AUTH_JSON_BASE64 (blank to keep/skip): "

if [ -z "$(get_env CODEX_AUTH_JSON_BASE64 || true)" ]; then
  say ""
  say "Optional Codex API fallback"
  say "Use OPENAI_API_KEY only when you want OpenAI Platform API-billed Codex usage."
  prompt_optional OPENAI_API_KEY "Paste OPENAI_API_KEY (blank to keep/skip): "
fi

say ""
say "Wrote $ENV_FILE. Review it, then start the daemon with: docker compose up -d"

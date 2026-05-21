#!/usr/bin/env bash
set -euo pipefail

BASE_URL=${AGENTS_QUICKSTART_BASE_URL:-https://raw.githubusercontent.com/eloylp/agents/${AGENTS_QUICKSTART_REF:-main}}
INSTALL_DIR=${AGENTS_QUICKSTART_DIR:-agents}

if [[ -t 1 ]] && command -v tput >/dev/null 2>&1; then
  BOLD=$(tput bold || true)
  DIM=$(tput dim || true)
  RESET=$(tput sgr0 || true)
  CYAN=$(tput setaf 6 || true)
  GREEN=$(tput setaf 2 || true)
  YELLOW=$(tput setaf 3 || true)
else
  BOLD=
  DIM=
  RESET=
  CYAN=
  GREEN=
  YELLOW=
fi

say() {
  printf '%b\n' "$*"
}

step() {
  say ""
  say "${BOLD}${CYAN}$*${RESET}"
}

ok() {
  say "${GREEN}OK${RESET} $*"
}

warn() {
  say "${YELLOW}WARN${RESET} $*"
}

fail() {
  say ""
  say "${YELLOW}ERROR${RESET} $*" >&2
  exit 1
}

logo() {
  say "${CYAN}${BOLD}"
  cat <<'EOF'
    _                    _        .----.   .----.   .----.
   / \   __ _  ___ _ __ | |_ ___  |o  o|<->|o  o|<->|o  o|
  / _ \ / _` |/ _ \ '_ \| __/ __| | -- |   | -- |   | -- |
 / ___ \ (_| |  __/ | | | |_\__ \  '----'   '----'   '----'
/_/   \_\__, |\___|_| |_|\__|___/    /|\      /|\      /|\
        |___/
EOF
  say "${RESET}${BOLD}Self-hosted agent orchestration, ready for Docker.${RESET}"
  say "${DIM}This installer downloads the Compose bundle, prepares .env, and starts the daemon.${RESET}"
}

need_cmd() {
  local cmd=$1
  if ! command -v "$cmd" >/dev/null 2>&1; then
    fail "Missing required command: $cmd"
  fi
}

download_if_missing() {
  local url=$1
  local dest=$2
  if [[ -f "$dest" ]]; then
    ok "Keeping existing $dest"
    return
  fi
  say "Downloading $dest"
  curl -fsSL "$url" -o "$dest"
}

refresh_file() {
  local url=$1
  local dest=$2
  local tmp
  tmp="$dest.tmp.$$"
  say "Refreshing $dest"
  curl -fsSL "$url" -o "$tmp"
  mv "$tmp" "$dest"
}

logo

step "1. Checking local requirements"
need_cmd curl
need_cmd docker

if ! docker compose version >/dev/null 2>&1; then
  fail "Docker Compose v2 is required. Install Docker with the 'docker compose' plugin and rerun this script."
fi
ok "Found curl, Docker, and Docker Compose v2"

step "2. Preparing ./$(basename "$INSTALL_DIR")"
mkdir -p "$INSTALL_DIR"
cd "$INSTALL_DIR"
mkdir -p scripts
ok "Using $(pwd)"

step "3. Downloading quickstart files"
say "Source: $BASE_URL"
download_if_missing "$BASE_URL/docker-compose.yaml" docker-compose.yaml
download_if_missing "$BASE_URL/.env.sample" .env.sample
refresh_file "$BASE_URL/scripts/init-env.sh" scripts/init-env.sh
chmod +x scripts/init-env.sh 2>/dev/null || true

step "4. Configuring credentials"
say "You can leave optional backend credentials blank and add them later."
if [[ -r /dev/tty ]]; then
  sh scripts/init-env.sh < /dev/tty
else
  warn "No terminal is available for interactive prompts; running credential setup on standard input."
  sh scripts/init-env.sh
fi

step "5. Starting the daemon"
docker compose up -d

say ""
ok "Agents is starting."
say ""
say "${BOLD}Next:${RESET}"
say "  cd $(pwd)"
say "  open http://localhost:8080/"
say ""
say "${DIM}If 'open' is not available on your system, visit http://localhost:8080/ in your browser.${RESET}"

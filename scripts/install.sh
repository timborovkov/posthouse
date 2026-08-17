#!/bin/sh
set -eu

module='github.com/timborovkov/posthouse/cmd/posthouse@latest'

if command -v posthouse >/dev/null 2>&1; then
  echo "posthouse is already on PATH: $(command -v posthouse)"
  echo "version: $(posthouse version)"
  echo "Next: posthouse setup"
  exit 0
fi

if command -v go >/dev/null 2>&1; then
  echo "Installing Posthouse with go install..."
  go install "$module"
  echo "Installed. If the command is not found, add \$(go env GOPATH)/bin to PATH."
  echo "Next: posthouse setup"
  echo "Guide: https://github.com/timborovkov/posthouse/blob/main/GETTING-STARTED.md"
  exit 0
fi

cat <<'EOF'
Go is not installed, so the CLI was not placed on PATH.

Option A — install Go 1.26.6, then rerun this script.
Option B — run Posthouse with Docker instead:

  git clone https://github.com/timborovkov/posthouse
  cd posthouse
  # Install Go later, or generate keys on another machine with: posthouse setup --write-env .env
  cp .env.example .env
  # Put long random values in POSTHOUSE_CACHE_KEY and POSTHOUSE_ACCESS_KEY.
  docker compose up --build

Guide: https://github.com/timborovkov/posthouse/blob/main/GETTING-STARTED.md
EOF
exit 1

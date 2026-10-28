#!/usr/bin/env bash
# Starts the whole backend in one terminal: API, scoring worker and voice agent.
# Output is prefixed per service; Ctrl+C stops everything.
#   make up            everything
#   make up AGENT=0    without the voice agent (browsing, admin, feedback)
set -uo pipefail
cd "$(dirname "$0")/.."

if [ ! -f .env ]; then
  echo "Missing .env — copy .env.example to .env and fill it in." >&2
  exit 1
fi
set -a; . ./.env; set +a

AGENT="${AGENT:-1}"

# Each service runs in its own process group (set -m), so stopping one group
# stops everything it started (go binaries, pnpm, node).
set -m
PIDS=""
cleanup() {
  trap - INT TERM EXIT
  [ -z "$PIDS" ] && return
  set +m # no "Terminated" job notices
  echo
  echo "Stopping…"
  for pid in $PIDS; do kill -TERM -- "-$pid" 2>/dev/null; done
  sleep 1
  for pid in $PIDS; do kill -KILL -- "-$pid" 2>/dev/null; done
  exit 0
}
trap cleanup INT TERM EXIT

# run <name> <color> <command...>: runs a service and prefixes its output.
run() {
  local name="$1" color="$2"; shift 2
  local prefix
  prefix="$(printf '\033[%sm%-6s\033[0m│ ' "$color" "$name")"
  ( "$@" </dev/null 2>&1 | while IFS= read -r line || [ -n "$line" ]; do printf '%s%s\n' "$prefix" "$line"; done ) &
  PIDS="$PIDS $!"
}

echo "Building…"
mkdir -p bin
go build -o bin/api ./cmd/api || exit 1
run api 32 ./bin/api

if [ "${STORE:-postgres}" = "memory" ]; then
  echo "Scoring runs inside the API (STORE=memory)."
else
  case "${RUN_WORKER_IN_API:-false}" in
    true|1|yes) echo "Scoring runs inside the API (RUN_WORKER_IN_API=true)." ;;
    *) go build -o bin/worker ./cmd/worker || exit 1; run worker 35 ./bin/worker ;;
  esac
fi

if [ "$AGENT" = "1" ]; then
  if [ ! -f agent/.env ]; then
    echo "Skipping the voice agent: agent/.env is missing (copy agent/.env.example)." >&2
  else
    (
      cd agent
      [ -d node_modules ] || pnpm install
      # Voice models are downloaded once; delete agent/.models-ready to fetch them again.
      if [ ! -f .models-ready ]; then pnpm download-files && touch .models-ready; fi
    ) || exit 1
    run agent 36 sh -c 'cd agent && exec pnpm dev'
  fi
fi

echo "Running. API on ${HTTP_ADDR:-:8080}. Press Ctrl+C to stop."
# Keep running until Ctrl+C; if a service crashes, its error stays on screen.
# (wait is interruptible, so Ctrl+C is handled at once.)
while :; do sleep 3600 & wait $!; done

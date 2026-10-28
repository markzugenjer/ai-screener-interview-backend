#!/bin/bash
# Single-container runtime for VCS: the Go API listens privately on loopback and
# the Next.js server (public, $PORT) forwards /api/v1 and /internal to it.
# If either process exits, the container exits so the platform restarts it.
set -euo pipefail

export API_PORT="${API_PORT:-8081}"
export HTTP_ADDR="127.0.0.1:${API_PORT}"
export API_INTERNAL_URL="http://127.0.0.1:${API_PORT}"
# Request-based CPU: score inside the requests that wait for it.
export INLINE_JOBS="${INLINE_JOBS:-true}"
# The API is only reachable through our own Next.js server, which reports the host.
export ALLOW_SAME_HOST_ORIGIN="${ALLOW_SAME_HOST_ORIGIN:-true}"
export APP_ENV="${APP_ENV:-production}"
export COOKIE_SECURE="${COOKIE_SECURE:-true}"
export TRUST_PROXY="${TRUST_PROXY:-true}"

/app/bin/api &
api=$!

# Wait (up to ~60s) for the API: it runs database migrations on start.
for _ in $(seq 1 120); do
  if node -e "fetch('${API_INTERNAL_URL}/healthz').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))"; then
    break
  fi
  if ! kill -0 "$api" 2>/dev/null; then
    echo "api exited during startup" >&2
    wait "$api" || exit $?
    exit 1
  fi
  sleep 0.5
done

PORT="${PORT:-8080}" HOSTNAME=0.0.0.0 node /app/web/server.js &
web=$!

trap 'kill -TERM "$api" "$web" 2>/dev/null || true' TERM INT
status=0
wait -n "$api" "$web" || status=$?
kill -TERM "$api" "$web" 2>/dev/null || true
wait || true
exit "$status"

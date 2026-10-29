#!/usr/bin/env bash
# Deploy Dicy Talks (website + API in one container) to VCS project
# dtritama/dicy-talks. Run from this folder on a machine that can reach VCS:
#   make vcs-deploy
# Steps: pin the latest website commit in the Dockerfile, sign in to VCS if
# needed, push production settings on the first deploy, then deploy.
set -euo pipefail
cd "$(dirname "$0")/.."

SLUG="${VCS_SLUG:-dicy-talks}"
FRONTEND_REPO="https://github.com/markzugenjer/ai-screener-interview-frontend"
ENV_FILE=".env.production"
export PATH="$HOME/.local/bin:$PATH"

say() { printf '\n\033[1m%s\033[0m\n' "$*"; }
json_field() { python3 -c 'import json,sys; d=json.loads(sys.stdin.read() or "{}"); print(d.get(sys.argv[1], ""))' "$1"; }

command -v vcs >/dev/null || {
  echo "vcs CLI not found. Install it with:"
  echo "  curl -fsSL https://github.com/fdc-ahay/vcs-release/releases/latest/download/install.sh | sh"
  exit 1
}

if [ -n "$(git status --porcelain)" ]; then
  echo "This folder has uncommitted changes. Commit or stash them first:"
  git status --short
  exit 1
fi

say "1/4  Pinning the latest website commit"
ref=$(git ls-remote "$FRONTEND_REPO" refs/heads/main | cut -f1)
[ ${#ref} -eq 40 ] || { echo "Could not read the website's latest commit from GitHub."; exit 1; }
current=$(sed -n 's/^ARG FRONTEND_REF=//p' Dockerfile | head -1)
if [ "$ref" != "$current" ]; then
  perl -pi -e "s/^ARG FRONTEND_REF=.*/ARG FRONTEND_REF=$ref/" Dockerfile
  git add Dockerfile
  git commit -q -m "chore(deploy): pin website ${ref:0:7} for VCS"
  git push -q origin HEAD || echo "(could not push to GitHub; continuing with the local commit)"
  echo "Website pinned to ${ref:0:7}."
else
  echo "Website already pinned to ${ref:0:7}."
fi

say "2/4  Signing in to VCS"
status=$(vcs auth status || true)
if [ "$(printf '%s' "$status" | json_field status)" != "ready" ]; then
  login=$(vcs auth login)
  url=$(printf '%s' "$login" | json_field url)
  code=$(printf '%s' "$login" | json_field user_code)
  echo "Open this link in the browser where you are signed in to VCS:"
  echo "  $url"
  echo "Check the code matches: $code — then click Approve."
  (command -v open >/dev/null && open "$url") || true
  vcs auth login --wait >/dev/null
  echo "Signed in."
else
  echo "Already signed in."
fi

say "3/4  Deploying to VCS project $SLUG (this takes a few minutes)"
# Bind this folder to the existing VCS project (adds a separate "vcs" git remote;
# the GitHub origin stays as it is). Safe to run again.
adopt=$(vcs agent adopt --slug "$SLUG" --port 8080 --health-path /healthz || true)
case "$(printf '%s' "$adopt" | json_field status)" in
  needs_input|failed) printf '%s\n' "$adopt"; echo; echo "VCS needs something before deploying. Copy the output above into the chat."; exit 1 ;;
esac
# VCS commits its own build-workflow updates ("chore: sync build workflow") to
# the project's copy. Merge those in first so the push is a fast-forward; stop if
# VCS's copy has anything else, rather than overwrite or guess.
if git fetch -q vcs 2>/dev/null && git rev-parse -q --verify vcs/main >/dev/null; then
  if ! git merge-base --is-ancestor vcs/main HEAD; then
    others=$(git diff --name-only HEAD...vcs/main | grep -v '^\.forgejo/' || true)
    if [ -n "$others" ]; then
      echo "VCS's copy of the project has changes outside its build workflow:"
      printf '  %s\n' $others
      echo "Copy this output into the chat; nothing was pushed."
      exit 1
    fi
    git merge -q --no-edit -m "chore(vcs): merge VCS build workflow updates" vcs/main
    git push -q origin HEAD || echo "(could not push to GitHub; continuing with the local commit)"
    echo "Merged VCS's build workflow updates."
  fi
fi
args=(agent deploy --slug "$SLUG" --port 8080 --health-path /healthz --wait 9m)
result=$(vcs "${args[@]}" || true)
code=$(printf '%s' "$result" | json_field code)
if [ "$code" = "MISSING_ENV" ]; then
  [ -f "$ENV_FILE" ] || { echo "VCS needs production settings but $ENV_FILE is missing."; exit 1; }
  echo "Pushing production settings from $ENV_FILE (names only are shown):"
  vcs env push --from "$ENV_FILE"
  result=$(vcs "${args[@]}" || true)
fi

say "4/4  Result"
printf '%s\n' "$result"
case "$(printf '%s' "$result" | json_field status)" in
  succeeded) echo; echo "Live at: $(printf '%s' "$result" | json_field url)" ;;
  running) echo; echo "Still building. Run 'make vcs-deploy' again to follow the same deployment." ;;
  *) echo; echo "Deployment did not finish. Copy the output above into the chat." ; exit 1 ;;
esac

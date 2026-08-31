#!/usr/bin/env bash
# verify-release.sh — confirm a freshly deployed host is actually serving the
# build we just shipped. Run by the pipeline from the agent, right after
# deploy.sh.
#
# /healthz only proves "some fleetdash is up". This proves it is THIS commit's
# binary — catching a silent rollback inside apply-release, or a restart that
# somehow kept the old process. If it fails, the stage fails and promotion
# stops; it does not itself roll back (that stays apply-release's job for
# health failures).
set -euo pipefail

HOST="" WANT="" PORT="8080"
while [ $# -gt 0 ]; do
  case "$1" in
    --host)    HOST="$2"; shift 2 ;;
    --version) WANT="$2"; shift 2 ;;
    --port)    PORT="$2"; shift 2 ;;
    *) echo "verify-release.sh: unknown arg: $1" >&2; exit 2 ;;
  esac
done
[ -n "$HOST" ] || { echo "verify-release.sh: missing --host" >&2; exit 2; }
[ -n "$WANT" ] || { echo "verify-release.sh: missing --version" >&2; exit 2; }

url="http://${HOST}:${PORT}/api/status"
json="$(curl -fsS --max-time 10 "$url")" \
  || { echo "verify-release.sh: cannot reach $url" >&2; exit 1; }

got="$(printf '%s' "$json" \
  | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
[ -n "$got" ] || { echo "verify-release.sh: no version field in $url response" >&2; exit 1; }

# The binary is stamped with the first 12 chars of the commit SHA (see
# azure-pipelines.yml); WANT is the full 40-char SHA, so match on prefix.
case "$WANT" in
  "$got"*) echo "verify-release.sh: ${HOST} confirmed on ${got}" ;;
  *) echo "verify-release.sh: ${HOST} is serving '${got}', expected '${WANT:0:12}' — deploy did not take" >&2
     exit 1 ;;
esac

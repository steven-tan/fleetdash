#!/usr/bin/env bash
# deploy.sh — ship one built fleetdash binary to a target host and apply it.
#
# Run by the Azure DevOps pipeline from the self-hosted agent. It does two small
# things over SSH:
#   1. copy the binary to /tmp/fleetdash.new on the target
#   2. run  sudo /opt/fleetdash/bin/apply-release  (installed by provision-target.sh)
# The apply-release helper does the install / restart / health-check / rollback
# and is the ONLY thing the low-privilege 'deploy' user may sudo.
set -euo pipefail

HOST="" BINARY="" SSH_KEY="" ENV_LABEL="?"
DEPLOY_USER="${DEPLOY_USER:-deploy}"

while [ $# -gt 0 ]; do
  case "$1" in
    --host)    HOST="$2";      shift 2 ;;
    --binary)  BINARY="$2";    shift 2 ;;
    --ssh-key) SSH_KEY="$2";   shift 2 ;;
    --env)     ENV_LABEL="$2"; shift 2 ;;
    *) echo "deploy.sh: unknown arg: $1" >&2; exit 2 ;;
  esac
done

[ -n "$HOST" ]   || { echo "deploy.sh: missing --host" >&2; exit 2; }
[ -n "$BINARY" ] || { echo "deploy.sh: missing --binary" >&2; exit 2; }
[ -f "$BINARY" ] || { echo "deploy.sh: binary not found: $BINARY" >&2; exit 1; }

SSH=(ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10)
SCP=(scp -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10)
if [ -n "$SSH_KEY" ]; then SSH+=(-i "$SSH_KEY"); SCP+=(-i "$SSH_KEY"); fi
TARGET="${DEPLOY_USER}@${HOST}"

echo "==> [${ENV_LABEL}] ship $(basename "$BINARY") -> ${TARGET}"
"${SCP[@]}" "$BINARY" "${TARGET}:/tmp/fleetdash.new"

echo "==> [${ENV_LABEL}] apply release on ${HOST}"
"${SSH[@]}" "$TARGET" 'sudo /opt/fleetdash/bin/apply-release'

echo "==> [${ENV_LABEL}] done"

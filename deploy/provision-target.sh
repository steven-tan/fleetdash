#!/usr/bin/env bash
# provision-target.sh — one-time setup on a fleetdash target host.
#
# Run ONCE per target, as root, from a checkout of this repo:
#
#   sudo FLEET_ENV=dev FLEET_CLOUD=azure FLEET_REGION=eastus \
#        FLEET_PEERS='http://stage-host:8080,http://prod-host:8080,http://adm-host:8080' \
#        DEPLOY_PUBKEY="$(cat fleetdash_deploy_key.pub)" \
#        ./deploy/provision-target.sh
#
# It creates:
#   - 'fleetdash'  : unprivileged service account that runs the app
#   - /opt/fleetdash, /etc/fleetdash/config.env, the systemd unit
#   - /opt/fleetdash/bin/apply-release (root-owned)
#   - 'deploy'     : login user the pipeline SSHes in as, may sudo ONLY apply-release
set -euo pipefail

: "${FLEET_ENV:?set FLEET_ENV=dev|stage|prod|node}"
: "${FLEET_CLOUD:?set FLEET_CLOUD=aws|gcp|azure|home}"
: "${DEPLOY_PUBKEY:?set DEPLOY_PUBKEY to the deploy public key text}"
FLEET_REGION="${FLEET_REGION:-}"
FLEET_PEERS="${FLEET_PEERS:-}"
APP_PORT="${APP_PORT:-8080}"
HERE="$(cd "$(dirname "$0")/.." && pwd)"

echo "==> service account + directories"
id fleetdash &>/dev/null || useradd --system --no-create-home --shell /usr/sbin/nologin fleetdash
install -d -o fleetdash -g fleetdash -m 0755 /opt/fleetdash
install -d -o root      -g root      -m 0755 /opt/fleetdash/bin
install -d -m 0755 /etc/fleetdash

echo "==> /etc/fleetdash/config.env"
cat > /etc/fleetdash/config.env <<EOF
FLEETDASH_LISTEN=:${APP_PORT}
FLEETDASH_NODE=$(hostname)
FLEETDASH_ENV=${FLEET_ENV}
FLEETDASH_CLOUD=${FLEET_CLOUD}
FLEETDASH_REGION=${FLEET_REGION}
FLEETDASH_PEERS=${FLEET_PEERS}
EOF
chmod 0644 /etc/fleetdash/config.env

echo "==> apply-release helper + systemd unit"
install -o root -g root -m 0755 "$HERE/deploy/apply-release"        /opt/fleetdash/bin/apply-release
install -o root -g root -m 0644 "$HERE/systemd/fleetdash.service"   /etc/systemd/system/fleetdash.service
systemctl daemon-reload
systemctl enable fleetdash

echo "==> deploy user + authorized key"
id deploy &>/dev/null || useradd --create-home --shell /bin/bash deploy
install -d -o deploy -g deploy -m 0700 /home/deploy/.ssh
touch /home/deploy/.ssh/authorized_keys
grep -qxF "$DEPLOY_PUBKEY" /home/deploy/.ssh/authorized_keys || echo "$DEPLOY_PUBKEY" >> /home/deploy/.ssh/authorized_keys
chown deploy:deploy /home/deploy/.ssh/authorized_keys
chmod 0600 /home/deploy/.ssh/authorized_keys

echo "==> sudoers: deploy may run ONLY apply-release"
cat > /etc/sudoers.d/fleetdash-deploy <<'EOF'
deploy ALL=(root) NOPASSWD: /opt/fleetdash/bin/apply-release
EOF
chmod 0440 /etc/sudoers.d/fleetdash-deploy
visudo -cf /etc/sudoers.d/fleetdash-deploy

echo "==> done. The first real binary arrives from the pipeline."
echo "    'systemctl start fleetdash' works now but will restart-loop until then."

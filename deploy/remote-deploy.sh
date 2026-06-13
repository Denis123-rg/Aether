#!/usr/bin/env bash
#
# Push the Aether repo to a remote host and bring up the 24/7 Docker stack.
#
# Idempotent: re-run it to ship code changes and rebuild. It rsyncs the repo
# (minus build artifacts), then runs `docker compose ... up -d --build` on the
# remote. The stack stays alive via `restart: unless-stopped`; enabling the
# docker daemon makes it survive reboots too.
#
# Usage:
#   ./deploy/remote-deploy.sh                # uses defaults below
#   SSH_HOST=ancilar-dev ./deploy/remote-deploy.sh
#   PROFILES="ledger" ./deploy/remote-deploy.sh    # also start Postgres
#
# Prereqs on the remote: docker + docker compose v2, and a populated
# deploy/docker/.env (this script never ships your local .env — see below).
set -euo pipefail

# ── Config (override via env) ────────────────────────────────────────────
# NOTE: "ancilar-mark1" does not resolve from this machine and has no SSH
# config block. The working alias is "ancilar-dev" (ssh.ancilar.com via
# cloudflared, user jitender). Override SSH_HOST if the box differs.
SSH_HOST="${SSH_HOST:-ancilar-dev}"
REMOTE_DIR="${REMOTE_DIR:-/home/jitender/aether}"
PROFILES="${PROFILES:-}"           # e.g. "ledger" or "ledger node"
COMPOSE_DIR="${REMOTE_DIR}/deploy/docker"
LOCAL_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

profile_args=""
for p in $PROFILES; do profile_args="$profile_args --profile $p"; done

echo "==> target:   $SSH_HOST:$REMOTE_DIR"
echo "==> profiles: ${PROFILES:-<none>}"

# ── 1. Connectivity check ────────────────────────────────────────────────
if ! ssh -o ConnectTimeout=15 "$SSH_HOST" 'docker compose version >/dev/null 2>&1 && echo ok' | grep -q ok; then
  echo "ERROR: cannot ssh to '$SSH_HOST' or 'docker compose' missing there." >&2
  echo "       Check ~/.ssh/config and that docker(+compose v2) is installed." >&2
  exit 1
fi

# ── 2. Ship the repo (exclude build/VCS cruft; never overwrite remote .env) ─
echo "==> syncing repo …"
ssh "$SSH_HOST" "mkdir -p '$REMOTE_DIR'"
rsync -az --delete \
  --exclude='.git/' \
  --exclude='target/' \
  --exclude='build/' \
  --exclude='node_modules/' \
  --exclude='**/.env' \
  "$LOCAL_ROOT/" "$SSH_HOST:$REMOTE_DIR/"

# ── 3. Verify the remote .env exists (holds the searcher key) ─────────────
if ! ssh "$SSH_HOST" "test -f '$COMPOSE_DIR/.env'"; then
  echo "WARNING: no $COMPOSE_DIR/.env on the remote." >&2
  echo "         Copy deploy/docker/.env.example -> .env there and fill it in," >&2
  echo "         then re-run. Bringing the stack up anyway (vars will be empty)." >&2
fi

# Generate admin token if missing on remote
echo "==> ensuring AETHER_ADMIN_TOKEN on remote …"
ssh "$SSH_HOST" "cd '$COMPOSE_DIR' && \
  if ! grep -q '^AETHER_ADMIN_TOKEN=' .env 2>/dev/null; then \
    echo \"AETHER_ADMIN_TOKEN=\$(openssl rand -hex 32)\" >> .env; \
    echo 'generated AETHER_ADMIN_TOKEN in remote .env'; \
  fi"

# ── 4. Build + start detached ────────────────────────────────────────────
echo "==> building + starting stack …"
ssh "$SSH_HOST" "cd '$COMPOSE_DIR' && \
  docker compose -f docker-compose.yml -f docker-compose.prod.yml $profile_args up -d --build"

# ── 5. Reboot survival (best-effort) ─────────────────────────────────────
ssh "$SSH_HOST" 'sudo -n systemctl enable docker >/dev/null 2>&1 && echo "==> docker daemon enabled on boot" || echo "==> NOTE: run: sudo systemctl enable docker (for reboot survival)"'

# ── 6. Status ────────────────────────────────────────────────────────────
echo "==> stack status:"
ssh "$SSH_HOST" "cd '$COMPOSE_DIR' && docker compose -f docker-compose.yml -f docker-compose.prod.yml ps"
echo
echo "Done. Tail logs with:"
echo "  ssh $SSH_HOST 'cd $COMPOSE_DIR && docker compose logs -f aether-rust aether-go'"
echo "Grafana: http://<remote>:3000  ·  Go dashboard: http://<remote>:8080"

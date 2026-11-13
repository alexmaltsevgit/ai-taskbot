#!/usr/bin/env bash
# Run only as the dedicated deployment account; never via sudo.
set -euo pipefail
umask 077
export DEPLOY_ROOT=${1:?root directory}
export RELEASE_SHA=${2:?commit SHA}
mode=${3:?prepare or deploy}
[[ $DEPLOY_ROOT =~ ^/[a-zA-Z0-9_/-]+$ && $DEPLOY_ROOT != / && $DEPLOY_ROOT != */ ]] || exit 1
[[ $RELEASE_SHA =~ ^[a-f0-9]{40}$ ]] || exit 1
[[ $mode == prepare || $mode == deploy ]] || exit 1
uid=$(id -u)
[[ $uid != 0 ]] || { echo 'Root SSH deployment is forbidden'; exit 1; }
export XDG_RUNTIME_DIR="/run/user/$uid"
export DOCKER_HOST="unix://$XDG_RUNTIME_DIR/docker.sock"
unset DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH DOCKER_CONFIG
docker info --format '{{json .SecurityOptions}}' | grep -q '"name=rootless"' || { echo 'A dedicated rootless Docker engine is required'; exit 1; }
[[ $(docker info --format '{{.Architecture}}') == x86_64 ]] || { echo 'Only amd64 server is supported'; exit 1; }
docker compose version >/dev/null
release="$DEPLOY_ROOT/releases/$RELEASE_SHA"
[[ -d $release && -f $DEPLOY_ROOT/shared/.env && -f $DEPLOY_ROOT/shared/config.json && -d $DEPLOY_ROOT/shared/secrets ]] || { echo 'Server runtime configuration is missing'; exit 1; }
# Prevent concurrent manual and Actions runs, without touching other projects.
exec 9>"$DEPLOY_ROOT/.deploy.lock"
flock -n 9 || { echo 'Another deployment is active'; exit 1; }
mkdir -p "$DEPLOY_ROOT/shared/data" "$DEPLOY_ROOT/shared/codex-auth"
# Rootless container uid 0 maps to this host account; these private bind mounts
# avoid image-owned uid 10001 named-volume permissions with dropped capabilities.
chmod 700 "$DEPLOY_ROOT/shared/data" "$DEPLOY_ROOT/shared/codex-auth"
cd "$release"
sha256sum -c checksums.txt >/dev/null
gzip -dc images.tar.gz | docker load >/dev/null
compose() { docker compose --project-name ai-taskbot-production --env-file "$DEPLOY_ROOT/shared/.env" -f "$1/compose.yaml" "${@:2}"; }
# Do not print expanded configuration: it includes runtime secrets.
compose "$release" config --quiet
compose "$release" run --rm --no-deps bot check-config
if [[ $mode == prepare ]]; then
  echo "Images staged. Run the documented Codex device login on the server, then deploy."
  exit 0
fi
# Authentication check occurs before stopping any current service. It does not
# prove model quota/network availability and must not be described as an E2E test.
compose "$release" run --rm --no-deps worker codex login status >/dev/null 2>&1 || { echo 'Codex login is required; see deployment documentation'; exit 1; }
previous=''
if [[ -f $DEPLOY_ROOT/current ]]; then
  previous=$(cat "$DEPLOY_ROOT/current")
  [[ $previous =~ ^[a-f0-9]{40}$ && -f $DEPLOY_ROOT/releases/$previous/compose.yaml ]] || { echo 'Invalid previous release'; exit 1; }
else
  # Never adopt pre-existing containers implicitly on the first managed deploy.
  [[ -z $(docker ps -aq --filter label=com.docker.compose.project=ai-taskbot-production) ]] || { echo 'Existing unmanaged deployment requires reconciliation'; exit 1; }
fi
restore() {
  if [[ -n $previous ]]; then
    if ! RELEASE_SHA=$previous compose "$DEPLOY_ROOT/releases/$previous" up -d --no-build --pull never; then
      return 1
    fi
    echo 'Previous images restored; state was not overwritten.'
  else
    if ! compose "$release" stop; then return 1; fi
    echo 'First deployment stopped; persistent data retained.'
  fi
}
update_started=false
release_committed=false
finish() {
  local status=$?
  # EXIT also handles set -e failures in assignments, backup setup and metadata
  # writes. Disable traps before restoration to prevent recursive/double rollback.
  trap - EXIT HUP INT TERM
  if [[ $update_started == true && $release_committed != true ]]; then
    if [[ $status == 0 ]]; then status=1; fi
    if ! restore; then
      echo 'ERROR: deployment failed and rollback failed; administrator action is required.' >&2
    fi
  fi
  exit "$status"
}
trap finish EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
# From this point every normal error or handled signal attempts one restoration.
# SIGKILL, host failure and a broken Docker daemon cannot guarantee rollback.
update_started=true
# Stop the poller before creating a consistent snapshot. Never restore a snapshot
# automatically: doing so would forget GitHub operations and permit duplicates.
if [[ -n $previous ]]; then
  RELEASE_SHA=$previous compose "$DEPLOY_ROOT/releases/$previous" stop bot
  mkdir -p "$DEPLOY_ROOT/backups"
  docker run --rm --network none --user 0:0 --entrypoint tar -v "$DEPLOY_ROOT/shared/data:/data:ro" "ai-taskbot-bot:$RELEASE_SHA" -C /data -czf - . >"$DEPLOY_ROOT/backups/state-$(date -u +%Y%m%dT%H%M%SZ)-$RELEASE_SHA.tar.gz"
fi
compose "$release" up -d --no-build --pull never
# Observe sustained process health, not just a successful docker compose command.
healthy=true
for attempt in 1 2 3 4 5 6; do
  sleep 5
  for service in bot worker; do
    container=$(compose "$release" ps -q "$service")
    if [[ -z $container ]] || [[ $(docker inspect --format '{{.State.Running}} {{.RestartCount}}' "$container") != 'true 0' ]]; then healthy=false; break; fi
  done
  [[ $healthy == true ]] || break
done
if [[ $healthy != true ]]; then exit 1; fi
printf '%s\n' "$RELEASE_SHA" >"$DEPLOY_ROOT/current.new"
mv "$DEPLOY_ROOT/current.new" "$DEPLOY_ROOT/current"
release_committed=true
echo 'Deployment process checks passed. Verify the Telegram-to-GitHub flow manually.'

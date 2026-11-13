#!/usr/bin/env bash
# Run as the dedicated account after prepare; never start the worker directly
# through the development Compose file or an externally connected network.
set -euo pipefail
export DEPLOY_ROOT=${1:?root directory}
export RELEASE_SHA=${2:?prepared commit SHA}
[[ $DEPLOY_ROOT =~ ^/[a-zA-Z0-9_/-]+$ && $DEPLOY_ROOT != / && $DEPLOY_ROOT != */ ]] || exit 1
[[ $RELEASE_SHA =~ ^[a-f0-9]{40}$ ]] || exit 1
uid=$(id -u)
[[ $uid != 0 ]] || { echo 'Root login helper is forbidden'; exit 1; }
export XDG_RUNTIME_DIR="/run/user/$uid" DOCKER_HOST="unix:///run/user/$uid/docker.sock"
unset DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH DOCKER_CONFIG
release="$DEPLOY_ROOT/releases/$RELEASE_SHA"
# sudo -u -H may retain an inaccessible caller directory (for example /root).
# Compose inspects the current directory even when all file paths are absolute.
cd "$release"
docker info --format '{{json .SecurityOptions}}' | grep -q '"name=rootless"' || exit 1
grep -qx 'x-ai-taskbot-egress: required-v1' "$release/compose.yaml" || { echo 'This release does not enforce proxy isolation'; exit 1; }
[[ -f $DEPLOY_ROOT/shared/secrets/proxy.json ]] || { echo 'Runtime proxy configuration is missing'; exit 1; }
exec 9>"$DEPLOY_ROOT/.deploy.lock"
flock -n 9 || { echo 'Another deployment/login operation is active'; exit 1; }
compose() { docker compose --project-name ai-taskbot-production --env-file "$DEPLOY_ROOT/shared/.env" -f "$release/compose.yaml" "$@"; }
compose config --quiet
compose run --rm --no-deps --pull never egress check-proxy-config
# Only the egress service has an outward network. Both login and inference use
# the same worker image, environment, isolated app network and persisted auth.
[[ -z $(compose ps -q bot) && -z $(compose ps -q worker) ]] || { echo 'Stop the managed bot/worker before login network verification'; exit 1; }
compose up -d --no-build --pull never egress
sleep 2
compose run --rm --no-deps --pull never worker node -e "$(cat "$release/egress.js")" working
compose stop egress
compose run --rm --no-deps --pull never worker node -e "$(cat "$release/egress.js")" blocked
compose up -d --no-build --pull never egress
compose run --rm --no-deps --pull never worker codex login --device-auth

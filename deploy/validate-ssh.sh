#!/usr/bin/env bash
set -euo pipefail
# Shared connection boundary for deployment and read-only diagnostics.
[[ ${DEPLOY_ENABLED:-} == true ]] || { echo 'SSH workflow is disabled'; exit 1; }
[[ ${DEPLOY_HOST:-} =~ ^[a-zA-Z0-9][a-zA-Z0-9.-]*$ ]] || exit 1
[[ ${DEPLOY_USER:-} =~ ^[a-z_][a-z0-9_-]*$ && $DEPLOY_USER != root ]] || exit 1
[[ ${DEPLOY_PORT:-} =~ ^[0-9]{1,5}$ ]] || exit 1
(( 10#$DEPLOY_PORT >= 1 && 10#$DEPLOY_PORT <= 65535 )) || exit 1

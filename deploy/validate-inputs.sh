#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/validate-ssh.sh"
[[ ${DEPLOY_ROOT:-} =~ ^/[a-zA-Z0-9_/-]+$ && $DEPLOY_ROOT != / && $DEPLOY_ROOT != */ ]] || exit 1
[[ ${DEPLOY_PLATFORM:-} == linux/amd64 ]] || { echo 'Only verified linux/amd64 is supported by this workflow'; exit 1; }
[[ ${RELEASE_SHA:-} =~ ^[a-f0-9]{40}$ ]] || exit 1
[[ ${DEPLOY_MODE:-} == prepare || ${DEPLOY_MODE:-} == deploy ]] || exit 1

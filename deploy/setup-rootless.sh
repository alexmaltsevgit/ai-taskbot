#!/usr/bin/env bash
# Install/start ONLY this unprivileged account's rootless Docker user service.
# Prerequisites are checked before invoking the already installed official tool.
set -euo pipefail
export LC_ALL=C

# Kept as a small probe so tests can simulate both permissions without touching
# the real host socket. Production has no environment bypass for this boundary.
rootful_socket_writable() {
  [[ -w /var/run/docker.sock ]]
}

rootless_setup() {
  local uid username runtime tool unit_state unit_command context_endpoint contexts info
  uid=$(id -u)
  username=$(id -un)
  [[ $uid != 0 ]] || { echo 'Rootless setup must not run as root'; return 1; }
  [[ ${HOME:-} == /* && -d $HOME && -w $HOME ]] || { echo 'A writable account home is required'; return 1; }
  [[ $(stat -Lc '%u' "$HOME") == "$uid" ]] || { echo 'Home ownership mismatch'; return 1; }
  runtime="/run/user/$uid"
  [[ $(stat -Lc '%u:%a:%F' "$runtime" 2>/dev/null) == "$uid:700:directory" ]] || { echo 'Private user runtime directory is missing or unsafe'; return 1; }
  export XDG_RUNTIME_DIR="$runtime"
  export DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime/bus"
  export DOCKER_HOST="unix://$runtime/docker.sock"
  # Avoid inherited context/TLS/config overriding our account-specific endpoint.
  unset DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH DOCKER_CONFIG XDG_CONFIG_HOME
  export PATH="$PATH:/usr/sbin:/sbin"
  local dependency
  for dependency in docker dockerd-rootless.sh rootlesskit newuidmap newgidmap slirp4netns iptables systemctl loginctl awk dpkg-query; do
    command -v "$dependency" >/dev/null 2>&1 || { echo "Missing prerequisite: $dependency; ask the administrator to install dependencies"; return 1; }
  done
  tool=/usr/bin/dockerd-rootless-setuptool.sh
  [[ $(stat -Lc '%u:%a:%F' "$tool" 2>/dev/null) == '0:755:regular file' ]] || { echo 'Expected root-owned official rootless setup tool is unavailable'; return 1; }
  dpkg-query -S "$tool" 2>/dev/null | grep -q '^docker-ce-rootless-extras: ' || { echo 'Setup tool is not owned by the expected installed Docker package'; return 1; }
  # Rootful Docker may be running for other services. We neither connect to it nor
  # stop it. An account with access to its socket is overprivileged for this setup.
  if rootful_socket_writable; then
    echo 'Account has rootful Docker socket access; remove that access before proceeding'
    return 1
  fi
  for dependency in /etc/subuid /etc/subgid; do
    awk -F: -v name="$username" -v uid="$uid" '($1==name || $1==uid) && $3>=65536 {found=1} END {exit !found}' "$dependency" || { echo 'At least 65536 subordinate user/group IDs are required'; return 1; }
  done
  systemctl --user show-environment >/dev/null 2>&1 || { echo 'User systemd/D-Bus session is unavailable'; return 1; }
  [[ $(loginctl show-user "$uid" --property=Linger --value 2>/dev/null) == yes ]] || { echo 'Administrator must enable lingering for this account before setup'; return 1; }
  unit_state=$(systemctl --user show docker.service --property=LoadState --value)
  if [[ $unit_state != not-found ]]; then
    unit_command=$(systemctl --user show docker.service --property=ExecStart --value)
    [[ $unit_command == '{ path=/usr/bin/dockerd-rootless.sh ;'* ]] || { echo 'Existing user docker.service is not the expected rootless service'; return 1; }
  fi
  contexts=$(docker context ls --format '{{.Name}}') || { echo 'Cannot inspect local Docker contexts'; return 1; }
  context_endpoint=''
  if grep -qx rootless <<< "$contexts"; then
    context_endpoint=$(docker context inspect rootless --format '{{.Endpoints.docker.Host}}' 2>/dev/null) || { echo 'Cannot inspect existing rootless context'; return 1; }
  fi
  [[ -z $context_endpoint || $context_endpoint == "$DOCKER_HOST" ]] || { echo 'Existing rootless context points elsewhere; refusing to replace it'; return 1; }
  if info=$(docker info --format '{{json .SecurityOptions}}' 2>/dev/null); then
    [[ $info == *'"name=rootless"'* ]] || { echo 'Account socket does not report a rootless engine'; return 1; }
    if [[ $context_endpoint == "$DOCKER_HOST" ]] && systemctl --user is-active --quiet docker.service && systemctl --user is-enabled --quiet docker.service; then
      echo 'Rootless Docker is already configured and running for this account.'
      return 0
    fi
  fi
  # The official check can report additional kernel/iptables prerequisites. Do
  # not execute its suggested root commands or print service/journal data.
  "$tool" check >/dev/null 2>&1 || { echo 'Official rootless prerequisite check failed; administrator review is required'; return 1; }
  rootlesskit true >/dev/null 2>&1 || { echo 'Rootless namespaces smoke test failed; no setup attempted'; return 1; }
  # No --force: it also bypasses a failed RootlessKit smoke test. A running but
  # inaccessible rootful daemon does not require --force in the official tool.
  "$tool" install >/dev/null 2>&1 || { echo 'Official user-service setup failed; inspect this account locally before retrying'; return 1; }
  [[ $(docker context inspect rootless --format '{{.Endpoints.docker.Host}}') == "$DOCKER_HOST" ]] || { echo 'Rootless context verification failed'; return 1; }
  systemctl --user is-active --quiet docker.service || { echo 'Rootless user service is not active'; return 1; }
  systemctl --user is-enabled --quiet docker.service || { echo 'Rootless user service is not enabled'; return 1; }
  docker info --format '{{json .SecurityOptions}}' | grep -q '"name=rootless"' || { echo 'Rootless engine verification failed'; return 1; }
  echo 'Rootless user service and account-specific Docker endpoint verified. No application deployed.'
}

if [[ -z ${BASH_SOURCE[0]:-} || ${BASH_SOURCE[0]:-} == "$0" ]]; then rootless_setup; fi

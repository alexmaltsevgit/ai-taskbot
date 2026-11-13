#!/usr/bin/env bash
# Read-only account prerequisites. Never run setup tools, sudo, Docker daemon
# operations, systemctl start/enable, or inspect runtime secrets/file contents.
set -euo pipefail
uid=$(id -u)
[[ $uid != 0 ]] || { echo 'Root diagnostics are forbidden'; exit 1; }
printf 'Account: '
id
printf 'Kernel and architecture: '
uname -srm
runtime=${XDG_RUNTIME_DIR:-/run/user/$uid}
# Ignore unexpected paths rather than probing arbitrary data from shell config.
if [[ $runtime == "/run/user/$uid" ]]; then
  printf 'User runtime directory: %s\n' "$runtime"
  if [[ -d $runtime ]]; then
    stat -c 'Runtime owner UID: %u; mode: %a' "$runtime"
  else
    echo 'Runtime directory is absent'
  fi
  if [[ -S $runtime/docker.sock ]]; then echo 'User Docker socket: present'; else echo 'User Docker socket: absent'; fi
else
  echo 'XDG_RUNTIME_DIR is nonstandard; manual review required'
fi
for tool in docker dockerd-rootless-setuptool.sh dockerd-rootless.sh rootlesskit newuidmap newgidmap slirp4netns systemctl; do
  if command -v "$tool" >/dev/null 2>&1; then printf '%s: available\n' "$tool"; else printf '%s: missing\n' "$tool"; fi
done
# These commands report local client/package versions without accessing engines.
if command -v docker >/dev/null 2>&1; then
  docker --version || true
  docker compose version --short 2>/dev/null || echo 'Compose client unavailable'
fi
if command -v rootlesskit >/dev/null 2>&1; then rootlesskit --version || true; fi
if command -v slirp4netns >/dev/null 2>&1; then slirp4netns --version || true; fi
if command -v dpkg-query >/dev/null 2>&1; then
  dpkg-query -W -f='${binary:Package}: ${Version}\n' uidmap docker-ce-rootless-extras slirp4netns 2>/dev/null || true
fi
if command -v systemctl >/dev/null 2>&1; then
  # Use show, not status: status may include journal output and application logs.
  systemctl --user show docker.service --property=LoadState,ActiveState,SubState --no-pager 2>/dev/null || echo 'User service manager is unavailable in this SSH session'
fi
if command -v loginctl >/dev/null 2>&1; then
  loginctl show-user "$uid" --property=Linger,State --no-pager 2>/dev/null || echo 'User session metadata unavailable'
fi
echo 'Read-only diagnostics completed; no rootless installation or service changes attempted.'

#!/usr/bin/env bash
# Isolated function mocks: no host systemd, namespace tools or daemon is called.
set -euo pipefail
cd "$(dirname "$0")/.."
bash -n deploy/setup-rootless.sh
source deploy/setup-rootless.sh
sandbox=$(mktemp -d /tmp/taskbotrootlesstestXXXXXXXX)
trap 'rm -rf "$sandbox"' EXIT
export HOME="$sandbox/home"
mkdir "$HOME"
trace="$sandbox/install-trace"
id() { if [[ $1 == -u ]]; then echo 1000; else echo taskbot; fi; }
stat() {
  case "${*: -1}" in
    "$HOME") echo "${TEST_HOME_OWNER:-1000}" ;;
    /run/user/1000) echo '1000:700:directory' ;;
    /usr/bin/dockerd-rootless-setuptool.sh) echo '0:755:regular file' ;;
    *) return 1 ;;
  esac
}
command() {
  if [[ $1 == -v && $2 == "${TEST_MISSING:-}" ]]; then return 1; fi
  builtin command "$@"
}
dockerd-rootless.sh() { return 98; }
newuidmap() { return 98; }
newgidmap() { return 98; }
slirp4netns() { return 98; }
iptables() { return 98; }
awk() { [[ ${TEST_SUBIDS_FAIL:-} != yes ]]; }
dpkg-query() { echo 'docker-ce-rootless-extras: /usr/bin/dockerd-rootless-setuptool.sh'; }
loginctl() { echo "${TEST_LINGER:-yes}"; }
systemctl() {
  case "$*" in
    '--user show-environment') return 0 ;;
    '--user show docker.service --property=LoadState --value')
      if [[ ${TEST_BAD_SERVICE:-} == yes || -f $trace ]]; then echo loaded; else echo not-found; fi ;;
    '--user show docker.service --property=ExecStart --value')
      if [[ ${TEST_BAD_SERVICE:-} == yes ]]; then echo '{ path=/bin/other ; }'; else echo '{ path=/usr/bin/dockerd-rootless.sh ; argv[]=/usr/bin/dockerd-rootless.sh ; }'; fi ;;
    '--user is-active --quiet docker.service'|'--user is-enabled --quiet docker.service') [[ -f $trace ]] ;;
    *) echo 'Unexpected service mutation' >&2; return 98 ;;
  esac
}
rootlesskit() { [[ $* == true && ${TEST_SMOKE_FAIL:-} != yes ]]; }
docker() {
  [[ ${DOCKER_HOST:-} == unix:///run/user/1000/docker.sock && -z ${DOCKER_CONTEXT:-} ]] || return 98
  case "$*" in
    'context ls --format {{.Name}}')
      if [[ ${TEST_BAD_CONTEXT:-} == yes || -f $trace ]]; then echo rootless; else echo default; fi ;;
    'context inspect rootless --format {{.Endpoints.docker.Host}}')
      if [[ ${TEST_BAD_CONTEXT:-} == yes ]]; then echo unix:///other/socket; elif [[ -f $trace ]]; then echo "$DOCKER_HOST"; else return 1; fi ;;
    'info --format {{json .SecurityOptions}}') [[ -f $trace ]] || return 1; echo '["name=rootless"]' ;;
    *) echo 'Unexpected daemon mutation' >&2; return 98 ;;
  esac
}
function /usr/bin/dockerd-rootless-setuptool.sh() {
  case "$*" in
    check) [[ ${TEST_CHECK_FAIL:-} != yes ]] ;;
    install) printf 'install\n' >> "$trace" ;;
    *) echo 'Unexpected installer flag or action' >&2; return 98 ;;
  esac
}
for scenario in TEST_MISSING:newuidmap TEST_SUBIDS_FAIL:yes TEST_LINGER:no TEST_BAD_SERVICE:yes TEST_BAD_CONTEXT:yes TEST_CHECK_FAIL:yes TEST_SMOKE_FAIL:yes TEST_HOME_OWNER:0; do
  variable=${scenario%:*};value=${scenario#*:}
  if (export "$variable=$value"; rootless_setup) >"$sandbox/result" 2>&1; then
    echo "Invalid prerequisite accepted: $variable"; exit 1
  fi
  test ! -f "$trace"
done
# Inherited rootful context cannot override the dedicated account endpoint.
(export DOCKER_CONTEXT=default DOCKER_HOST=unix:///var/run/docker.sock; rootless_setup)
test "$(wc -l < "$trace")" = 1
rootless_setup
test "$(wc -l < "$trace")" = 1
# Exercise the exact SSH transport shape: a Bash script consumed from stdin.
export trace
{
  declare -f id stat command dockerd-rootless.sh newuidmap newgidmap slirp4netns iptables awk dpkg-query loginctl systemctl rootlesskit docker /usr/bin/dockerd-rootless-setuptool.sh
  cat deploy/setup-rootless.sh
} | bash -s
test "$(wc -l < "$trace")" = 1
echo 'Rootless preflight, endpoint isolation, stdin transport and idempotence tests passed' 

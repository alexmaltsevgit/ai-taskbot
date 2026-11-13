#!/usr/bin/env bash
# No server, Docker engine, or credentials required. Validate the input boundary
# and shell syntax; actual Docker/SSH behavior requires the documented live test.
set -euo pipefail
cd "$(dirname "$0")/.."
bash deploy/test-rootless.sh
bash -n deploy/install-release.sh deploy/validate-inputs.sh deploy/validate-ssh.sh deploy/diagnose-server.sh
export DEPLOY_ENABLED=true DEPLOY_HOST=server.example DEPLOY_USER=taskbot
export DEPLOY_PORT=22222 DEPLOY_ROOT=/home/taskbot/ai-taskbot DEPLOY_PLATFORM=linux/amd64
export RELEASE_SHA=0123456789abcdef0123456789abcdef01234567 DEPLOY_MODE=prepare
bash deploy/validate-inputs.sh
# Diagnostics need only SSH configuration, not an image, release, or runtime env.
env -u DEPLOY_ROOT -u DEPLOY_PLATFORM -u RELEASE_SHA -u DEPLOY_MODE bash deploy/validate-ssh.sh
reject() {
  local variable=$1 value=$2
  if env "$variable=$value" bash deploy/validate-inputs.sh >/dev/null 2>&1; then
    echo "Unsafe value accepted for $variable" >&2
    exit 1
  fi
}
reject DEPLOY_ENABLED false
reject DEPLOY_USER root
reject DEPLOY_HOST 'server;echo pwned'
reject DEPLOY_PORT 0
reject DEPLOY_PORT 65536
reject DEPLOY_ROOT /tmp/../other
reject DEPLOY_ROOT /
reject DEPLOY_ROOT '/tmp/a b'
reject DEPLOY_PLATFORM linux/arm64
reject RELEASE_SHA HEAD
reject DEPLOY_MODE destroy
echo 'Deployment input and syntax tests passed'

# Exercise staging, initial deployment, rootful rejection and rollback with a
# fake Docker CLI. These tests never contact the host Docker socket.
sandbox=$(mktemp -d /tmp/taskbotdeploytestXXXXXXXX)
trap 'rm -rf "$sandbox"' EXIT
mkdir -p "$sandbox/bin" "$sandbox/app/shared/secrets" "$sandbox/app/releases/$RELEASE_SHA"
export DEPLOY_TEST_TRACE="$sandbox/trace"
cat > "$sandbox/bin/docker" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$DEPLOY_TEST_TRACE"
case "$1" in
 info)
  if [[ $* == *SecurityOptions* ]]; then
   if [[ ${TEST_ROOTFUL:-} == yes ]]; then echo '["name=seccomp"]'; else echo '["name=rootless"]'; fi
  else echo x86_64; fi ;;
 load) cat >/dev/null ;;
 ps) : ;;
 inspect) if [[ ${TEST_UNHEALTHY:-} == yes ]];then echo 'false 1';else echo 'true 0';fi ;;
 run) printf 'mock-backup' ;;
 compose)
  if [[ $* == *' ps -q '* ]]; then
    if [[ ${TEST_PS_FAIL:-} == yes ]]; then exit 17; fi
    echo container
  fi
  if [[ ${TEST_RESTORE_FAIL:-} == yes && $* == *"releases/$TEST_PREVIOUS/compose.yaml up "* ]]; then exit 18; fi ;;
 *) exit 1 ;;
esac
MOCK
cat > "$sandbox/bin/id" <<'MOCK'
#!/usr/bin/env bash
echo 1000
MOCK
cat > "$sandbox/bin/sleep" <<'MOCK'
#!/usr/bin/env bash
if [[ ${TEST_SIGNAL:-} == yes ]]; then kill -TERM "$PPID"; fi
exit 0
MOCK
cat > "$sandbox/bin/mkdir" <<'MOCK'
#!/usr/bin/env bash
if [[ ${TEST_BACKUP_DIR_FAIL:-} == yes && $* == */backups ]]; then exit 19; fi
exec /usr/bin/mkdir "$@"
MOCK
cat > "$sandbox/bin/mv" <<'MOCK'
#!/usr/bin/env bash
if [[ ${TEST_METADATA_FAIL:-} == yes && $* == */current ]]; then exit 20; fi
exec /usr/bin/mv "$@"
MOCK
chmod +x "$sandbox/bin/"*
export PATH="$sandbox/bin:$PATH"
export DEPLOY_ROOT="$sandbox/app"
printf 'not-real-secrets\n' > "$DEPLOY_ROOT/shared/.env"
printf '{}\n' > "$DEPLOY_ROOT/shared/config.json"
release="$DEPLOY_ROOT/releases/$RELEASE_SHA"
cp deploy/compose.yaml deploy/install-release.sh "$release/"
printf 'mock-images' | gzip > "$release/images.tar.gz"
(cd "$release" && sha256sum images.tar.gz compose.yaml install-release.sh > checksums.txt)
bash deploy/install-release.sh "$DEPLOY_ROOT" "$RELEASE_SHA" prepare
test ! -f "$DEPLOY_ROOT/current"
test "$(stat -c %a "$DEPLOY_ROOT/shared/data")" = 700
test "$(stat -c %a "$DEPLOY_ROOT/shared/codex-auth")" = 700
if TEST_ROOTFUL=yes bash deploy/install-release.sh "$DEPLOY_ROOT" "$RELEASE_SHA" prepare >/dev/null 2>&1; then echo 'Rootful engine accepted'; exit 1; fi
bash deploy/install-release.sh "$DEPLOY_ROOT" "$RELEASE_SHA" deploy
test "$(cat "$DEPLOY_ROOT/current")" = "$RELEASE_SHA"
previous=$RELEASE_SHA
export RELEASE_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
mkdir "$DEPLOY_ROOT/releases/$RELEASE_SHA"
cp "$release/"* "$DEPLOY_ROOT/releases/$RELEASE_SHA/"
if TEST_UNHEALTHY=yes bash deploy/install-release.sh "$DEPLOY_ROOT" "$RELEASE_SHA" deploy >/dev/null 2>&1; then echo 'Unhealthy deployment accepted'; exit 1; fi
test "$(cat "$DEPLOY_ROOT/current")" = "$previous"
grep -q "releases/$previous/compose.yaml up -d --no-build --pull never" "$DEPLOY_TEST_TRACE"
# Failures from ordinary shell commands must trigger exactly one rollback,
# preserve the error code, and leave the committed release marker unchanged.
for scenario in TEST_PS_FAIL:17 TEST_BACKUP_DIR_FAIL:19 TEST_METADATA_FAIL:20 TEST_SIGNAL:143; do
  variable=${scenario%:*}
  expected=${scenario#*:}
  : > "$DEPLOY_TEST_TRACE"
  status=0
  env "$variable=yes" bash deploy/install-release.sh "$DEPLOY_ROOT" "$RELEASE_SHA" deploy >"$sandbox/failure.log" 2>&1 || status=$?
  test "$status" = "$expected"
  test "$(cat "$DEPLOY_ROOT/current")" = "$previous"
  test "$(grep -c "releases/$previous/compose.yaml up -d --no-build --pull never" "$DEPLOY_TEST_TRACE")" = 1
  grep -q 'Previous images restored' "$sandbox/failure.log"
done
# Rollback failure is reported, but does not mask the original deployment error.
status=0
TEST_PS_FAIL=yes TEST_RESTORE_FAIL=yes TEST_PREVIOUS=$previous bash deploy/install-release.sh "$DEPLOY_ROOT" "$RELEASE_SHA" deploy >"$sandbox/failure.log" 2>&1 || status=$?
test "$status" = 17
grep -q 'rollback failed' "$sandbox/failure.log"
if grep -Eq 'prune|down|volume rm|system ' "$DEPLOY_TEST_TRACE"; then echo 'Unsafe cleanup command'; exit 1; fi
echo 'Mock staging, deployment, rootless boundary and rollback tests passed'

# Diagnostic probes must never invoke setup or service-changing commands.
for tool in dockerd-rootless-setuptool.sh dockerd-rootless.sh newuidmap newgidmap; do
  cat > "$sandbox/bin/$tool" <<'MOCK'
#!/usr/bin/env bash
printf 'FORBIDDEN_SETUP_INVOKED\n' >> "$DEPLOY_TEST_TRACE"
exit 98
MOCK
done
for tool in rootlesskit slirp4netns; do
  cat > "$sandbox/bin/$tool" <<'MOCK'
#!/usr/bin/env bash
[[ $* == --version ]] || exit 98
echo 'mock version'
MOCK
done
cat > "$sandbox/bin/systemctl" <<'MOCK'
#!/usr/bin/env bash
[[ $* == '--user show docker.service --property=LoadState,ActiveState,SubState --no-pager' ]] || exit 98
printf 'LoadState=not-found\nActiveState=inactive\nSubState=dead\n'
MOCK
cat > "$sandbox/bin/loginctl" <<'MOCK'
#!/usr/bin/env bash
[[ $* == 'show-user 1000 --property=Linger,State --no-pager' ]] || exit 98
printf 'Linger=no\nState=online\n'
MOCK
cat > "$sandbox/bin/dpkg-query" <<'MOCK'
#!/usr/bin/env bash
[[ $1 == -W ]] || exit 98
echo 'uidmap: mock-version'
MOCK
cat > "$sandbox/bin/uname" <<'MOCK'
#!/usr/bin/env bash
[[ $* == -srm ]] || exit 98
echo 'Linux test x86_64'
MOCK
chmod +x "$sandbox/bin/"*
bash deploy/diagnose-server.sh > "$sandbox/diagnostics.log"
grep -q 'Read-only diagnostics completed' "$sandbox/diagnostics.log"
if grep -q FORBIDDEN_SETUP_INVOKED "$DEPLOY_TEST_TRACE"; then echo 'Diagnostics executed setup'; exit 1; fi
test "$(cat "$DEPLOY_ROOT/current")" = "$previous"
echo 'Read-only diagnostic probe tests passed'

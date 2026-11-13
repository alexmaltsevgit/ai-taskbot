#!/usr/bin/env bash
# Integration test on an isolated CI runner; creates only unique disposable
# networks/containers. No real upstream credentials or production server needed.
set -euo pipefail
bot_image=${1:?bot image}
worker_image=${2:?worker image}
cd "$(dirname "$0")/.."
prefix="taskbot-probe-$$"
app="$prefix-app";outbound="$prefix-out"
files=$(mktemp -d)
cleanup(){ docker rm -f "$prefix-origin" "$prefix-upstream" "$prefix-egress" "$prefix-worker" >/dev/null 2>&1 || true;docker network rm "$app" "$outbound" >/dev/null 2>&1 || true;rm -rf "$files"; }
trap cleanup EXIT
chmod 755 "$files"
# Normalize the real production Compose, not an independent YAML approximation.
DEPLOY_ROOT="$files" RELEASE_SHA=synthetic TELEGRAM_BOT_TOKEN=synthetic WORKER_TOKEN=synthetic docker compose -f deploy/compose.yaml config --format json | python3 deploy/check-compose-egress.py
docker network create --internal "$app" >/dev/null
docker network create "$outbound" >/dev/null
common=(--detach --read-only --cap-drop ALL --security-opt no-new-privileges --tmpfs /tmp --user 0:0 --entrypoint node)
docker run "${common[@]}" --name "$prefix-origin" --network "$outbound" --sysctl net.ipv4.ip_unprivileged_port_start=0 "$worker_image" -e "$(cat deploy/probes/fake-origin.js)" >/dev/null
origin_ip=$(docker inspect --format "{{(index .NetworkSettings.Networks \"$outbound\").IPAddress}}" "$prefix-origin")
docker run "${common[@]}" --name "$prefix-upstream" --network "$outbound" -e ORIGIN_IP="$origin_ip" "$worker_image" -e "$(cat deploy/probes/fake-upstream.js)" >/dev/null
upstream_ip=$(docker inspect --format "{{(index .NetworkSettings.Networks \"$outbound\").IPAddress}}" "$prefix-upstream")
printf '{"url":"http://%s:18080","username":"synthetic","password":"not-a-real-secret"}\n' "$upstream_ip" > "$files/proxy.json"
chmod 600 "$files/proxy.json"
# Rootful CI maps container uid 0 to host root, unlike production rootless.
# Change ownership only of this synthetic disposable fixture.
sudo chown 0:0 "$files/proxy.json"
docker run -d --name "$prefix-egress" --network "$app" --network-alias egress --dns 127.0.0.1 --read-only --cap-drop ALL --security-opt no-new-privileges --user 0:0 --mount "type=bind,source=$files/proxy.json,target=/run/proxy/proxy.json,readonly" "$bot_image" egress-proxy >/dev/null
docker network connect "$outbound" "$prefix-egress"
docker run "${common[@]}" --name "$prefix-worker" --network "$app" --network-alias worker --dns 127.0.0.1 "$worker_image" -e 'require("http").createServer((q,r)=>r.end("ok")).listen(8090,"0.0.0.0")' >/dev/null
sleep 3
# Positive control: the fake origin and external DNS really are reachable from
# the outward network, so their failure inside app is not a dead-server result.
docker run --rm --network "$outbound" --entrypoint node "$worker_image" -e 'const dns=require("dns").promises;const r=new dns.Resolver({timeout:2000,tries:1});r.setServers([process.argv[1]]);r.resolve4("example.org").then(a=>{if(a[0]!=="127.0.0.9")process.exit(1)}).catch(()=>process.exit(1));' "$origin_ip"
probe(){
 docker run --rm --network "$app" --dns 127.0.0.1 --read-only --cap-drop ALL --security-opt no-new-privileges --user 0:0 --entrypoint node -e PROBE_ORIGIN="$origin_ip" -e PROBE_PORT=18081 -e PROBE_DIRECT_HOST="$origin_ip" -e PROBE_DIRECT_PORT=18081 -e PROBE_PLAINTEXT=1 -e PROBE_WORKER=1 "$worker_image" -e "$(cat deploy/probes/egress.js)" "$1"
}
probe working
docker stop "$prefix-egress" >/dev/null
probe blocked
echo 'Isolated Docker TCP/DNS/proxy/fail-closed integration passed.'

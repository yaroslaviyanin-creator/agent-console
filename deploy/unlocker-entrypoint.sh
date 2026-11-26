#!/usr/bin/env bash
set -euo pipefail

data_dir="$HOME/.local/share/agunlocker"
bridge_port=53130

tcp_ready() {
  timeout 1 bash -c 'exec 3<>"/dev/tcp/127.0.0.1/$1"' _ "$1" 2>/dev/null
}

local_port() {
  local port=53129
  if [[ -s "$data_dir/proxy_port" ]]; then
    read -r port < "$data_dir/proxy_port" || true
  fi
  [[ "$port" =~ ^[0-9]{1,5}$ ]] && (( 10#$port > 0 && 10#$port <= 65535 )) || return 1
  printf '%s\n' "$port"
}

if [[ ${1:-} == healthcheck ]]; then
  port=$(local_port) && tcp_ready "$port" && tcp_ready "$bridge_port"
  exit
fi

node - "$data_dir/settings.json" <<'JS'
const fs = require('node:fs');
const path = process.argv[2];
let settings = {};
try {
  settings = JSON.parse(fs.readFileSync(path, 'utf8').replace(/^\uFEFF/, ''));
} catch (error) {
  if (error.code !== 'ENOENT') throw new Error('cannot read existing unlocker settings');
}
settings.client_patch = true;
settings.local_proxy = true;
const code = (process.env.AG_UNLOCKER_CODE || '').trim();
if (code) {
  settings.group_key = code;
  settings.group_proxy = true;
  settings.group_key_at = Math.floor(Date.now() / 1000);
}
const temp = path + '.tmp-' + process.pid;
fs.writeFileSync(temp, JSON.stringify(settings) + '\n', { mode: 0o600, flag: 'wx' });
fs.renameSync(temp, path);
JS
unset AG_UNLOCKER_CODE

proxy_pid=
bridge_pid=
stop_children() {
  local pid
  for pid in "$proxy_pid" "$bridge_pid"; do
    if [[ -n "$pid" ]]; then kill "$pid" 2>/dev/null || true; fi
  done
  for pid in "$proxy_pid" "$bridge_pid"; do
    if [[ -n "$pid" ]]; then wait "$pid" 2>/dev/null || true; fi
  done
}
trap 'stop_children; exit 0' TERM INT

while true; do
  rm -f "$data_dir/proxy_port"
  ag_unlocker --proxy &
  proxy_pid=$!
  for (( attempt=0; attempt<40; attempt++ )); do
    kill -0 "$proxy_pid" 2>/dev/null || break
    if port=$(local_port) && tcp_ready "$port"; then
      # The upstream binary only listens on loopback. Forward its TCP stream
      # to the Compose network without publishing a port on the host.
      socat "TCP4-LISTEN:$bridge_port,reuseaddr,fork" "TCP4:127.0.0.1:$port" &
      bridge_pid=$!
      wait -n "$proxy_pid" "$bridge_pid" || true
      break
    fi
    sleep 0.25
  done
  stop_children
  proxy_pid=
  bridge_pid=
  echo 'unlocker proxy or network listener stopped; retrying in 2s' >&2
  sleep 2
done

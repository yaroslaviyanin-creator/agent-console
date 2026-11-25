#!/usr/bin/env bash
set -euo pipefail

AG_UNLOCKER_BIN="${AG_UNLOCKER_BIN:-/usr/local/bin/ag_unlocker}"
AG_DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/agunlocker"
AGY_PATH="${ANTIGRAVITY_BIN:-/home/app/.local/bin/agy}"

case "${1:-}" in
  create-user|set-role|-h|--help)
    exec /app/agent-server "$@"
    ;;
esac

is_unlocker_enabled() {
  local raw="${AG_UNLOCKER_ENABLED:-}"
  if [ -z "$raw" ]; then
    [ "${PROVIDER:-}" = "antigravity" ]
    return
  fi
  case "$(printf '%s' "$raw" | tr '[:upper:]' '[:lower:]')" in
    1|true|yes|on) return 0 ;;
    *) return 1 ;;
  esac
}

is_antigravity_provider() {
  [ "${PROVIDER:-}" = "antigravity" ]
}

resolve_agy_bin() {
  if [ -f "$AGY_PATH" ]; then
    readlink -f "$AGY_PATH" 2>/dev/null || printf '%s' "$AGY_PATH"
    return 0
  fi
  if command -v "$AGY_PATH" >/dev/null 2>&1; then
    local found
    found="$(command -v "$AGY_PATH")"
    readlink -f "$found" 2>/dev/null || printf '%s' "$found"
    return 0
  fi
  return 1
}

patch_agy_binary() {
  local strict="$1"
  local target
  if ! target="$(resolve_agy_bin)"; then
    if [ "$strict" = "1" ]; then
      echo "[entrypoint] ERROR: agy binary not found ($AGY_PATH)" >&2
      return 1
    fi
    echo "[entrypoint] WARN: agy binary not found ($AGY_PATH), skipping patch" >&2
    return 0
  fi
  if [ ! -w "$target" ]; then
    if [ "$strict" = "1" ]; then
      echo "[entrypoint] ERROR: agy binary is not writable ($target)" >&2
      return 1
    fi
    echo "[entrypoint] WARN: agy binary is not writable ($target), skipping patch" >&2
    return 0
  fi

  node - "$target" "$strict" <<'EOF'
const fs = require('fs');
const path = require('path');

const target = process.argv[2];
const strict = process.argv[3] === '1';
const buf = fs.readFileSync(target);

// Only patch ELF binaries, not shell wrappers.
if (buf.length < 4 || buf[0] !== 0x7f || buf[1] !== 0x45 || buf[2] !== 0x4c || buf[3] !== 0x46) {
  console.error(`[entrypoint] ${strict ? 'ERROR' : 'skip patch'}: ${target} is not an ELF binary`);
  process.exit(strict ? 1 : 0);
}

function replaceAllInPlace(buffer, fromStr, toStr) {
  const from = Buffer.from(fromStr, 'utf8');
  const to = Buffer.from(toStr, 'utf8');
  if (from.length !== to.length) {
    throw new Error(`Length mismatch: ${fromStr} vs ${toStr}`);
  }
  let count = 0;
  let idx = 0;
  while ((idx = buffer.indexOf(from, idx)) !== -1) {
    to.copy(buffer, idx);
    count++;
    idx += to.length;
  }
  return count;
}

const r1 = replaceAllInPlace(buf, 'ineligible', 'inexigible');
const r2 = replaceAllInPlace(buf, 'https_proxy', 'AG_LS_PROXY');

if (r1 === 0 && r2 === 0) {
  const hasPatched = buf.includes(Buffer.from('inexigible')) || buf.includes(Buffer.from('AG_LS_PROXY'));
  if (!hasPatched) {
    console.error(`[entrypoint] ${strict ? 'ERROR' : 'WARN'}: agy patch signatures not found in ${target}`);
    process.exit(strict ? 1 : 0);
  }
  console.error(`[entrypoint] agy already patched: ${target}`);
  process.exit(0);
}

const tmp = path.join(path.dirname(target), `.agy.patch.${process.pid}.tmp`);
fs.writeFileSync(tmp, buf, { mode: 0o755 });
fs.chmodSync(tmp, 0o755);
fs.renameSync(tmp, target);
console.error(`[entrypoint] agy patched (${target}): ineligible->inexigible=${r1}, https_proxy->AG_LS_PROXY=${r2}`);
EOF
}

configure_unlocker_settings() {
  mkdir -p "$AG_DATA_DIR"
  local settings_file="$AG_DATA_DIR/settings.json"
  local code="${AG_UNLOCKER_CODE:-}"

  node - "$settings_file" "$code" <<'EOF'
const fs = require('fs');
const path = require('path');

const settingsPath = process.argv[2];
const code = (process.argv[3] || '').trim();

let cfg = {};
if (fs.existsSync(settingsPath)) {
  try {
    const raw = fs.readFileSync(settingsPath, 'utf8').replace(/^\uFEFF/, '');
    cfg = JSON.parse(raw);
  } catch (err) {
    console.error(`[entrypoint] WARN: failed to parse ${settingsPath}, resetting: ${err.message}`);
    cfg = {};
  }
}

cfg.client_patch = true;
cfg.local_proxy = true;
if (code) {
  cfg.group_key = code;
  cfg.group_proxy = true;
  cfg.group_key_at = Math.floor(Date.now() / 1000);
}

const tmp = path.join(path.dirname(settingsPath), `.settings.${process.pid}.tmp`);
fs.writeFileSync(tmp, JSON.stringify(cfg, null, 2) + '\n', { mode: 0o600 });
fs.renameSync(tmp, settingsPath);
EOF
}

wait_for_proxy_port() {
  local port="$1"
  node - "$port" <<'EOF'
const net = require('net');
const port = Number(process.argv[2]);
const s = net.connect({ host: '127.0.0.1', port });
s.setTimeout(300);
s.on('connect', () => { s.destroy(); process.exit(0); });
s.on('timeout', () => { s.destroy(); process.exit(1); });
s.on('error', () => { process.exit(1); });
EOF
}

UNLOCKER_SUPERVISOR_PID=""

run_unlocker_supervisor() {
  local child_pid=""
  local stopping=0
  stop_child() {
    local sig="$1"
    stopping=1
    if [ -n "$child_pid" ] && kill -0 "$child_pid" 2>/dev/null; then
      kill "-$sig" "$child_pid" 2>/dev/null || true
    fi
  }
  trap 'stop_child TERM' TERM
  trap 'stop_child INT' INT

  while [ "$stopping" -eq 0 ]; do
    "$AG_UNLOCKER_BIN" --proxy >&2 &
    child_pid=$!
    local rc=0
    while true; do
      local wait_rc=0
      wait "$child_pid" || wait_rc=$?
      if [ "$stopping" -eq 1 ] && kill -0 "$child_pid" 2>/dev/null; then
        continue
      fi
      rc=$wait_rc
      break
    done
    child_pid=""
    if [ "$stopping" -eq 1 ]; then
      break
    fi
    echo "[entrypoint] WARN: ag_unlocker --proxy exited (code=$rc), restarting in 2s..." >&2
    sleep 2
  done
}

start_unlocker_proxy() {
  local strict="$1"
  if [ ! -x "$AG_UNLOCKER_BIN" ]; then
    echo "[entrypoint] ERROR: ag_unlocker binary not found at $AG_UNLOCKER_BIN" >&2
    return 1
  fi

  configure_unlocker_settings
  patch_agy_binary "$strict"

  # Remove stale proxy_port file before launch so we observe the fresh port.
  rm -f "$AG_DATA_DIR/proxy_port"

  run_unlocker_supervisor &
  UNLOCKER_SUPERVISOR_PID=$!

  local port="53129"
  local ready=false
  local i
  for i in $(seq 1 40); do
    if ! kill -0 "$UNLOCKER_SUPERVISOR_PID" 2>/dev/null; then
      echo "[entrypoint] ERROR: ag_unlocker supervisor exited unexpectedly" >&2
      UNLOCKER_SUPERVISOR_PID=""
      return 1
    fi
    if [ -f "$AG_DATA_DIR/proxy_port" ]; then
      local file_port
      file_port="$(tr -d '[:space:]' < "$AG_DATA_DIR/proxy_port" || true)"
      if [ -n "$file_port" ]; then
        port="$file_port"
      fi
    fi
    if wait_for_proxy_port "$port"; then
      ready=true
      break
    fi
    sleep 0.25
  done

  if [ "$ready" != "true" ]; then
    echo "[entrypoint] ERROR: ag_unlocker proxy did not listen on 127.0.0.1:$port within 10s" >&2
    kill -TERM "$UNLOCKER_SUPERVISOR_PID" 2>/dev/null || true
    wait "$UNLOCKER_SUPERVISOR_PID" 2>/dev/null || true
    UNLOCKER_SUPERVISOR_PID=""
    return 1
  fi

  export AG_LS_PROXY="http://127.0.0.1:${port}"
  echo "[entrypoint] ag_unlocker proxy listening on $AG_LS_PROXY (supervisor_pid=$UNLOCKER_SUPERVISOR_PID)" >&2
}

if is_unlocker_enabled; then
  if is_antigravity_provider; then
    if ! start_unlocker_proxy 1; then
      echo "[entrypoint] FATAL: ag_unlocker is required for PROVIDER=antigravity when AG_UNLOCKER_ENABLED=true" >&2
      exit 1
    fi
  else
    start_unlocker_proxy 0 || echo "[entrypoint] WARN: continuing without active ag_unlocker proxy (PROVIDER=${PROVIDER:-})" >&2
  fi
fi

if [ -z "$UNLOCKER_SUPERVISOR_PID" ]; then
  exec /app/agent-server "$@"
fi

server_pid=""
shutting_down=0
on_signal() {
  local sig="$1"
  shutting_down=1
  if [ -n "$server_pid" ] && kill -0 "$server_pid" 2>/dev/null; then
    kill "-$sig" "$server_pid" 2>/dev/null || true
  fi
  if [ -n "$UNLOCKER_SUPERVISOR_PID" ] && kill -0 "$UNLOCKER_SUPERVISOR_PID" 2>/dev/null; then
    kill "-$sig" "$UNLOCKER_SUPERVISOR_PID" 2>/dev/null || true
  fi
}
trap 'on_signal TERM' TERM
trap 'on_signal INT' INT

/app/agent-server "$@" &
server_pid=$!

server_status=0
while true; do
  wait_rc=0
  wait "$server_pid" || wait_rc=$?
  if [ "$shutting_down" -eq 1 ] && kill -0 "$server_pid" 2>/dev/null; then
    continue
  fi
  server_status=$wait_rc
  break
done

if [ -n "$UNLOCKER_SUPERVISOR_PID" ] && kill -0 "$UNLOCKER_SUPERVISOR_PID" 2>/dev/null; then
  kill -TERM "$UNLOCKER_SUPERVISOR_PID" 2>/dev/null || true
  wait "$UNLOCKER_SUPERVISOR_PID" 2>/dev/null || true
fi

exit "$server_status"

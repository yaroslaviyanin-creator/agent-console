const fs = require('fs');
const path = require('path');

const target = process.argv[2];
const buf = fs.readFileSync(target);

// Only patch ELF binaries, not shell wrappers.
if (buf.length < 4 || buf[0] !== 0x7f || buf[1] !== 0x45 || buf[2] !== 0x4c || buf[3] !== 0x46) {
  console.error(`[runner build] ERROR: ${target} is not an ELF binary`);
  process.exit(1);
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
    console.error(`[runner build] ERROR: agy patch signatures not found in ${target}`);
    process.exit(1);
  }
  console.error(`[runner build] agy already patched: ${target}`);
  process.exit(0);
}

const tmp = path.join(path.dirname(target), `.agy.patch.${process.pid}.tmp`);
fs.writeFileSync(tmp, buf, { mode: 0o755 });
fs.chmodSync(tmp, 0o755);
fs.renameSync(tmp, target);
console.error(`[runner build] agy patched (${target}): ineligible->inexigible=${r1}, https_proxy->AG_LS_PROXY=${r2}`);

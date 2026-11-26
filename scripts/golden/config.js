'use strict';
// config.json: what multichat writes back after loading a given file and then
// adding or removing sources from the panel. Runs server.js's own load block,
// detectPlatform, addSource, removeSource and saveConfig in a sandbox, with the
// real lib/targets.js (reply pins) and lib/infoapi.js (preset cleaning) — the
// two things that make the saved file differ from a plain pass-through.
const vm = require('vm');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { load, show, functionSource } = require('./source');
const { read, list, writeGolden } = require('./write');

const { createReplyTargets } = load('lib/targets.js');
const { cleanPresets } = load('lib/infoapi.js');

// The config load block: from `let config = {` through the first-run catch.
function loadBlock() {
  const text = show('server.js');
  const a = text.indexOf('let config = {');
  const marker = '} catch (e) { /* first run */ }';
  const b = text.indexOf(marker, a);
  if (a < 0 || b < 0) throw new Error('config load block not found in server.js');
  return text.slice(a, b + marker.length).replace(/^let config/, 'config');
}

const code = [
  loadBlock(),
  functionSource('server.js', 'detectPlatform'),
  functionSource('server.js', 'saveConfig'),
  functionSource('server.js', 'addSource'),
  functionSource('server.js', 'removeSource'),
  functionSource('server.js', 'startSources'),
].join('\n\n');

function runCase(spec) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'golden-config-'));
  const CONFIG_PATH = path.join(dir, 'config.json');
  if (spec.file !== null) fs.writeFileSync(CONFIG_PATH, spec.file);
  let n = 0;
  const sources = new Map();
  const ctx = {
    fs, CONFIG_PATH, URL, console: { error() {} },
    config: undefined, sources, history: [],
    cleanPresets,
    crypto: { randomBytes: k => Buffer.from(String(++n).padStart(k * 2, '0'), 'hex') },
    startAdapter() {}, broadcastStatus() {}, broadcast() {},
    replyTargets: createReplyTargets({ listSources: () => [...sources.values()] }),
  };
  vm.createContext(ctx);
  vm.runInContext(code, ctx);
  vm.runInContext('startSources()', ctx);
  const steps = [];
  const snapshot = op => steps.push({ op, saved: fs.existsSync(CONFIG_PATH) ? fs.readFileSync(CONFIG_PATH, 'utf8') : null });
  for (const op of spec.ops) {
    if (op.add !== undefined) vm.runInContext(`addSource(${JSON.stringify(op.add)})`, ctx);
    else if (op.remove !== undefined) vm.runInContext(`removeSource([...sources.keys()][${op.remove}])`, ctx);
    else throw new Error('unknown op ' + JSON.stringify(op));
    snapshot(op);
  }
  fs.rmSync(dir, { recursive: true, force: true });
  return { file: spec.file, ids: [...sources.keys()], steps };
}

for (const f of list('config/cases', '.json')) {
  writeGolden('config/golden/' + f, runCase(JSON.parse(read('config/cases/' + f))));
}

'use strict';
// Loads multichat's own modules from a pinned commit, straight out of git, so
// the goldens come from the code that runs on stream — not from a re-reading
// of it, and not from whatever the checkout's working tree happens to hold.
const { execFileSync } = require('child_process');
const Module = require('module');
const path = require('path');
const os = require('os');

const COMMIT = '29a9d2c';
const SRC = process.env.MULTICHAT_SRC || path.join(os.homedir(), 'Code', 'megarohas_multichat');

function show(rel) {
  try {
    return execFileSync('git', ['-C', SRC, 'show', `${COMMIT}:${rel}`], { encoding: 'utf8' });
  } catch (e) {
    console.error(`Can't read ${rel} at ${COMMIT} from ${SRC}. Set MULTICHAT_SRC to a multichat clone that has that commit.`);
    process.exit(1);
  }
}

const cache = new Map();

// load('lib/twitch.js', ['parseLine', ...]) -> module.exports, plus
// __internals holding the named top-level functions the module doesn't export.
function load(rel, internals = []) {
  const key = rel + '|' + internals.join(',');
  if (cache.has(key)) return cache.get(key);
  let text = show(rel);
  if (internals.length) text += `\n;module.exports.__internals = { ${internals.join(', ')} };\n`;
  const filename = path.join(SRC, rel);
  const m = new Module(filename, module);
  m.filename = filename;
  m.paths = Module._nodeModulePaths(path.dirname(filename));
  // relative requires come from the same commit too
  m.require = id => {
    if (!id.startsWith('.')) return require(id);
    let r = path.posix.join(path.posix.dirname(rel), id);
    if (!r.endsWith('.js') && !r.endsWith('.json')) r += '.js';
    return r.endsWith('.json') ? JSON.parse(show(r)) : load(r);
  };
  m._compile(text, filename);
  cache.set(key, m.exports);
  return m.exports;
}

// The body of a top-level `function name(...) { ... }` in a file, by brace
// matching (strings and comments in these functions don't contain braces).
function functionSource(rel, name) {
  const text = show(rel);
  const start = text.indexOf(`function ${name}(`);
  if (start < 0) throw new Error(`${name} not found in ${rel}`);
  let depth = 0;
  for (let i = text.indexOf('{', start); i < text.length; i++) {
    if (text[i] === '{') depth++;
    else if (text[i] === '}' && --depth === 0) return text.slice(start, i + 1);
  }
  throw new Error(`unbalanced ${name} in ${rel}`);
}

// Deterministic Math.random for ids the source makes up.
function seedRandom(seed = 1) {
  let a = seed >>> 0;
  Math.random = () => {
    a = (a + 0x6D2B79F5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

module.exports = { COMMIT, SRC, show, load, functionSource, seedRandom };

'use strict';
// A sign-in on disk being refreshed: the bytes multichat's TokenSession
// leaves in secrets.json after each step of a case, the refresh request it
// sent, and what tokens() returned. Runs the real lib/oauth-session.js and
// lib/tokens.js against a local token endpoint that gives each step's answer.
const fs = require('fs');
const http = require('http');
const os = require('os');
const path = require('path');
const { load } = require('./source');
const { read, list, writeGolden } = require('./write');

const { SecretStore } = load('lib/tokens.js');
const { TokenSession, SignedOut } = load('lib/oauth-session.js');

// "$field" in a case's params is that field of the stored tokens.
const params = (spec, t) => Object.fromEntries(spec.map(([k, v]) => [k, v.startsWith('$') ? t[v.slice(1)] : v]));
// keep: the listed fields that are set, or null when none are.
const keeper = fields => t => {
  const o = {};
  for (const f of fields || []) if (t[f]) o[f] = t[f];
  return Object.keys(o).length ? o : null;
};

async function run(spec, base, answer, requests) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'golden-session-'));
  const file = path.join(dir, 'secrets.json');
  if (spec.file !== null) fs.writeFileSync(file, spec.file, { mode: 0o600 });
  const store = new SecretStore(file);
  const steps = [];
  for (const step of spec.steps) {
    answer.status = step.status;
    answer.body = step.answer;
    requests.length = 0;
    const session = new TokenSession({
      store, section: step.section, now: () => spec.now, expiredMessage: 'expired',
      keep: keeper(step.keep),
      refreshRequest: t => ({ url: base + '/token', params: params(step.params, t) }),
    });
    let result;
    try {
      result = JSON.stringify(await session.tokens(!!step.force));
    } catch (e) {
      result = e instanceof SignedOut ? 'SignedOut' : 'Error: ' + e.message;
    }
    steps.push({
      request: requests.length ? requests.join('\n') : null,
      result,
      file: fs.existsSync(file) ? fs.readFileSync(file, 'utf8') : null,
    });
  }
  fs.rmSync(dir, { recursive: true, force: true });
  return { steps };
}

const answer = {};
const requests = [];
const srv = http.createServer((req, res) => {
  let b = '';
  req.on('data', c => { b += c; });
  req.on('end', () => {
    requests.push(b);
    res.writeHead(answer.status, { 'content-type': 'application/json' });
    res.end(answer.body);
  });
});
srv.listen(0, '127.0.0.1', async () => {
  const base = 'http://127.0.0.1:' + srv.address().port;
  for (const name of list('session/cases', '.json')) {
    writeGolden(`session/golden/${name}`, await run(JSON.parse(read(`session/cases/${name}`)), base, answer, requests));
  }
  srv.close();
});

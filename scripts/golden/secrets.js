'use strict';
// secrets.json: the exact bytes multichat's SecretStore leaves after a run of
// set() calls on a given starting file, and what get() then returns. Runs the
// real lib/tokens.js.
const fs = require('fs');
const os = require('os');
const path = require('path');
const { load } = require('./source');
const { read, list, writeGolden } = require('./write');

const { SecretStore } = load('lib/tokens.js');

for (const name of list('secrets/cases', '.json')) {
  const spec = JSON.parse(read(`secrets/cases/${name}`));
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'golden-secrets-'));
  const file = path.join(dir, 'secrets.json');
  if (spec.file !== null) fs.writeFileSync(file, spec.file);
  const s = new SecretStore(file);
  for (const [section, value] of spec.steps) s.set(section, value);
  const gets = {};
  for (const g of spec.gets) gets[g] = JSON.stringify(s.get(g));
  writeGolden(`secrets/golden/${name}`, {
    file: fs.existsSync(file) ? fs.readFileSync(file, 'utf8') : null,
    gets,
  });
  fs.rmSync(dir, { recursive: true, force: true });
}

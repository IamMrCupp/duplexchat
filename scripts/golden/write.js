'use strict';
const fs = require('fs');
const path = require('path');

const ROOT = path.resolve(__dirname, '..', '..');
const TESTDATA = path.join(ROOT, 'testdata');

function read(rel) { return fs.readFileSync(path.join(TESTDATA, rel), 'utf8'); }
function list(rel, ext) {
  return fs.readdirSync(path.join(TESTDATA, rel)).filter(f => f.endsWith(ext)).sort();
}
function writeGolden(rel, value) {
  const file = path.join(TESTDATA, rel);
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, JSON.stringify(value, null, 2) + '\n');
}

module.exports = { TESTDATA, read, list, writeGolden };

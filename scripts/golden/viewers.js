'use strict';
// The viewer bar's YouTube parser: what multichat's parseYouTubeViewers
// returns (or that it throws) for each input. The inputs are edge cases of
// JavaScript's own semantics the parser leans on: \b and /i without the u
// flag, \s, Number(), Math.round, and shapes where a property access throws.
const { load } = require('./source');
const { read, writeGolden } = require('./write');

const { parseYouTubeViewers } = load('lib/viewers.js');

const inputs = JSON.parse(read('viewers/cases/parse.json'));
writeGolden('viewers/golden/parse.json', inputs.map(i => {
  try {
    return { out: parseYouTubeViewers(i) };
  } catch (e) {
    return { throws: e.constructor.name };
  }
}));

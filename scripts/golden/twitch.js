'use strict';
// Twitch: IRC parsing, message building and moderation, through lib/twitch.js's
// own TwitchSource with a fake socket; channelFromUrl; emote list loading
// with a fake fetch.
const { load, seedRandom } = require('./source');
const { read, list, writeGolden } = require('./write');

const tw = load('lib/twitch.js', ['parseLine', 'unescapeTag', 'rolesFromBadges', 'buildParts']);
const { TwitchSource, channelFromUrl } = tw;
const { parseLine, unescapeTag, rolesFromBadges } = tw.__internals;

// testdata/twitch/ext-emotes.json: the third-party map a loaded reader holds.
const extEmotes = new Map(Object.entries(JSON.parse(read('twitch/ext-emotes.json'))));

function linesOf(file) {
  return read(file).split('\n').filter(l => l && !l.startsWith('#'));
}

// One TwitchSource per input file; every handler call and socket write recorded.
async function runIrc() {
  const allLines = [];
  for (const f of list('twitch/irc', '.irc')) {
    seedRandom(1);
    const out = [];
    let calls;
    const rec = name => (...args) => calls.push({ call: name, args });
    const src = new TwitchSource('ExampleChannel', {
      onMessage: rec('onMessage'), onRemove: rec('onRemove'), onRemoveAuthor: rec('onRemoveAuthor'),
      onClear: rec('onClear'), onStatus: rec('onStatus'),
    });
    src.sock = { write: s => calls.push({ call: 'write', args: [s] }), destroy: () => calls.push({ call: 'destroy', args: [] }) };
    src.extLoaded = true;           // ROOMSTATE must not fetch
    src.extEmotes = /-noext\.irc$/.test(f) ? null : extEmotes;
    for (const line of linesOf('twitch/irc/' + f)) {
      calls = [];
      try { src._handle(line); } catch (e) { calls.push({ call: 'throw', args: [String(e.message)] }); }
      out.push({ line, calls });
      allLines.push(line);
    }
    writeGolden('twitch/golden/irc/' + f.replace(/\.irc$/, '.json'), out);
  }
  writeGolden('twitch/golden/parse.json', allLines.map(line => ({ line, parsed: parseLine(line) })));
}

function runSmall() {
  const urls = JSON.parse(read('twitch/channel-urls.json'));
  writeGolden('twitch/golden/channel-urls.json', urls.map(url => ({ url, channel: channelFromUrl(url) })));
  const escapes = JSON.parse(read('twitch/tag-escapes.json'));
  writeGolden('twitch/golden/tag-escapes.json', escapes.map(raw => ({ raw, value: unescapeTag(raw) })));
  const badges = JSON.parse(read('twitch/badges.json'));
  writeGolden('twitch/golden/badges.json', badges.map(tag => ({ tag, roles: rolesFromBadges(tag) })));
}

// Emote lists: the source's loaders against canned API responses. The map is
// written sorted by name — within one service the source's order is the
// response order, and across services it's a race (duplexchat fixes that
// order on purpose), so only membership and URLs are pinned here.
async function runEmotes() {
  const api = JSON.parse(read('twitch/emote-api.json'));
  global.fetch = async url => {
    const body = api[url];
    if (body === undefined) return { ok: false, status: 404, json: async () => ({}) };
    return { ok: true, status: 200, json: async () => body };
  };
  const src = new TwitchSource('examplechannel', { onStatus() {} });
  src.extLoaded = false;
  src._loadExtEmotes('12345');
  for (let i = 0; i < 50 && !src.extEmotes; i++) await new Promise(r => setTimeout(r, 10));
  if (!src.extEmotes) throw new Error('emotes never loaded');
  writeGolden('twitch/golden/emote-map.json', [...src.extEmotes].sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0)));
}

(async () => {
  await runIrc();
  runSmall();
  await runEmotes();
})().catch(e => { console.error(e); process.exit(1); });

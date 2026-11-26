'use strict';
// YouTube: lib/youtube.js's own YouTubeSource, driven through resolve → init →
// poll against a fake fetch, plus its pure helpers. Every request it makes,
// every handler call and every retry it schedules is recorded in order.
const { load, seedRandom } = require('./source');
const { read, list, writeGolden } = require('./write');

const yt = load('lib/youtube.js', [
  'directVideoId', 'intColor', 'thumbUrl', 'runsToParts', 'runsToText', 'simpleOrRuns',
  'authorFrom', 'contData', 'extractJson',
]);
const { YouTubeSource, ownerChannelId } = yt;
const I = yt.__internals;

const settle = async () => { for (let i = 0; i < 20; i++) await new Promise(r => setImmediate(r)); };

function recorder(events) {
  const rec = name => (...args) => events.push({ event: 'call', call: name, args });
  return {
    onMessage: rec('onMessage'), onRemove: rec('onRemove'), onRemoveAuthor: rec('onRemoveAuthor'),
    onClear: rec('onClear'), onStatus: rec('onStatus'), onMeta: rec('onMeta'),
  };
}

// routes: { "GET <url>": response | [responses...] } — a list is consumed in order.
// response: { status, headers?, body? (JSON value), raw? (text as-is), bodyFile? (testdata path), throw? (network error) }
function fakeFetch(routes, events) {
  const used = {};
  return async (url, opts = {}) => {
    const method = (opts.method || 'GET').toUpperCase();
    const key = `${method} ${url}`;
    events.push({ event: 'fetch', method, url, headers: opts.headers || {}, body: opts.body ? JSON.parse(opts.body) : null });
    let resp = routes[key];
    if (Array.isArray(resp)) { const i = used[key] || 0; used[key] = i + 1; resp = resp[Math.min(i, resp.length - 1)]; }
    if (!resp) throw new Error('no route for ' + key);
    if (resp.throw) throw new Error(resp.throw);
    const text = resp.bodyFile ? read(resp.bodyFile) : resp.raw !== undefined ? resp.raw : JSON.stringify(resp.body === undefined ? {} : resp.body);
    const headers = Object.fromEntries(Object.entries(resp.headers || {}).map(([k, v]) => [k.toLowerCase(), String(v)]));
    return {
      ok: resp.status >= 200 && resp.status < 300,
      status: resp.status,
      headers: { get: k => (k.toLowerCase() in headers ? headers[k.toLowerCase()] : null) },
      text: async () => text,
      json: async () => JSON.parse(text),
    };
  };
}

async function runFlows() {
  for (const f of list('youtube/flows', '.json')) {
    const spec = JSON.parse(read('youtube/flows/' + f));
    seedRandom(1);
    const events = [];
    global.fetch = fakeFetch(spec.routes || {}, events);
    const src = new YouTubeSource(spec.url, recorder(events));
    // retries are recorded, not run; the scenario says which step to run next
    src._later = (fn, ms) => events.push({ event: 'later', ms, next: /_(\w+)\(\)/.exec(fn.toString())[1] });
    Object.assign(src, spec.state || {});
    for (const step of spec.steps) {
      events.push({ event: 'step', step });
      if (step === 'resolve') src._resolve();
      else if (step === 'initChat') src._initChat();
      else if (step === 'poll') src._poll();
      else throw new Error('unknown step ' + step);
      await settle();
    }
    const state = {};
    for (const k of ['videoId', 'ownerChannelId', 'apiKey', 'clientVersion', 'continuation', 'errStreak']) state[k] = src[k];
    writeGolden('youtube/golden/flows/' + f, { events, state });
  }
}

function runActions() {
  for (const f of list('youtube/actions', '.json')) {
    seedRandom(1);
    const events = [];
    const src = new YouTubeSource('https://www.youtube.com/watch?v=AAAAAAAAAAA', recorder(events));
    for (const batch of JSON.parse(read('youtube/actions/' + f))) {
      src._handleActions(batch);
      events.push({ event: 'batch-end' });
    }
    writeGolden('youtube/golden/actions/' + f, events);
  }
}

function runHelpers() {
  const h = JSON.parse(read('youtube/helpers.json'));
  writeGolden('youtube/golden/helpers.json', {
    directVideoId: h.urls.map(url => ({ url, id: I.directVideoId(url) })),
    intColor: h.colors.map(n => ({ n, color: I.intColor(n) })),
    thumbUrl: h.thumbs.map(obj => ({ obj, url: I.thumbUrl(obj) })),
    runs: h.runs.map(runs => ({ runs, parts: I.runsToParts(runs), text: I.runsToText(runs) })),
    simpleOrRuns: h.nodes.map(node => ({ node, text: I.simpleOrRuns(node) })),
    authorFrom: h.authors.map(r => ({ r, author: I.authorFrom(r) })),
    contData: h.continuations.map(conts => ({ conts, data: I.contData(conts) })),
  });
  const pages = list('youtube/pages', '.html').map(f => {
    const html = read('youtube/pages/' + f);
    return { page: f, ytInitialData: I.extractJson(html, 'ytInitialData'), ownerChannelId: ownerChannelId(html) };
  });
  writeGolden('youtube/golden/pages.json', pages);
}

(async () => {
  runHelpers();
  runActions();
  await runFlows();
})().catch(e => { console.error(e); process.exit(1); });

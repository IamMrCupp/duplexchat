# Golden fixtures

duplexchat is an exact port of multichat at `29a9d2c`. Most of what it ports was never tested in multichat: IRC parsing, InnerTube parsing, and the exact bytes `config.json` is written with. These scripts run multichat's own code on fixed inputs and save what it produced. The Go tests compare against those files.

## Regenerate

```bash
node scripts/golden/all.js
```

The scripts read multichat's modules straight from git at commit `29a9d2c`, so the checkout's working tree doesn't matter. They look for a clone at `~/Code/megarohas_multichat`; set `MULTICHAT_SRC` to use another. If that commit isn't there, they stop.

Node is needed only here. It isn't used at runtime or in CI.

## What each script covers

| Script | Source code it runs | Inputs | Goldens |
|---|---|---|---|
| `twitch.js` | `TwitchSource` (fake socket), `parseLine`, `unescapeTag`, `rolesFromBadges`, `channelFromUrl`, the emote loaders (fake `fetch`) | `testdata/twitch/` | `testdata/twitch/golden/` |
| `youtube.js` | `YouTubeSource` through resolve → init → poll (fake `fetch`), `_handleActions`, the page and run helpers | `testdata/youtube/` | `testdata/youtube/golden/` |
| `config.js` | `server.js`'s load block, `detectPlatform`, `addSource`, `removeSource`, `saveConfig`, with the real `lib/targets.js` and `cleanPresets` | `testdata/config/cases/` | `testdata/config/golden/` |

## Reading the goldens

- **Status text is multichat's Russian.** duplexchat is English only, so the Go tests compare the state exactly and the text through a translation table.
- **Random values are seeded here and only shape-checked in Go:**
  - message IDs for lines that arrive without one;
  - YouTube's backoff jitter, between 0 and 1 s.
- **Emote maps are sorted by name.** Across services, multichat's winner is a race; duplexchat fixes the order on purpose (BTTV, then FFZ, then 7TV), and its own tests cover that.
- **The goldens change only when the baseline does.** If a regenerate produces a diff without a baseline change, the scripts or the inputs moved. Find out which before committing.

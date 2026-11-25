# Port brief: megarohas_multichat (Node.js) to duplexchat (Go)

Written 2026-11-22 as the input to research and planning. It is not the plan.
The plan, the phase checklists and the session log live in the project's
claude-project-kit working folder, outside this repository.

Three kinds of statement appear below and are labelled: **Decided** (Aaron has
chosen it), **Proposed** (a suggestion to confirm or reject during planning),
and **Open** (needs an answer before or during planning).

## 1. What is being ported

| | |
|---|---|
| Source | [IamMrCupp/megarohas_multichat](https://github.com/IamMrCupp/megarohas_multichat), `main` at `ef3e0f0`, version 0.3.0 |
| Upstream of that | [megarohas/megarohas_multichat](https://github.com/megarohas/megarohas_multichat), MIT |
| Size | about 7,600 lines: 29 JavaScript files, 4 HTML pages, 1 stylesheet |
| Runtime | Node.js 18+, no runtime dependencies |
| Tests | 12 files on Node's built-in runner, all on `127.0.0.1`, no internet |

The tool merges Twitch and YouTube chat into one OBS overlay, gives the
streamer a control panel, and lets them reply to either chat from a dock.

## 2. Decided

- Rewrite the server in Go. The pages in `public/` stay HTML, CSS and
  browser JavaScript.
- New standalone repository, `IamMrCupp/duplexchat`, public, not a GitHub fork.
- MIT license, with megarohas's copyright notice kept beside Aaron's.
- The reason for Go is distribution: one small static executable per platform,
  with the pages embedded, replacing the Node single-executable build chain.
- Work follows the kit: research, then a plan and checklist that Aaron
  approves, then code. Business logic gets tests in the same change.
- Conventional Commits, one line, signed off with `-s`, no AI co-author
  trailers. No work directly on the default branch.

## 3. Source inventory

Server side, which is what gets rewritten:

| File | Lines | What it does |
|---|---|---|
| `server.js` | 589 | HTTP server and router, config load and save, SSE hub with a 100-message history, source registry, port fallback, `--window` launcher |
| `lib/twitch.js` | 430 | Reads Twitch chat over anonymous IRC on TLS (`irc.chat.twitch.tv:6697`, `justinfan` login). Loads BTTV, FFZ and 7TV emote lists. Maps badges to roles |
| `lib/youtube.js` | 481 | Reads YouTube live chat by polling InnerTube `get_live_chat`, the interface the chat page itself uses. Resolves any URL form to a video ID. No API key |
| `lib/twitch-send.js` | 305 | Sends to Twitch chat. Device-code sign-in with a public client, scope `user:write:chat`, 500-character limit, honours `Ratelimit-Reset` on 429 |
| `lib/youtube-send.js` | 394 | Sends to YouTube chat. Installed-app OAuth with the user's own client, loopback redirect, PKCE. Makes exactly three kinds of Data API call to protect quota |
| `lib/oauth-session.js` | 128 | Shared by both senders: HTTP helpers, token refresh near expiry, one retry after a 401, forgetting dead tokens |
| `lib/tokens.js` | 128 | `secrets.json` store (mode 0600, never cached, read-merge-write per section) and the `sending.lock` file that lets only one process send |
| `lib/sendapi.js` | 240 | The dock's HTTP surface: session token, send, reply target, setup endpoints, OAuth callback hand-off |
| `lib/gate.js` | 89 | Guard for every route that can write to the streamer's accounts. See section 5 |
| `lib/targets.js` | 91 | Which chat a reply goes to: one pinned source per platform, each with a key the dock must echo back |
| `lib/viewers.js` | 146 | Viewer counts. Twitch from Get Streams using the existing sign-in; YouTube from InnerTube `updated_metadata` |

Browser side, which is carried over and only touched where the server contract
changes:

| File | Lines | What it is |
|---|---|---|
| `public/overlay.html`, `overlay.css`, `overlay-options.js` | 698 | The OBS overlay and its URL settings |
| `public/panel.html` | 378 | Control panel: add and remove stream links, test messages, clear |
| `public/dock.html`, `dock-logic.js` | 507 | The reply dock for OBS |
| `public/setup.html` | 281 | Sign-in setup for sending |
| `public/viewer-bar.js` | 71 | Viewer count bar |

Not ported: `build/` (esbuild, postject and rcedit packaging of a Node
single-executable), `start.sh`, `start.cmd` and the `start-window` scripts.
Go's own build replaces them.

## 4. Contract the pages depend on

Routes open to any local page:

- `GET /`, `/dock`, `/setup`, `/overlay`, plus the static scripts, stylesheet
  and favicon
- `GET /events` — server-sent events
- `GET /api/state`, `GET /api/fonts`, `GET /fonts/<name>`

Routes behind the gate:

- `POST /api/sources`, `DELETE /api/sources/<id>`,
  `POST /api/sources/<id>/reconnect`, `POST /api/test`, `POST /api/clear`
- `GET /api/dock/session`, `POST /api/send`, `POST /api/dock/target`
- `GET /api/setup/state`, `/api/setup/<platform>/<action>`
- The YouTube sign-in redirect, which returns to `http://127.0.0.1:<port>`

Event types on `/events`: `hello` (history, sources, viewers), `msg`,
`remove`, `removeAuthor`, `clear`, `status`, `viewers`. A heartbeat comment is
written every 25 seconds.

Files written beside the executable: `config.json`, `secrets.json`,
`sending.lock`, `error.log`, and a `fonts/` folder the user fills.
`MULTICHAT_DIR` moves them elsewhere; the tests rely on that.

`config.json` keys: `host` (default `127.0.0.1`), `port` (default `3131`),
`sources`, `twitchClientId`, `youtubeViewers`, `replyTo`.

## 5. Behaviour that must survive the port

These are easy to lose in a rewrite. Most have a test in the source. The
byte-order-mark handling and the port fallback do not, so they need new tests.

- **The gate.** A write request passes only if it comes from a loopback
  address with no proxy headers, its `Host` names this server on a loopback
  name (this is what stops DNS rebinding), its `Origin` and `Sec-Fetch-Site`
  are this server, and it carries the per-process session token.
- **Loopback by default.** Setting `host` to `0.0.0.0` opens the overlay and
  the feed to the LAN. Sending and the panel's controls stay local.
- **Twitch refresh tokens are single-use.** The new token is written to disk
  before anything else happens.
- **Secrets never leave.** No access token, refresh token or client secret
  appears in a response, a log line or an error message.
- **One sender per folder.** `sending.lock` holds the process ID; a second
  copy reads chat but does not send.
- **Replies cannot land in the wrong chat.** A send whose target key no
  longer matches what the dock was showing is refused. Removing the pinned
  source leaves the platform with no target rather than hopping to another.
- **YouTube quota discipline.** Reading never uses the Data API. Sending uses
  `channels.list` once per sign-in, `videos.list` once per broadcast and
  `liveChatMessages.insert` once per reply, and nothing else.
- **Survive bad input and bad networks.** A failing source is logged to
  `error.log` and the process keeps running. `config.json` saved with a UTF-8
  byte-order mark still loads. If the port is busy, the next 20 are tried.
- **Viewer counts never invent a zero.** A platform is `none`, `live`,
  `offline` or `unknown`; a known number is dropped after three failed polls.

## 6. Proposed for the Go version

- **Standard library only.** The source uses no WebSockets: it pushes to
  pages over server-sent events and reads Twitch over a raw TLS socket. Go's
  `net/http`, `crypto/tls`, `encoding/json` and `embed` appear to cover all
  of it. Confirm during research before promising zero dependencies.
- **One goroutine per source**, replacing the Node event-loop adapters, with
  the SSE hub as the single fan-out point.
- **`embed` for `public/`**, so the executable is self-contained.
- **`go test` with `net/http/httptest`**, keeping the source's rule that no
  test touches the real internet. Port the 12 test files' cases, not only
  their happy paths.
- **CI on GitHub Actions**: `go vet`, `go test -race`, and a cross-compile
  check for Linux, Windows and macOS.
- **Same files on disk, same formats**, so an existing install can swap the
  executable and keep its `config.json` and sign-ins.

## 7. Open

1. **Drop-in compatibility.** Should duplexchat read the Node version's
   `config.json` and `secrets.json` unchanged? Proposed yes; it decides how
   much freedom the data model has.
2. **`--window` mode.** The source can launch the panel in an app-style
   browser window, searching fixed install paths for Edge, Chrome or Chromium
   on Windows, macOS and Linux. Keep it, or cut it now that there is an OBS
   dock?
3. **Language of messages.** Some server-side strings and the second README
   are Russian, inherited from upstream. English only from here on?
4. **Renames.** `MULTICHAT_DIR`, the console banner and page titles still
   carry the old name. Rename all, or keep the environment variable for
   compatibility?
5. **Platforms and signing.** Linux x64 is the machine it runs on. Which
   others ship, and is an unsigned macOS build acceptable?
6. **Releases.** release-please, as the kit uses, or GoReleaser?
7. **Default branch.** This repository was created with `master`; the fork
   uses `main`.
8. **Carried-over issues.** The fork has two open design items: a secret
   only the user's account can read, carried in the dock URL
   ([#10](https://github.com/IamMrCupp/megarohas_multichat/issues/10)), and
   reading chat through the official APIs as an option
   ([#12](https://github.com/IamMrCupp/megarohas_multichat/issues/12)).
   In scope for the port, or after it?
9. **Tracking upstream.** When YouTube changes its chat internals, upstream
   fixes `lib/youtube.js`. After the port that fix has to be translated by
   hand. Decide who watches upstream and how.

## 8. What can only be verified on the streaming machine

- Sending as Aaron on Twitch and YouTube. The sign-ins stay on that machine.
- The dock and overlay loaded in OBS, at stream resolution.
- A full-length stream without a dropped connection or a leak.

## 9. Starting work with the kit

On the machine where Claude Code runs:

```bash
git clone https://github.com/IamMrCupp/duplexchat ~/Code/duplexchat
cd ~/Code/duplexchat
~/Code/claude-project-kit/bootstrap.sh
```

Interactive mode asks for the working folder, project name, tracker and CI.
Answer `github` and `github-actions`. Then, in Claude Code in this repository:

> Follow the instructions in `<working-folder>/SEED-PROMPT.md`. Read
> `docs/PORT-BRIEF.md` first: sections 2 and 5 are settled, section 6 is to be
> confirmed, and section 7 is the list of questions to ask me.

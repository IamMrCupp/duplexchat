# duplexchat

[![License: MIT](https://img.shields.io/github/license/IamMrCupp/duplexchat)](LICENSE)
[![Status: port in progress](https://img.shields.io/badge/status-port%20in%20progress-orange)](docs/PORT-BRIEF.md)
[![Language: Go](https://img.shields.io/badge/language-Go-00ADD8?logo=go&logoColor=white)](https://go.dev/)

Twitch and YouTube chat in one place for a streamer: a transparent overlay for
OBS, a control panel, and a dock you can reply from. It runs on your own
machine and talks only to Twitch and YouTube.

> **Nothing here runs yet.** This repository is the Go port of a working
> Node.js tool. If you want something you can use today, use
> [IamMrCupp/megarohas_multichat](https://github.com/IamMrCupp/megarohas_multichat).
> What is being ported, and why, is in [docs/PORT-BRIEF.md](docs/PORT-BRIEF.md).

## Where this comes from

duplexchat is a port of
[megarohas/megarohas_multichat](https://github.com/megarohas/megarohas_multichat)
by megarohas. The chat reading, the overlay and the panel are their design.
The reply dock, the viewer bar and the loopback-only default were added in
[the fork](https://github.com/IamMrCupp/megarohas_multichat) this port starts
from. Both are MIT-licensed, and megarohas's copyright notice is kept in
[LICENSE](LICENSE).

## What it will do

- Read Twitch chat anonymously and YouTube live chat without an API key.
- Show both in one OBS overlay, with emotes, badges, and moderation respected.
- Let the streamer reply to either chat from one dock inside OBS.
- Ship as a single small executable for Linux, Windows and macOS.

## Support

If this saves you time and you'd like to throw a coffee my way:

- [ko-fi.com/iammrcupp](https://ko-fi.com/iammrcupp)
- [buymeacoffee.com/iammrcupp](https://buymeacoffee.com/iammrcupp)

## License

[MIT](LICENSE)

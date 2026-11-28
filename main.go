// duplexchat merges Twitch and YouTube live chat into one OBS overlay.
// It's a Go port of megarohas_multichat; see docs/PORT-BRIEF.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/app"
	"github.com/IamMrCupp/duplexchat/internal/applog"
	"github.com/IamMrCupp/duplexchat/internal/config"
	"github.com/IamMrCupp/duplexchat/internal/emotes"
	"github.com/IamMrCupp/duplexchat/internal/gate"
	"github.com/IamMrCupp/duplexchat/internal/hub"
	"github.com/IamMrCupp/duplexchat/internal/secrets"
	"github.com/IamMrCupp/duplexchat/internal/server"
	"github.com/IamMrCupp/duplexchat/internal/sources"
	"github.com/IamMrCupp/duplexchat/internal/twitch"
	"github.com/IamMrCupp/duplexchat/internal/twitchsend"
	"github.com/IamMrCupp/duplexchat/internal/viewers"
	"github.com/IamMrCupp/duplexchat/internal/window"
	"github.com/IamMrCupp/duplexchat/internal/youtube"
	"github.com/IamMrCupp/duplexchat/internal/youtubesend"
)

// version is set at release with -ldflags "-X main.version=…".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("duplexchat", flag.ContinueOnError)
	flags.SetOutput(stderr)
	showVersion := flags.Bool("version", false, "print the version and exit")
	openWindow := flags.Bool("window", false, "open the control panel in an app window")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}

	dir, err := config.AppDir(getenv, os.Executable)
	if err != nil {
		fmt.Fprintln(stderr, "Can't find the app folder:", err)
		return 1
	}
	fontsDir := filepath.Join(dir, "fonts")
	os.MkdirAll(fontsDir, 0o755) // the user drops fonts in here; failing is fine
	log := applog.NewTo(filepath.Join(dir, "error.log"), stderr)
	cfgPath := filepath.Join(dir, "config.json")
	cfg := config.Load(cfgPath)
	// tokens and client secrets: secrets.json beside config.json, owner-only.
	// One copy per folder sends; a second one still reads chat (two sharing
	// single-use refresh tokens would sign each other out).
	vault := secrets.New(filepath.Join(dir, "secrets.json"), secrets.Options{Log: log.Write})
	sendLock := secrets.AcquireSendLock(dir, os.Getpid(), nil)
	defer sendLock.Release()

	port, err := server.Port(cfg.Port)
	if err != nil {
		log.Write("listen", err)
		return 1
	}
	ln, actual, err := server.Listen(server.Host(cfg.Host), port, 20, func(s string) { fmt.Fprintln(stdout, s) })
	if err != nil {
		log.Write("listen", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var counts *viewers.Counts // set below; the feed's first page asks it
	feed := hub.New(hub.Options{OnFirstClient: func() {
		// first page in: don't wait for the next tick
		if counts != nil {
			go counts.Refresh(context.Background())
		}
	}})
	go feed.Run(ctx)
	store := sources.NewStore(cfg, cfgPath, sources.Options{Warn: func(s string) { fmt.Fprintln(stderr, s) }})
	chats := &app.App{Store: store, Hub: feed, Readers: map[string]app.Factory{
		// DUPLEXCHAT_TWITCH_IRC points the reader at another IRC server (tests)
		"twitch": twitch.Factory(emotes.New().For, getenv("DUPLEXCHAT_TWITCH_IRC")),
		// DUPLEXCHAT_YOUTUBE_ORIGIN points the reader at another YouTube (tests)
		"youtube": youtube.Factory(getenv("DUPLEXCHAT_YOUTUBE_ORIGIN")),
	}}

	// DUPLEXCHAT_TWITCH_HTTP points sending at another Twitch (tests): both
	// id.twitch.tv and api.twitch.tv at one origin
	twitchHTTP := getenv("DUPLEXCHAT_TWITCH_HTTP")
	// A sign-in or sign-out changes what the count can say: ask again now
	// instead of leaving the old answer up until the next tick. Nobody
	// watching, nobody to tell: the first page to connect asks anyway.
	viewersStale := func() {
		if counts != nil && feed.Clients() > 0 {
			go counts.RefreshNow(context.Background())
		}
	}
	twitchSender := twitchsend.New(twitchsend.Options{
		Store: vault, ClientID: store.TwitchClientID, SetClientID: store.SetTwitchClientID,
		IDBase: twitchHTTP, APIBase: twitchHTTP, OnAuthChange: viewersStale,
	})
	youtubeSender := youtubesend.New(youtubesend.Options{
		Store: vault, Endpoints: googleEndpoints(getenv("DUPLEXCHAT_GOOGLE_HTTP")), OnAuthChange: viewersStale,
		GetViewerSource: store.YouTubeViewers, SetViewerSource: store.SetYouTubeViewers,
	})
	senders := map[string]server.Sender{"twitch": twitchSender, "youtube": youtubeSender}

	// Viewer counts for the overlay's bar: Twitch through the official API
	// with the dock's sign-in; YouTube through the watch page's own
	// interface, so no API quota is spent. Only polled while a page is
	// connected, and never with the tokens in a copy that doesn't own sending.
	interval := 30 * time.Second
	if cfg.YouTubeViewers == "api" {
		interval = 60 * time.Second
	}
	youtubeOrigin := getenv("DUPLEXCHAT_YOUTUBE_ORIGIN")
	counts = viewers.New(viewers.Options{
		ListSources: chats.ViewerSources,
		Twitch: func(ctx context.Context, logins []string) (viewers.Twitch, error) {
			if !sendLock.Held {
				return viewers.Twitch{State: "signed_out", Note: "Viewer counts for Twitch come from the copy of the server that owns sending."}, nil
			}
			return twitchSender.Viewers(ctx, logins)
		},
		// Free by default. With "youtubeViewers": "api" in config.json (or the
		// switch on /setup) the official API is asked first; if it can't
		// answer (not signed in, quota gone, count hidden) the free path does.
		YouTube: viewers.PreferOfficial(
			func() bool { return store.YouTubeViewers() == "api" && sendLock.Held },
			youtubeSender.Viewers,
			func(ctx context.Context, s viewers.Source) (*viewers.YouTube, error) {
				return viewers.FetchYouTube(ctx, viewers.FetchOptions{VideoID: s.VideoID, APIKey: s.APIKey, ClientVersion: s.ClientVersion, Origin: youtubeOrigin})
			}),
		Interval: interval,
		Active:   func() bool { return feed.Clients() > 0 },
		OnChange: func(c hub.ViewerCounts) { feed.Viewers(c) },
	})

	handler := server.New(server.Options{
		Port:     actual,
		Token:    gate.NewToken(),
		Panel:    chats,
		Pages:    pages(),
		Events:   feed,
		FontsDir: fontsDir,
		Log:      log.Write,
		Disabled: func() string {
			if sendLock.Held {
				return ""
			}
			return sendLock.Reason
		},
		Targets:   chats.Targets,
		SetTarget: chats.SetTarget,
		Notices:   func() []string { return []string{vault.Problem()} },
		Senders:   senders,
		State: func() server.State {
			return server.State{
				Version: version, Host: cfg.Host, Port: actual,
				LAN: server.LANOrigins(cfg.Host, actual), Sources: store.Status(), Viewers: feed.CurrentViewers(),
			}
		},
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	var notes []string
	if !sendLock.Held {
		notes = append(notes, sendLock.Reason)
	}
	fmt.Fprint(stdout, server.Banner(actual, server.LANOrigins(cfg.Host, actual), notes))
	chats.Start()
	if *openWindow {
		go func() {
			time.Sleep(300 * time.Millisecond)
			line, err := window.Open(window.Real(runtime.GOOS), fmt.Sprintf("http://127.0.0.1:%d/", actual), dir)
			if err != nil {
				log.Write("window", err)
				return
			}
			fmt.Fprintln(stdout, "  "+line)
		}()
	}

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	counts.Start()
	defer counts.Stop()
	select {
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Write("listen", err)
			return 1
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutdown) // the feed's streams end when ctx does
	}
	return 0
}

// googleEndpoints points YouTube sending at another Google (tests):
// DUPLEXCHAT_GOOGLE_HTTP serves /o/oauth2/v2/auth, /token, /revoke and
// /youtube/v3. "" is the real Google.
func googleEndpoints(base string) youtubesend.Endpoints {
	if base == "" {
		return youtubesend.Endpoints{}
	}
	return youtubesend.Endpoints{Auth: base + "/o/oauth2/v2/auth", Token: base + "/token", Revoke: base + "/revoke", API: base + "/youtube/v3"}
}

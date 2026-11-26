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
	"syscall"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/app"
	"github.com/IamMrCupp/duplexchat/internal/applog"
	"github.com/IamMrCupp/duplexchat/internal/config"
	"github.com/IamMrCupp/duplexchat/internal/gate"
	"github.com/IamMrCupp/duplexchat/internal/hub"
	"github.com/IamMrCupp/duplexchat/internal/server"
	"github.com/IamMrCupp/duplexchat/internal/sources"
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
	feed := hub.New(hub.Options{})
	go feed.Run(ctx)
	store := sources.NewStore(cfg, cfgPath, sources.Options{Warn: func(s string) { fmt.Fprintln(stderr, s) }})
	chats := &app.App{Store: store, Hub: feed}

	handler := server.New(server.Options{
		Port:     actual,
		Token:    gate.NewToken(),
		Panel:    chats,
		Pages:    pages(),
		Events:   feed,
		FontsDir: fontsDir,
		Log:      log.Write,
		State: func() server.State {
			return server.State{
				Version: version, Host: cfg.Host, Port: actual,
				LAN: server.LANOrigins(cfg.Host, actual), Sources: store.Status(), Viewers: feed.CurrentViewers(),
			}
		},
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	fmt.Fprint(stdout, server.Banner(actual, server.LANOrigins(cfg.Host, actual), nil))
	chats.Start()

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
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

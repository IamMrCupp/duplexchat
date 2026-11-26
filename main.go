// duplexchat merges Twitch and YouTube live chat into one OBS overlay.
// It's a Go port of megarohas_multichat; see docs/PORT-BRIEF.md.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/IamMrCupp/duplexchat/internal/server"
)

// version is set at release with -ldflags "-X main.version=…".
var version = "dev"

const addr = "127.0.0.1:3131"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	fmt.Printf("duplexchat %s\n  Panel: http://%s/\n", version, addr)
	if err := http.ListenAndServe(addr, server.New(pages())); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

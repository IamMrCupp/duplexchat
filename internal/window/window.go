// Package window opens the control panel in an app-style browser window —
// Edge, Chrome or Chromium with --app — the way multichat's --window flag
// does (server.js at 29a9d2c: openAppWindow).
package window

import (
	"os"
	"os/exec"
	"path/filepath"
)

// System is everything Open touches, so tests can fake it.
type System struct {
	GOOS     string
	Getenv   func(string) string
	Exists   func(path string) bool
	LookPath func(name string) (string, error)
	Start    func(name string, args ...string) error // starts detached and doesn't wait
}

// Real is this machine.
func Real(goos string) System {
	return System{
		GOOS:     goos,
		Getenv:   os.Getenv,
		Exists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
		LookPath: exec.LookPath,
		Start:    startDetached,
	}
}

// candidates are the browsers to try, in multichat's order.
func (s System) candidates() []string {
	switch s.GOOS {
	case "windows":
		join := func(env, rest string) string { return filepath.Join(s.Getenv(env), rest) }
		return []string{
			join("ProgramFiles(x86)", `Microsoft\Edge\Application\msedge.exe`),
			join("ProgramFiles", `Microsoft\Edge\Application\msedge.exe`),
			join("ProgramFiles", `Google\Chrome\Application\chrome.exe`),
			join("LOCALAPPDATA", `Google\Chrome\Application\chrome.exe`),
		}
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	}
	return []string{"microsoft-edge", "google-chrome", "chromium", "chromium-browser"}
}

// Args are the app-window flags: its own profile folder beside the app, a
// panel-sized window, no first-run screens.
func Args(url, appDir string) []string {
	return []string{
		"--app=" + url,
		"--user-data-dir=" + filepath.Join(appDir, ".window-profile"),
		"--window-size=940,960",
		"--no-first-run",
		"--no-default-browser-check",
	}
}

// Open starts the panel window and returns a line saying what it used.
// With no Chromium-family browser it opens the default browser instead.
func Open(s System, url, appDir string) (string, error) {
	for _, c := range s.candidates() {
		found := false
		name := c
		if s.GOOS == "windows" || s.GOOS == "darwin" {
			found = s.Exists(c)
			name = filepath.Base(c)
		} else if p, err := s.LookPath(c); err == nil {
			found, c = true, p
		}
		if found {
			if err := s.Start(c, Args(url, appDir)...); err != nil {
				return "", err
			}
			return "App window: " + name, nil
		}
	}
	var err error
	switch s.GOOS {
	case "windows":
		err = s.Start("cmd", "/c", "start", "", url)
	case "darwin":
		err = s.Start("open", url)
	default:
		err = s.Start("xdg-open", url)
	}
	if err != nil {
		return "", err
	}
	return "App window: no Chromium browser found, opened the default browser", nil
}

func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = detached()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

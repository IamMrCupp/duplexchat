package window

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type started struct {
	name string
	args []string
}

func fake(goos string, have map[string]bool) (System, *[]started) {
	var log []started
	return System{
		GOOS: goos,
		Getenv: func(k string) string {
			return map[string]string{"ProgramFiles(x86)": `C:\PF86`, "ProgramFiles": `C:\PF`, "LOCALAPPDATA": `C:\Users\me\AppData\Local`}[k]
		},
		Exists: func(p string) bool { return have[p] },
		LookPath: func(n string) (string, error) {
			if have[n] {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		Start: func(n string, a ...string) error { log = append(log, started{n, a}); return nil },
	}, &log
}

func TestArgs(t *testing.T) {
	got := strings.Join(Args("http://127.0.0.1:3131/", "/opt/dc"), " ")
	want := "--app=http://127.0.0.1:3131/ --user-data-dir=" + filepath.Join("/opt/dc", ".window-profile") + " --window-size=940,960 --no-first-run --no-default-browser-check"
	if got != want {
		t.Errorf("args = %q", got)
	}
}

func TestPicksFirstBrowserInOrder(t *testing.T) {
	cases := []struct {
		goos string
		have map[string]bool
		exe  string
		line string
	}{
		{"darwin", map[string]bool{"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge": true, "/Applications/Chromium.app/Contents/MacOS/Chromium": true},
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge", "App window: Microsoft Edge"},
		{"linux", map[string]bool{"chromium": true, "google-chrome": true}, "/usr/bin/google-chrome", "App window: google-chrome"},
		{"windows", map[string]bool{filepath.Join(`C:\PF`, `Google\Chrome\Application\chrome.exe`): true},
			filepath.Join(`C:\PF`, `Google\Chrome\Application\chrome.exe`), "App window: " + filepath.Base(filepath.Join(`C:\PF`, `Google\Chrome\Application\chrome.exe`))},
	}
	for _, c := range cases {
		s, log := fake(c.goos, c.have)
		line, err := Open(s, "http://127.0.0.1:3131/", "/opt/dc")
		if err != nil || line != c.line || len(*log) != 1 || (*log)[0].name != c.exe || (*log)[0].args[0] != "--app=http://127.0.0.1:3131/" {
			t.Errorf("%s: %q %v %+v", c.goos, line, err, *log)
		}
	}
}

func TestFallsBackToDefaultBrowser(t *testing.T) {
	want := map[string]string{"darwin": "open http://u/", "linux": "xdg-open http://u/", "windows": "cmd /c start  http://u/"}
	for goos, cmd := range want {
		s, log := fake(goos, nil)
		line, err := Open(s, "http://u/", "/d")
		if err != nil || !strings.Contains(line, "opened the default browser") {
			t.Errorf("%s: %q %v", goos, line, err)
		}
		if got := (*log)[0].name + " " + strings.Join((*log)[0].args, " "); got != cmd {
			t.Errorf("%s: ran %q, want %q", goos, got, cmd)
		}
	}
}

func TestStartFailureIsReported(t *testing.T) {
	s, _ := fake("linux", map[string]bool{"chromium": true})
	s.Start = func(string, ...string) error { return errors.New("exec format error") }
	if _, err := Open(s, "http://u/", "/d"); err == nil {
		t.Error("want the start error")
	}
}

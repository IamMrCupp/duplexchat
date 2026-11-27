package secrets

// multichat test/tokens.test.js at 29a9d2c, case for case.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IamMrCupp/duplexchat/internal/jsjson"
)

func tmp(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "secrets.json")
}

func mode(t *testing.T, f string) os.FileMode {
	t.Helper()
	st, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func skipUnlessPosix(t *testing.T) {
	if !posix {
		t.Skip("mode bits don't apply on Windows")
	}
}

// obj builds a section from alternating keys and values.
func obj(kv ...any) *jsjson.Object {
	o := jsjson.NewObject()
	for i := 0; i < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// plain turns a section into ordinary Go values for comparing.
func plain(o *jsjson.Object) map[string]any {
	if o == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(jsjson.Stringify(o, ""), &m); err != nil {
		panic(err)
	}
	return m
}

func set(t *testing.T, s *Store, section string, v *jsjson.Object) {
	t.Helper()
	if err := s.Set(section, v); err != nil {
		t.Fatal(err)
	}
}

func TestMissingFileReadsEmptyAndIsNotCreated(t *testing.T) {
	f := tmp(t)
	s := New(f, Options{})
	if s.Get("twitch") != nil {
		t.Fatal("want nil")
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("reading created the file")
	}
}

func TestWrittenOwnerOnlyRoundTripsSectionsIndependent(t *testing.T) {
	skipUnlessPosix(t)
	f := tmp(t)
	s := New(f, Options{})
	set(t, s, "twitch", obj("accessToken", "FAKE_ACCESS", "refreshToken", "FAKE_REFRESH"))
	set(t, s, "youtube", obj("clientSecret", "FAKE_SECRET"))
	if m := mode(t, f); m != 0o600 {
		t.Fatalf("mode %o", m)
	}
	again := New(f, Options{})
	if got := plain(again.Get("twitch")); !reflect.DeepEqual(got, map[string]any{"accessToken": "FAKE_ACCESS", "refreshToken": "FAKE_REFRESH"}) {
		t.Fatalf("twitch = %v", got)
	}
	set(t, again, "twitch", nil)
	if New(f, Options{}).Get("twitch") != nil {
		t.Fatal("twitch not removed")
	}
	if got := plain(New(f, Options{}).Get("youtube")); !reflect.DeepEqual(got, map[string]any{"clientSecret": "FAKE_SECRET"}) {
		t.Fatalf("youtube = %v", got)
	}
	if m := mode(t, f); m != 0o600 {
		t.Fatalf("mode %o", m)
	}
	entries, _ := os.ReadDir(filepath.Dir(f))
	if len(entries) != 1 || entries[0].Name() != "secrets.json" {
		t.Fatalf("left behind: %v", entries) // no temp file
	}
}

func TestLoosenedFileIsTightenedBeforeUse(t *testing.T) {
	skipUnlessPosix(t)
	f := tmp(t)
	os.WriteFile(f, []byte(`{"twitch":{"refreshToken":"FAKE_REFRESH"}}`), 0o644)
	os.Chmod(f, 0o644)
	s := New(f, Options{})
	if got := s.Get("twitch").String("refreshToken"); got != "FAKE_REFRESH" {
		t.Fatalf("got %q", got)
	}
	if m := mode(t, f); m != 0o600 {
		t.Fatalf("mode %o", m)
	}
}

func TestUnparseableFileSetAsideReportedNeverOverwritten(t *testing.T) {
	skipUnlessPosix(t)
	f := tmp(t)
	const broken = `{"twitch": {"refreshToken": "FAKE_REFRESH_in_a_broken_file"`
	os.WriteFile(f, []byte(broken), 0o600)
	var logged [][2]string
	at := func() time.Time { return time.Date(2026, 11, 20, 10, 15, 0, 0, time.UTC) }
	s := New(f, Options{Log: func(kind string, what any) { logged = append(logged, [2]string{kind, fmt.Sprint(what)}) }, Now: at})
	if s.Get("twitch") != nil {
		t.Fatal("want nil")
	}
	aside := f + ".broken-20261120T101500Z"
	if b, _ := os.ReadFile(aside); string(b) != broken {
		t.Fatalf("aside = %q", b) // kept, byte for byte
	}
	if m := mode(t, aside); m != 0o600 {
		t.Fatalf("mode %o", m)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("original still there")
	}
	if !strings.Contains(s.Problem(), "couldn't be parsed, so it was moved to secrets.json.broken-20261120T101500Z") {
		t.Fatalf("problem = %q", s.Problem())
	}
	// logged once, by name: no contents, no parser message quoting them
	if len(logged) != 1 || logged[0][0] != "secrets" {
		t.Fatalf("logged = %v", logged)
	}
	if strings.Contains(fmt.Sprint(logged), "FAKE_REFRESH") || strings.Contains(s.Problem(), "FAKE_REFRESH") {
		t.Fatal("contents leaked")
	}
	// a new sign-in writes a fresh file; the broken copy is still there
	set(t, s, "twitch", obj("refreshToken", "FAKE_REFRESH_new"))
	if got := New(f, Options{}).Get("twitch").String("refreshToken"); got != "FAKE_REFRESH_new" {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(aside); err != nil {
		t.Fatal("aside gone")
	}
	// valid JSON that isn't an object counts as broken too
	g := tmp(t)
	os.WriteFile(g, []byte("[1,2,3]"), 0o644)
	s2 := New(g, Options{Now: at})
	if s2.Get("twitch") != nil {
		t.Fatal("want nil")
	}
	if _, err := os.Stat(g + ".broken-20261120T101500Z"); err != nil {
		t.Fatal("array not set aside")
	}
}

func TestUnreadableFileReportedNeverWrittenOver(t *testing.T) {
	skipUnlessPosix(t)
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	f := tmp(t)
	os.WriteFile(f, []byte(`{"twitch":{"refreshToken":"FAKE_REFRESH"}}`), 0o600)
	dir := filepath.Dir(f)
	os.Chmod(dir, 0o000) // the folder: stat fails with EACCES
	var logged []string
	s := New(f, Options{Log: func(_ string, what any) { logged = append(logged, fmt.Sprint(what)) }})
	func() {
		defer os.Chmod(dir, 0o700)
		if s.Get("twitch") != nil {
			t.Fatal("want nil")
		}
		s.Get("twitch")
		if !strings.Contains(s.Problem(), "can't be read (EACCES)") {
			t.Fatalf("problem = %q", s.Problem())
		}
		if len(logged) != 1 {
			t.Fatalf("logged %d times", len(logged)) // not once per read
		}
		if err := s.Set("twitch", obj("refreshToken", "FAKE_REFRESH_new")); err == nil || !strings.Contains(err.Error(), "unreadable") {
			t.Fatalf("set = %v", err)
		}
	}()
	if got := s.Get("twitch").String("refreshToken"); got != "FAKE_REFRESH" {
		t.Fatalf("got %q", got) // untouched, and the problem clears
	}
	if s.Problem() != "" {
		t.Fatalf("problem = %q", s.Problem())
	}
}

func TestTwoStoresSeeEachOtherAndMergeBySection(t *testing.T) {
	f := tmp(t)
	a := New(f, Options{})
	b := New(f, Options{})
	set(t, a, "twitch", obj("refreshToken", "FAKE_REFRESH_1"))
	if got := b.Get("twitch").String("refreshToken"); got != "FAKE_REFRESH_1" {
		t.Fatalf("got %q", got) // no stale cache
	}
	set(t, b, "youtube", obj("clientSecret", "FAKE_SECRET"))   // must not drop a's section
	set(t, a, "twitch", obj("refreshToken", "FAKE_REFRESH_2")) // must not drop b's section
	raw, _ := os.ReadFile(f)
	var got map[string]any
	json.Unmarshal(raw, &got)
	want := map[string]any{"twitch": map[string]any{"refreshToken": "FAKE_REFRESH_2"}, "youtube": map[string]any{"clientSecret": "FAKE_SECRET"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("file = %s", raw)
	}
	set(t, b, "youtube", nil)
	if a.Get("youtube") != nil {
		t.Fatal("youtube not removed")
	}
	if got := b.Get("twitch").String("refreshToken"); got != "FAKE_REFRESH_2" {
		t.Fatalf("got %q", got)
	}
}

func TestSendLockOnePerFolderDeadOwnerTakenOver(t *testing.T) {
	dir := filepath.Dir(tmp(t))
	always := func(int) bool { return true }
	first := AcquireSendLock(dir, 1001, always)
	if !first.Held {
		t.Fatal("first not held")
	}
	if posix {
		if m := mode(t, first.File); m != 0o600 {
			t.Fatalf("mode %o", m)
		}
	}
	second := AcquireSendLock(dir, 1002, func(pid int) bool { return pid == 1001 })
	if second.Held || second.Owner != 1001 || !strings.Contains(second.Reason, "another copy of the server (process 1001)") {
		t.Fatalf("second = %+v", second)
	}
	second.Release() // releasing what you don't hold does nothing
	if b, _ := os.ReadFile(first.File); string(b) != "1001" {
		t.Fatalf("lock = %q", b)
	}
	if !AcquireSendLock(dir, 1001, always).Held {
		t.Fatal("same process again")
	}
	// the owner died without cleaning up
	third := AcquireSendLock(dir, 1003, func(int) bool { return false })
	if !third.Held {
		t.Fatal("third not held")
	}
	if b, _ := os.ReadFile(third.File); string(b) != "1003" {
		t.Fatalf("lock = %q", b)
	}
	third.Release()
	if _, err := os.Stat(third.File); !os.IsNotExist(err) {
		t.Fatal("not released")
	}
	// garbage in the lock file is treated as stale
	os.WriteFile(filepath.Join(dir, "sending.lock"), []byte("not a pid"), 0o600)
	if !AcquireSendLock(dir, 1004, always).Held {
		t.Fatal("garbage not taken over")
	}
}

func TestGetReturnsACopy(t *testing.T) {
	s := New(tmp(t), Options{})
	set(t, s, "twitch", obj("refreshToken", "FAKE_REFRESH"))
	s.Get("twitch").Set("refreshToken", "changed")
	if got := s.Get("twitch").String("refreshToken"); got != "FAKE_REFRESH" {
		t.Fatalf("got %q", got)
	}
}

// Beyond the source's tests.

func TestLockReasonsQuoteTheSourceText(t *testing.T) {
	dir := t.TempDir()
	AcquireSendLock(dir, 1001, nil)
	got := AcquireSendLock(dir, 1002, func(int) bool { return true })
	want := "Sending is off in this copy: another copy of the server (process 1001) is already running from this folder and owns sending. Use that one's dock, or stop it."
	if got.Reason != want {
		t.Fatalf("reason = %q", got.Reason)
	}
	missing := AcquireSendLock(filepath.Join(dir, "nope"), 1, nil)
	if missing.Held || missing.Reason != "Sending is off: couldn't create sending.lock (ENOENT)." {
		t.Fatalf("missing = %+v", missing)
	}
}

func TestReleaseLeavesAnotherOwnersLock(t *testing.T) {
	dir := t.TempDir()
	l := AcquireSendLock(dir, 1001, nil)
	os.WriteFile(l.File, []byte("2002"), 0o600) // taken over meanwhile
	l.Release()
	if b, _ := os.ReadFile(l.File); string(b) != "2002" {
		t.Fatal("released someone else's lock")
	}
}

func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Fatal("this process isn't alive")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if Alive(cmd.Process.Pid) {
		t.Fatal("an exited process is alive")
	}
}

// A copy killed without cleaning up leaves its PID behind; the next one
// takes the lock over with the real liveness check.
func TestLockLeftByKilledProcessIsTakenOver(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "sending.lock"), []byte(fmt.Sprint(cmd.Process.Pid)), 0o600)
	l := AcquireSendLock(dir, os.Getpid(), nil)
	if !l.Held {
		t.Fatalf("not taken over: %+v", l)
	}
	l.Release()
}

func TestArraySectionReadsAsIndexedObject(t *testing.T) {
	f := tmp(t)
	os.WriteFile(f, []byte(`{"twitch":["a","b"],"youtube":"x"}`), 0o600)
	s := New(f, Options{})
	if got := plain(s.Get("twitch")); !reflect.DeepEqual(got, map[string]any{"0": "a", "1": "b"}) {
		t.Fatalf("twitch = %v", got)
	}
	if s.Get("youtube") != nil {
		t.Fatal("a string section should read as nil")
	}
}

func TestRemovingAnAbsentSectionCreatesNoFile(t *testing.T) {
	f := tmp(t)
	set(t, New(f, Options{}), "twitch", nil)
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("file created")
	}
}

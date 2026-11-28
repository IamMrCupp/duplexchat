package main

// The setup page sends people to docs/SETUP-SENDING.md on GitHub, and the
// docs link to each other. A renamed or missing file breaks those links only
// after main moves, so check them here: every link to a file in this repo,
// absolute or relative, names a file that exists.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	repoBlob = regexp.MustCompile(`https://github\.com/IamMrCupp/duplexchat/blob/main/([^"'\s)#?]+)`)
	mdLink   = regexp.MustCompile(`\]\(([^)\s]+)\)`)
)

func TestLinksToRepoFilesExist(t *testing.T) {
	var files []string
	for _, glob := range []string{"public/*.html", "*.md", "docs/*.md"} {
		m, err := filepath.Glob(glob)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	exists := func(from, path string) {
		t.Helper()
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s links to %s, which doesn't exist", from, path)
		}
	}
	checked := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range repoBlob.FindAllStringSubmatch(string(b), -1) {
			exists(f, filepath.FromSlash(m[1]))
			checked++
		}
		if !strings.HasSuffix(f, ".md") {
			continue
		}
		for _, m := range mdLink.FindAllStringSubmatch(string(b), -1) {
			target, _, _ := strings.Cut(m[1], "#")
			if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			exists(f, filepath.Join(filepath.Dir(f), filepath.FromSlash(target)))
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("found no links to check; the patterns are wrong")
	}
}

// The setup page's "Steps:" link is the guide in this repo, not the fork's.
func TestSetupPageLinksToThisRepositorysGuide(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("public", "setup.html"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://github.com/IamMrCupp/duplexchat/blob/main/docs/SETUP-SENDING.md"
	if !strings.Contains(string(b), `href="`+want+`"`) {
		t.Errorf("setup.html doesn't link to %s", want)
	}
}

// Package golden loads the fixtures generated from multichat's own code by
// scripts/golden (see its README). Tests in other packages compare their
// output with these files.
package golden

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Root is the repository's testdata directory, found from this file's
// location so tests in any package can use the same paths.
func Root() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("golden: can't locate testdata")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata")
}

// Path joins elements onto Root.
func Path(elem ...string) string {
	return filepath.Join(append([]string{Root()}, elem...)...)
}

// Read returns a testdata file's bytes, failing the test if it can't.
func Read(t testing.TB, elem ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(Path(elem...))
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	return b
}

// Load decodes a testdata JSON file into v, failing the test if it can't.
func Load(t testing.TB, v any, elem ...string) {
	t.Helper()
	if err := json.Unmarshal(Read(t, elem...), v); err != nil {
		t.Fatalf("golden: %s: %v", filepath.Join(elem...), err)
	}
}

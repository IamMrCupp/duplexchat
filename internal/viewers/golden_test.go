package viewers

// testdata/viewers: what multichat's own parseYouTubeViewers returned, or
// that it threw, for inputs at the edges of the JavaScript it relies on
// (scripts/golden/viewers.js).

import (
	"testing"

	"github.com/IamMrCupp/duplexchat/internal/golden"
	"github.com/IamMrCupp/duplexchat/internal/jsjson"
)

func TestParseGoldens(t *testing.T) {
	in, err := jsjson.Parse(golden.Read(t, "viewers", "cases", "parse.json"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := jsjson.Parse(golden.Read(t, "viewers", "golden", "parse.json"))
	if err != nil {
		t.Fatal(err)
	}
	inputs, wants := in.([]any), want.([]any)
	if len(inputs) != len(wants) || len(inputs) == 0 {
		t.Fatalf("%d inputs, %d goldens", len(inputs), len(wants))
	}
	for i, input := range inputs {
		got, err := ParseYouTube(input)
		w := wants[i]
		if _, throws := jsjson.Get(w, "throws"); throws {
			if err == nil {
				t.Errorf("%d: multichat throws, got %+v (%s)", i, got, jsjson.Stringify(input, ""))
			}
			continue
		}
		out, _ := jsjson.Get(w, "out")
		var exp *YouTube
		if out != nil {
			l, _ := jsjson.Get(out, "live")
			exp = &YouTube{Live: l == true}
			if n, ok := jsjson.Get(out, "count"); ok && n != nil {
				exp.Count = n.(float64)
			}
		}
		if err != nil || (got == nil) != (exp == nil) || (got != nil && *got != *exp) {
			t.Errorf("%d: got %+v %v, multichat %+v (%s)", i, got, err, exp, jsjson.Stringify(input, ""))
		}
	}
}

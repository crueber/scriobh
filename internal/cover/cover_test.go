package cover

import (
	"bytes"
	"encoding/xml"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func wellFormed(t *testing.T, svg []byte) {
	t.Helper()
	d := xml.NewDecoder(bytes.NewReader(svg))
	for {
		if _, err := d.Token(); err == io.EOF {
			return
		} else if err != nil {
			t.Fatalf("the SVG is not well-formed XML: %v\n%s", err, svg)
		}
	}
}

func TestGolden(t *testing.T) {
	cases := map[string]Spec{
		"novel":   {Title: "The Long Tide", Subtitle: "A novel of the salt road", Author: "Máire Ní Bhriain", Kind: "novel", Color: "#2F5D50"},
		"title":   {Title: "Untitled"},
		"long":    {Title: "A Very Long Title That Goes On And On Across Several Lines Of The Cover Before It Ends", Subtitle: "And a subtitle long enough to wrap at least once or twice over", Author: "A. N. Author", Kind: "nonfiction", Color: "#7A4B2A"},
		"unicode": {Title: "春の海 終日のたりのたり", Author: "与謝蕪村", Kind: "essay", Color: "#3B5B7A"},
	}
	for name, spec := range cases {
		got := SVG(spec)
		wellFormed(t, got)
		p := filepath.Join("testdata", name+".golden.svg")
		if *update {
			_ = os.MkdirAll("testdata", 0o755)
			if err := os.WriteFile(p, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%v (run with -update)", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from its golden file (run with -update to accept)\n got:\n%s\nwant:\n%s", name, got, want)
		}
	}
}

func TestDeterministicAndKeyed(t *testing.T) {
	a := Spec{Title: "Same", Subtitle: "s", Author: "a", Kind: "novel", Color: "#2F5D50"}
	if !bytes.Equal(SVG(a), SVG(a)) || a.Key() != a.Key() {
		t.Fatal("not deterministic")
	}
	for _, b := range []Spec{{Title: "Other"}, {Title: "Same", Subtitle: "t"}, {Title: "Same", Subtitle: "s", Author: "b"}, {Title: "Same", Subtitle: "s", Author: "a", Kind: "essay"},
		{Title: "Same", Subtitle: "s", Author: "a", Kind: "novel", Color: "#7A4B2A"}} {
		if a.Key() == b.Key() {
			t.Errorf("%+v has the key of %+v", b, a)
		}
	}
	// White space, case of the colour and an unknown colour are normalised.
	if (Spec{Title: " Same  ", Subtitle: "s", Author: "a", Kind: "novel", Color: "#2f5d50"}).Key() != a.Key() {
		t.Error("equivalent specs have different keys")
	}
	if c := (Spec{Title: "x", Color: "red"}).clean().Color; c == "red" || !strings.HasPrefix(c, "#") {
		t.Errorf("an unknown colour was kept: %q", c)
	}
	// With no colour the pick comes from the title, so it is stable.
	if (Spec{Title: "Stable"}).clean().Color != (Spec{Title: "Stable"}).clean().Color {
		t.Error("the fallback colour is not stable")
	}
}

func TestFlatAndInert(t *testing.T) {
	svg := string(SVG(Spec{Title: "A title", Subtitle: "sub", Author: "me", Kind: "novel", Color: "#2F5D50"}))
	for _, bad := range []string{"Gradient", "gradient", "url(", "<style", " style=", "<script", "<image", "<foreignObject", "href", "filter", "xlink", " on"} {
		if strings.Contains(svg, bad) {
			t.Errorf("the SVG contains %q", bad)
		}
	}
	fills := regexp.MustCompile(`fill="([^"]+)"`).FindAllStringSubmatch(svg, -1)
	for _, f := range fills {
		if !regexp.MustCompile(`^#[0-9A-F]{6}$`).MatchString(f[1]) {
			t.Errorf("fill %q is not a flat hex colour", f[1])
		}
	}
	if !strings.Contains(svg, `fill="#2F5D50"`) {
		t.Error("the flat background is missing")
	}
}

func TestHostileTextIsEscapedAndWellFormed(t *testing.T) {
	hostile := []string{
		`<script>alert(1)</script>`,
		`"><svg onload=alert(1)>`,
		`</text></svg><script>alert(2)</script>`,
		"a\x00b\x01c\x1b[31m\x7fd",
		"bad \ufffe \uffff \xed\xa0\x80 \xff ends",
		"]]> & &amp; &#x41; <![CDATA[ x ]]>",
		"\u202eright-to-left override\u202c and \u200b zero width \ufeff bom",
		"line\nbreak\ttab\rreturn",
		strings.Repeat("W", 500),
		strings.Repeat("\U0001F600", 300),
		strings.Repeat("word ", 400),
		"'\"`${}",
	}
	for _, h := range hostile {
		for _, s := range []Spec{{Title: h}, {Title: "t", Subtitle: h}, {Title: "t", Author: h}, {Title: "t", Kind: h}} {
			svg := SVG(s)
			wellFormed(t, svg)
			// The only elements are the ones the drawing writes.
			for _, m := range regexp.MustCompile(`<([a-zA-Z]+)`).FindAllStringSubmatch(string(svg), -1) {
				switch m[1] {
				case "svg", "title", "rect", "text":
				default:
					t.Errorf("%q produced element <%s>", h, m[1])
				}
			}
			if bytes.ContainsAny(svg, "\x00\x01\x1b\x7f") {
				t.Errorf("%q left a control character", h)
			}
		}
	}
}

func TestWrappingStaysInsideTheCover(t *testing.T) {
	titles := []string{"Short", strings.Repeat("Supercalifragilistic", 8), strings.Repeat("The quick brown fox ", 30), strings.Repeat("字", 120), strings.Repeat("W", 200)}
	for _, title := range titles {
		lines, size := fit(title, 66, 30, 0, float64(width-2*margin), 5, 320)
		if len(lines) == 0 || len(lines) > 5 {
			t.Errorf("%q: %d lines", title[:min(10, len(title))], len(lines))
		}
		for _, l := range lines {
			if w := measure(l, size, 0); w > float64(width-2*margin)+1 {
				t.Errorf("a line is %.0f wide in a space of %d: %q", w, width-2*margin, l)
			}
		}
		if float64(len(lines))*size*1.3 > 320.5 {
			t.Errorf("the title is %.0f tall", float64(len(lines))*size*1.3)
		}
	}
	// A title that fits is not shrunk, one that does not is shrunk before it is cut.
	if _, size := fit("Short", 66, 30, 0, 488, 5, 320); size != 66 {
		t.Errorf("a short title shrank to %.0f", size)
	}
	if lines, size := fit(strings.Repeat("The quick brown fox ", 12), 66, 30, 0, 488, 5, 320); size >= 66 || strings.HasSuffix(lines[len(lines)-1], "…") && size > 30 {
		t.Errorf("size %.0f, last line %q", size, lines[len(lines)-1])
	}
	// At the smallest size an over-long title is cut with an ellipsis.
	lines, _ := fit(strings.Repeat("word ", 400), 66, 30, 0, 488, 5, 320)
	if !strings.HasSuffix(lines[len(lines)-1], "…") {
		t.Errorf("no ellipsis: %q", lines)
	}
}

func TestCacheIsBoundedAndKeyedByContent(t *testing.T) {
	k1, a := Cached(Spec{Title: "Cached one"})
	k2, b := Cached(Spec{Title: "Cached one"})
	if k1 != k2 || &a[0] != &b[0] {
		t.Error("the second call did not come from the cache")
	}
	for i := 0; i < cacheMax+20; i++ {
		Cached(Spec{Title: "bulk " + string(rune('a'+i%26)) + strings.Repeat("x", i)})
	}
	mu.Lock()
	n := len(cache)
	mu.Unlock()
	if n > cacheMax {
		t.Errorf("the cache holds %d covers", n)
	}
}

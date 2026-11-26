package i18n_test

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/web"
)

var (
	reTplKey = regexp.MustCompile(`\.T "([A-Za-z0-9_.]+)"`)
	reJSKey  = regexp.MustCompile(`\bT\("([a-z_0-9]+)",\s*"((?:[^"\\]|\\.)*)"`)
	reMsgs   = regexp.MustCompile(`\.Msgs "([A-Za-z0-9_.]+)"`)
)

// Every message a template asks for is in the English catalogue, and every default a script carries is the English
// text, so a screen never shows a key and a script never disagrees with the catalogue it is handed.
func TestEveryKeyTheScreensUseIsInTheCatalogue(t *testing.T) {
	c := i18n.Default
	tpl, _ := fs.Glob(web.FS, "templates/*.html")
	part, _ := fs.Glob(web.FS, "templates/partials/*.html")
	used := map[string]string{}
	for _, f := range append(tpl, part...) {
		b, _ := fs.ReadFile(web.FS, f)
		for _, m := range reTplKey.FindAllStringSubmatch(string(b), -1) {
			used[m[1]] = f
			if !c.Has(m[1]) {
				t.Errorf("%s asks for %q, which the English catalogue lacks", f, m[1])
			}
		}
		for _, m := range reMsgs.FindAllStringSubmatch(string(b), -1) {
			n := 0
			for _, k := range c.Keys() {
				if strings.HasPrefix(k, m[1]) {
					n++
				}
			}
			if n == 0 {
				t.Errorf("%s hands %q to a script, and no key has it as a prefix", f, m[1])
			}
		}
	}
	if len(used) < 40 {
		t.Errorf("only %d keys found in the templates: is the pattern stale?", len(used))
	}
	prefix := map[string]string{"review.js": "review.js.", "suggest.js": "review.js.", "share.js": "share.js.", "gate.js": "gate.js.", "compile.js": "cmp.js.", "compile-print.js": "cmp.js.", "split.js": "split.js.", "lookup.js": "lk.js.", "cwrite.js": "cw.js.", "lock.js": "lock.js."}
	for file, pre := range prefix {
		b, err := fs.ReadFile(web.FS, "static/js/"+file)
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, m := range reJSKey.FindAllStringSubmatch(string(b), -1) {
			found++
			want := strings.NewReplacer(`\"`, `"`, `“`, "“", `”`, "”", `…`, "…", `·`, "·").Replace(m[2])
			if got := c.Raw("en", pre+m[1]); got != want {
				t.Errorf("%s: T(%q) says %q, the catalogue says %q", file, m[1], want, got)
			}
		}
		min := 10
		if file == "lock.js" { // the locked page has five messages; the others have ten or more
			min = 5
		}
		if found < min {
			t.Errorf("%s: only %d messages found", file, found)
		}
	}
}

func TestLookupFallsBackToEnglishAndPlaceholdersAreFilled(t *testing.T) {
	c := i18n.Default
	if got := c.T("en", "rq.open_many", "n", 7); got != "7 open comments." {
		t.Errorf("a placeholder: %q", got)
	}
	if got := c.T("de-AT", "rq.title"); got != "Review" {
		t.Errorf("a language with no catalogue falls back to English: %q", got)
	}
	if got := c.T("en", "no.such.key"); got != "no.such.key" {
		t.Errorf("a key nobody wrote is shown as itself: %q", got)
	}
	if got := c.T("", "rq.title"); got != "Review" {
		t.Errorf("no language: %q", got)
	}
}

// The pseudo-locales test the whole path without a translation: every string is accented and bracketed, placeholders
// survive, and the right-to-left one sets the direction.
func TestPseudoLocales(t *testing.T) {
	c := i18n.Default
	got := c.T("xx", "rq.open_many", "n", 3)
	if !strings.HasPrefix(got, "⟦") || !strings.HasSuffix(got, "⟧") || !strings.Contains(got, "3") || strings.Contains(got, "{n}") || strings.Contains(got, "open comments") {
		t.Errorf("pseudo: %q", got)
	}
	if c.Dir("xx") != "ltr" || c.Dir("xx-rtl") != "rtl" || c.Dir("en") != "ltr" || c.Dir("zz") != "ltr" {
		t.Error("directions")
	}
	if r := c.T("xx-rtl", "rq.title"); !strings.HasPrefix(r, "‏") {
		t.Errorf("rtl pseudo: %q", r)
	}
	// every key survives the pseudo path and keeps its placeholders
	ph := regexp.MustCompile(`\{[a-z]+\}`)
	for _, k := range c.Keys() {
		en, p := c.Raw("en", k), c.Raw("xx", k)
		if strings.Join(ph.FindAllString(en, -1), ",") != strings.Join(ph.FindAllString(p, -1), ",") {
			t.Errorf("%s: placeholders %q became %q", k, en, p)
		}
	}
	for _, l := range c.Locales() {
		if l.Tag == "xx" && !l.Test {
			t.Error("the pseudo-locale is not marked as a test")
		}
	}
	if !c.Known("xx") || !c.Known("en-GB") || c.Known("zz") {
		t.Error("Known")
	}
}

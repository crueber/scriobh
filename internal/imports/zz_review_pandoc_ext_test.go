package imports_test

import "testing"

// GUARD (passes today): a format name that is not exactly one of the ten allow-listed words is refused before
// Pandoc is started: dashes, equals, spaces, NUL, a Cyrillic lookalike, case, and Pandoc's `+extension` suffixes.
var reviewHostileFormatNames = []string{
	"-", "--", "-f", "=", "odt=", " odt", "odt ", "od t", "odt\x00", "\x00odt", "odt\x00html", "оdt", /* Cyrillic o */
	"ODT", "Odt", "rst+raw_html", "latex+raw_tex", "gfm+lua", "markdown+raw_html", "rst-smart", "typst+lua", "custom.lua", "./x.lua",
	"odt\n", "odt\r\n--lua-filter=x", "docx", "html", "markdown", "gfm", "json", "native",
}

func TestReviewImportRefusesHostileFormatNamesBeforePandocStarts(t *testing.T) {
	p := newPandocEnv(t, true)
	for _, f := range reviewHostileFormatNames {
		if _, err := p.preview(f, nil); err == nil {
			t.Errorf("format %q was accepted", f)
		}
	}
	if p.conversions() != 0 {
		t.Fatal("Pandoc was started for a name that is not on the list")
	}
}

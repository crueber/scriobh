package ops

import (
	"strings"
	"testing"
)

func TestSafeFilename(t *testing.T) {
	for in, want := range map[string]string{
		"book.md":                        "book.md",
		"../../etc/passwd":               "etc-passwd",
		`..\..\windows\system32`:         "windows-system32",
		`a "quoted" name.md`:             "a quoted name.md",
		"line\nbreak\r\nhere.md":         "line break here.md",
		"nul\x00byte.md":                 "nulbyte.md",
		"  .hidden  ":                    "hidden",
		"tab\tseparated  name":           "tab separated name",
		"name: with|odd?chars*<>":        "name-with-odd-chars",
		"":                               "download",
		"...":                            "download",
		"////":                           "download",
		"Máire's boat.md":                "Máires boat.md",
		"bom\ufeffname\u200bhere":        "bomnamehere",
		"sep\u2028arator":                "sep arator",
		strings.Repeat("é", 200) + ".md": strings.Repeat("é", 60),
	} {
		got := SafeFilename(in)
		if got != want && !(strings.HasPrefix(want, "é") && strings.HasPrefix(got, "é") && len(got) <= 120) {
			t.Errorf("SafeFilename(%q) = %q, want %q", in, got, want)
		}
		for _, r := range got {
			if r < 0x20 || r == 0x7f || r == '/' || r == '\\' || r == '"' {
				t.Errorf("SafeFilename(%q) = %q holds %U", in, got, r)
			}
		}
		if len(got) > 120 {
			t.Errorf("SafeFilename(%q) is %d bytes", in, len(got))
		}
	}
}

func TestContentDisposition(t *testing.T) {
	got := ContentDisposition("Máire \"x\"\r\nSet-Cookie: a=b.md")
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("header injection: %q", got)
	}
	if !strings.HasPrefix(got, `attachment; filename="`) || !strings.Contains(got, `filename*=UTF-8''M%C3%A1ire%20x%20Set-Cookie-a%3Db.md`) {
		t.Errorf("got %q", got)
	}
	if strings.Count(got, `"`) != 2 {
		t.Errorf("the quoted fallback is not one clean string: %q", got)
	}
	if got := ContentDisposition("book.md"); got != `attachment; filename="book.md"; filename*=UTF-8''book.md` {
		t.Errorf("got %q", got)
	}
}

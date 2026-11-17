package publish

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

func pagedOptions() Options {
	o := print6x9()
	o.Format = "html-paged"
	return o
}

func TestHTMLPagedIsSelfContainedAndInert(t *testing.T) {
	b := bookFrom("A <script>alert(1)</script> \"title\" }</style><script>x</script>", feature)
	b.Byline = `Name "</style><img src=x onerror=alert(1)>`
	b.Images = map[string]Image{"/files/att_picture1234": jpegImage(300, 200)}
	out, _, err := WriteHTMLPaged(b, pagedOptions())
	if err != nil {
		t.Fatal(err)
	}
	keep(t, "paged.html", out)
	h := string(out)
	for _, bad := range []string{"<script", "<link", "<iframe", "<object", "<embed", "<form", "http-equiv=\"refresh\"", "javascript:", "@import", "url(", "src=\"http", "href=\"http"} {
		if strings.Contains(strings.ToLower(h), strings.ToLower(bad)) {
			t.Errorf("the file contains %q", bad)
		}
	}
	noValues := regexp.MustCompile(`="[^"]*"`).ReplaceAllString(h, `=""`) // text inside an attribute value is escaped, not markup
	if m := regexp.MustCompile(`(?i)<[a-z][^>]*\son[a-z]+\s*=`).FindString(noValues); m != "" {
		t.Errorf("an element carries an event handler: %s", m)
	}
	// the only src is a data: image, the only style is the one the CSP names by hash
	for _, m := range regexp.MustCompile(`src="([^"]*)"`).FindAllStringSubmatch(h, -1) {
		if !strings.HasPrefix(m[1], "data:image/") {
			t.Errorf("src %q", m[1])
		}
	}
	styles := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindAllStringSubmatch(h, -1)
	if len(styles) != 1 {
		t.Fatalf("%d style elements", len(styles))
	}
	sum := sha256.Sum256([]byte(styles[0][1]))
	if !strings.Contains(h, "style-src 'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") {
		t.Error("the Content-Security-Policy does not name the stylesheet by its hash")
	}
	if !strings.Contains(h, "default-src 'none'") || strings.Contains(h, "unsafe-inline") {
		t.Error("the Content-Security-Policy is not closed")
	}
	// hostile titles are text in the head and strings in the stylesheet
	if strings.Count(h, "<style>") != 1 || strings.Count(h, "</style>") != 1 {
		t.Error("a title closed the style element")
	}
	css := styles[0][1]
	if !strings.Contains(css, `content: "A `) || strings.Contains(css, "<") || strings.Contains(css, "alert") && strings.Contains(css, "(1)<") {
		t.Errorf("the title is not an escaped CSS string:\n%s", css[:600])
	}
	// the page and the type
	for _, want := range []string{"@page { size: 432pt 648pt;", "break-before: right", "orphans: 2", "@page :left", "@page :right", "counter(page, lower-roman)", `class="chapter bodystart"`, "&lt;script&gt;alert(1)"} {
		if !strings.Contains(h, want) {
			t.Errorf("the file lacks %q", want)
		}
	}
	// deterministic
	again, _, _ := WriteHTMLPaged(b, pagedOptions())
	if !bytes.Equal(out, again) {
		t.Error("two runs differ")
	}
}

func TestCSSStringEscapes(t *testing.T) {
	for in, want := range map[string]string{
		`plain title`: `"plain title"`,
		`a"b`:         `"a\22 b"`,
		`a\b`:         `"a\5C b"`,
		`</style>`:    `"\3C \2F style\3E "`,
		"line\nbreak": `"line break"`,
		"é":           `"\E9 "`,
		"‮overrid":    `"overrid"`,
	} {
		if got := cssString(in); got != want {
			t.Errorf("cssString(%q) = %s, want %s", in, got, want)
		}
	}
}

// A web link is its label (and, with link_urls, the address in parentheses):
// the paged HTML never carries a live external link.
func TestPagedHTMLHasNoLiveLinks(t *testing.T) {
	md := "See [the site](https://example.com/a?b=1&c=2) and [mail](mailto:me@example.com) and [evil](javascript:alert(1)) and [rel](/files/att_x) and <https://auto.example.org/>.[^1]\n\n[^1]: A note with [a link](http://example.net/n).\n"
	for _, urls := range []bool{false, true} {
		o := pagedOptions()
		if urls {
			o.LinkURLs = True()
		}
		out, _, err := WriteHTMLPaged(bookFrom("Links", md), o)
		if err != nil {
			t.Fatal(err)
		}
		h := string(out)
		body := h[strings.Index(h, "<body>"):]
		for _, bad := range []string{`href="http`, `href="mailto`, `href="javascript`, `href="/files`, `href='http`} {
			if strings.Contains(body, bad) {
				t.Errorf("link_urls=%v: the body contains %s", urls, bad)
			}
		}
		for _, want := range []string{"the site", "mail", "evil", "a link"} {
			if !strings.Contains(body, want) {
				t.Errorf("link_urls=%v: %q is missing", urls, want)
			}
		}
		has := func(s string) bool { return strings.Contains(body, s) }
		if urls {
			if !has("the site (https://example.com/a?b=1&amp;c=2)") || !has("mail (mailto:me@example.com)") || !has("a link (http://example.net/n)") {
				t.Errorf("the addresses are not in parentheses after their labels:\n%s", body)
			}
			// an unsafe address is never a link: the parser leaves it as literal text, which is inert
			if strings.Contains(body, `href="javascript`) {
				t.Error("an unsafe address became a link")
			}
		} else if has("example.com") || has("mailto") || has("example.net") {
			t.Error("an address appeared without link_urls")
		}
		// the only anchors left are in-page: the contents and the notes
		for _, m := range regexp.MustCompile(`<a [^>]*href="([^"]*)"`).FindAllStringSubmatch(body, -1) {
			if !strings.HasPrefix(m[1], "#") {
				t.Errorf("an anchor leaves the page: %s", m[1])
			}
		}
	}
}

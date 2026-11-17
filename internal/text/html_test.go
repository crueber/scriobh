package text

import (
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
)

var (
	reTag   = regexp.MustCompile(`<(/?)([A-Za-z][A-Za-z0-9]*)((?:\s+[a-z-]+(?:="[^"]*")?)*)\s*/?>`)
	reAnyLt = regexp.MustCompile(`<`)
	reAttr  = regexp.MustCompile(`\s+([a-z-]+)(?:="([^"]*)")?`)
)

var allowedTags = map[string]bool{"p": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "em": true, "strong": true,
	"del": true, "code": true, "pre": true, "a": true, "img": true, "span": true, "br": true, "hr": true, "ul": true, "ol": true, "li": true,
	"blockquote": true, "table": true, "thead": true, "tr": true, "th": true, "td": true, "section": true, "sup": true}

var allowedAttrs = map[string]bool{"href": true, "rel": true, "referrerpolicy": true, "title": true, "class": true, "id": true, "src": true,
	"alt": true, "start": true, "role": true, "aria-label": true}

// assertInert fails when the HTML holds anything but the elements and attributes
// the renderer is allowed to write. Every `<` in the output must open one of
// them: user text is escaped, so a stray one is a hole.
func assertInert(t *testing.T, out string) {
	t.Helper()
	tags := reTag.FindAllStringSubmatchIndex(out, -1)
	if got, want := len(tags), len(reAnyLt.FindAllStringIndex(out, -1)); got != want {
		t.Fatalf("%d tags found for %d '<' characters; something is not a whitelisted tag:\n%s", got, want, out)
	}
	for _, m := range tags {
		name := strings.ToLower(out[m[4]:m[5]])
		if !allowedTags[name] {
			t.Errorf("tag <%s> is not allowed in:\n%s", name, out)
		}
		for _, a := range reAttr.FindAllStringSubmatch(out[m[6]:m[7]], -1) {
			if !allowedAttrs[a[1]] {
				t.Errorf("attribute %s is not allowed on <%s>", a[1], name)
			}
			val := html.UnescapeString(a[2])
			switch a[1] {
			case "href":
				if !regexp.MustCompile(`(?i)^(https?://|mailto:|#)`).MatchString(val) {
					t.Errorf("href %q is not http, https, mailto or a fragment", val)
				}
			case "src":
				if !reDataImage.MatchString(val) {
					t.Errorf("src %q is not an inline raster image", val)
				}
			}
		}
	}
	low := strings.ToLower(out)
	for _, bad := range []string{"<script", "<iframe", "<object", "<embed", "<form", "<style", "<link", "<meta", "<svg", "<base"} {
		if strings.Contains(low, bad) {
			t.Errorf("output contains %q:\n%s", bad, out)
		}
	}
}

func TestRenderHTMLHostileInputIsInert(t *testing.T) {
	cases := map[string]string{
		"script block":     "<script>alert(1)</script>\n\ntext",
		"script inline":    "a <script>alert(1)</script> b",
		"img onerror":      "<img src=x onerror=alert(1)>",
		"img inline":       "text <img src=x onerror=alert(1)> text",
		"svg":              "<svg onload=alert(1)><circle/></svg>",
		"iframe":           "<iframe src=\"javascript:alert(1)\"></iframe>",
		"style":            "<style>body{background:url(http://evil/x)}</style>",
		"meta refresh":     "<meta http-equiv=refresh content=\"0;url=http://evil\">",
		"form":             "<form action=http://evil><input name=x></form>",
		"js link":          "[click](javascript:alert(1))",
		"js link spaced":   "[click](java\tscript:alert(1))",
		"js link entity":   "[click](&#106;avascript:alert(1))",
		"vbscript":         "[x](vbscript:msgbox(1))",
		"data link":        "[x](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)",
		"file link":        "[x](file:///etc/passwd)",
		"relative link":    "[x](../secret)",
		"autolink js":      "<javascript:alert(1)>",
		"image data html":  "![x](data:text/html;base64,PHNjcmlwdD4=)",
		"image data svg":   "![x](data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+)",
		"image remote":     "![tracker](http://evil.example/p.gif)",
		"image js":         "![x](javascript:alert(1))",
		"title quote":      "[x](http://a.example \"t\" onclick=\"alert(1)\")",
		"title attr":       "[x](http://a.example \"\\\" onmouseover=\\\"alert(1)\")",
		"alt quote":        "![\"onerror=alert(1) x](a.png)",
		"heading":          "# <img src=x onerror=alert(1)>",
		"fence lang":       "```\"><script>alert(1)</script>\ncode\n```",
		"fence lang 2":     "```js onload=alert(1)\ncode\n```",
		"html comment tag": "<!-- --><script>alert(1)</script>",
		"entities":         "&lt;script&gt;alert(1)&lt;/script&gt; &#60;script&#62;",
		"table cell":       "| a | b |\n|---|---|\n| <script>alert(1)</script> | [x](javascript:alert(1)) |",
		"footnote":         "text[^<script>]\n\n[^x]: <script>alert(1)</script>",
		"wiki":             "[[<script>alert(1)</script>|<img src=x onerror=1>]]",
		"directive":        ":::<script>alert(1)</script>\n<img src=x onerror=1>\n:::",
		"blockquote":       "> <script>alert(1)</script>",
		"nul":              "a\x00<script>alert(1)</script>",
		"private":          "%%<script>alert(1)</script>%% and <img src=x onerror=1>",
	}
	for name, md := range cases {
		for _, private := range []bool{false, true} {
			out := RenderHTML(md, HTMLOptions{Private: private, WikiHref: func(string) (string, bool) { return "#x", true }})
			t.Run(name, func(t *testing.T) { assertInert(t, out) })
		}
	}
}

// Whatever sits inside a %%private note%% stays out of the default rendering:
// not its text, and not the address, title, alt text, target or label of a link,
// image or reference that was inside it either.
func TestRenderHTMLPrivateNotesLeakNothing(t *testing.T) {
	secrets := []string{"SECRETWORD", "secret.example", "hiddentitle", "hiddenalt", "hiddenwiki", "hiddencode", "hiddenraw", "hiddenfootnote", "hiddenref"}
	cases := map[string]string{
		"text":              "Keep %%SECRETWORD%% going.",
		"link":              "Keep %%see [SECRETWORD](https://secret.example/x \"hiddentitle\")%% going.",
		"link text only":    "Keep [%%SECRETWORD%%](https://secret.example/x \"hiddentitle\") going.",
		"link crossing":     "Keep %%SECRETWORD [a](https://secret.example/x \"hiddentitle\") b%% going.",
		"autolink":          "Keep %%<https://secret.example/x>%% going.",
		"image":             "Keep %%![hiddenalt](https://secret.example/p.png \"hiddentitle\")%% going.",
		"data image":        "Keep %%![hiddenalt](data:image/png;base64,iVBORw0KGgo=)%% going.",
		"wiki":              "Keep %%[[hiddenwiki|SECRETWORD]]%% going.",
		"code span":         "Keep %%`hiddencode`%% going.",
		"raw html":          "Keep %%<b>hiddenraw</b>%% going.",
		"emphasis":          "Keep %%*SECRETWORD* and **hiddencode**%% going.",
		"strikethrough":     "Keep %%~~SECRETWORD~~%% going.",
		"heading":           "# Title %%SECRETWORD [x](https://secret.example)%% end",
		"list item":         "- one %%SECRETWORD [x](https://secret.example)%%\n- two",
		"ordered item":      "1. one %%SECRETWORD%%\n2. two",
		"quote":             "> quoted %%SECRETWORD [x](https://secret.example)%% end",
		"table cell":        "| a | b |\n|---|---|\n| %%SECRETWORD [x](https://secret.example)%% | 2 |",
		"footnote body":     "Text[^1].\n\n[^1]: note %%SECRETWORD [x](https://secret.example)%% end",
		"footnote ref":      "Text %%hiddenref[^h]%% more.\n\n[^h]: hiddenfootnote and SECRETWORD",
		"two refs one note": "Text %%a[^h]%% more %%b%%.\n\n[^h]: hiddenfootnote",
		"across lines":      "Keep %%SECRETWORD\n[x](https://secret.example)%% going.",
		"nested emphasis":   "Keep *a %%SECRETWORD* [x](https://secret.example)%% going.",
		"hard break":        "Keep %%SECRETWORD\\\n[x](https://secret.example)%% going.",
	}
	for name, md := range cases {
		out := RenderHTML(md, HTMLOptions{WikiHref: func(string) (string, bool) { return "#hiddenwiki", true }})
		assertInert(t, out)
		for _, sec := range secrets {
			if strings.Contains(out, sec) {
				t.Errorf("%s: %q is in the default rendering:\n%s\n---\n%s", name, sec, md, out)
			}
		}
		if strings.Contains(out, "private-note") {
			t.Errorf("%s: a private marker in the default rendering", name)
		}
		// And a document with no pair of marks has nothing to hide: the same text without
		// the marks does show these things, so the test is not passing by dropping everything.
		kept := RenderHTML(strings.ReplaceAll(md, "%%", ""), HTMLOptions{WikiHref: func(string) (string, bool) { return "#x", true }})
		shown := false
		for _, sec := range secrets {
			shown = shown || strings.Contains(kept, sec)
		}
		if !shown {
			t.Errorf("%s: the case shows nothing even without the marks:\n%s", name, kept)
		}
		// With private notes asked for they are all there, marked.
		with := RenderHTML(md, HTMLOptions{Private: true, WikiHref: func(string) (string, bool) { return "#x", true }})
		assertInert(t, with)
		if !strings.Contains(with, "private-note") && name != "two refs one note" && name != "footnote ref" {
			t.Errorf("%s: kept notes are not marked:\n%s", name, with)
		}
	}
	// The visible reference to a footnote also named in a private note keeps its text.
	out := RenderHTML("Seen[^h] and %%unseen[^h]%%.\n\n[^h]: shown note", HTMLOptions{})
	if !strings.Contains(out, "shown note") || strings.Count(out, `class="fnref"`) != 1 {
		t.Errorf("a footnote that is also referenced in a private note:\n%s", out)
	}
}

// The fallback for text the parser cannot read is still free of comments and
// private notes.
func TestRenderHTMLParserFailureFallback(t *testing.T) {
	// Not reachable with ordinary text (the parser is bounded and recovered); call the
	// pieces the fallback uses on the shapes it must remove.
	got := rePrivateSpan.ReplaceAllString(reComment.ReplaceAllString("a <!-- scriobh:alts {\"x\":1} --> b %%SECRETWORD%% c %%unclosed", ""), "")
	if strings.Contains(got, "scriobh") || strings.Contains(got, "SECRETWORD") || strings.Contains(got, "unclosed") {
		t.Errorf("fallback text %q", got)
	}
}

func TestRenderHTMLRules(t *testing.T) {
	r := func(md string, o ...HTMLOptions) string {
		var op HTMLOptions
		if len(o) > 0 {
			op = o[0]
		}
		return RenderHTML(md, op)
	}
	has := func(t *testing.T, out string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("missing %q in:\n%s", w, out)
			}
		}
	}
	hasNot := func(t *testing.T, out string, bad ...string) {
		t.Helper()
		for _, w := range bad {
			if strings.Contains(out, w) {
				t.Errorf("unexpected %q in:\n%s", w, out)
			}
		}
	}
	t.Run("prose", func(t *testing.T) {
		out := r("A *quiet* **loud** ~~old~~ line with `code`.\n\nSecond.\n")
		has(t, out, "<p>A <em>quiet</em> <strong>loud</strong> <del>old</del> line with <code>code</code>.</p>", "<p>Second.</p>")
	})
	t.Run("escapes and entities", func(t *testing.T) {
		has(t, r(`Tom \& Jerry &amp; &copy; 5 < 6 \*not\*`), "Tom &amp; Jerry &amp; \u00a9 5 &lt; 6 *not*")
	})
	t.Run("raw html is text, comments are gone", func(t *testing.T) {
		out := r("before <b>bold</b> after <!-- hidden --> end\n\n<div class=\"x\">block</div>\n\n<!-- scriobh:alts {\"version\":1} -->\n")
		has(t, out, "<code class=\"raw\">&lt;b&gt;</code>", "<pre class=\"source\">&lt;div class=&#34;x&#34;&gt;block&lt;/div&gt;</pre>")
		hasNot(t, out, "hidden", "scriobh:alts", "version")
	})
	t.Run("front matter is dropped", func(t *testing.T) {
		out := r("---\ntitle: Secret plan\ntags: [a]\n---\n\nBody.\n")
		hasNot(t, out, "Secret plan", "title:")
		has(t, out, "<p>Body.</p>")
	})
	t.Run("private notes", func(t *testing.T) {
		md := "Keep %%drop this *note*%% going.\n\nA %%second\nacross lines%% one.\n\nUnpaired %% stays.\n\nEmpty %%%% stays.\n\nEscaped \\%% stays.\n"
		out := r(md)
		hasNot(t, out, "drop this", "across lines", "private-note")
		has(t, out, "Keep  going.", "Unpaired %% stays.", "Empty %%%% stays.", "Escaped %% stays.")
		kept := r(md, HTMLOptions{Private: true})
		has(t, kept, "Keep <span class=\"private-note\">drop this </span><em><span class=\"private-note\">note</span></em>", "across lines")
		hasNot(t, kept, "%%drop")
	})
	t.Run("private matches the counter", func(t *testing.T) {
		md := "one %%two three%% four five\n"
		if got, want := visible(r(md)), SearchBody(md); got != want {
			t.Errorf("rendered %q, counter reads %q", got, want)
		}
	})
	t.Run("links", func(t *testing.T) {
		out := r("[a](https://x.example/p?q=1&r=2) [b](mailto:a@b.example) [c](#here) [d](/local) [e](javascript:alert(1))")
		has(t, out, `<a href="https://x.example/p?q=1&amp;r=2" rel="noopener noreferrer" referrerpolicy="no-referrer">a</a>`, `href="mailto:a@b.example"`, `href="#here"`)
		hasNot(t, out, `href="/local"`, `href="javascript`)
		has(t, out, "d", "[e](javascript:alert(1))")
	})
	t.Run("images", func(t *testing.T) {
		png := "data:image/png;base64,iVBORw0KGgo="
		out := r("![tiny](" + png + ") ![far](https://x.example/a.png)")
		has(t, out, `<img src="`+png+`" alt="tiny">`, `<span class="omitted">[image: far (not included)]</span>`)
		hasNot(t, out, "x.example")
	})
	t.Run("wiki links", func(t *testing.T) {
		o := HTMLOptions{WikiHref: func(s string) (string, bool) {
			if s == "Chapter 2" {
				return "#chapter-2", true
			}
			return "", false
		}}
		out := r("see [[Chapter 2]], [[Chapter 2|the next one]] and [[Nowhere]]", o)
		has(t, out, `<a class="wiki" href="#chapter-2">Chapter 2</a>`, `>the next one</a>`, `<span class="wiki">Nowhere</span>`)
	})
	t.Run("footnotes", func(t *testing.T) {
		out := r("One[^a] and two[^b].\n\n[^a]: First note.\n[^b]: Second note.\n", HTMLOptions{IDPrefix: "d3-"})
		has(t, out, `<sup class="fnref" id="d3-fnref1"><a href="#d3-fn1"`, `<li id="d3-fn2">`, "Second note.", `href="#d3-fnref2"`)
	})
	t.Run("headings shift and scene breaks", func(t *testing.T) {
		out := r("# Top\n\n## Next\n\n* * *\n\n###### Deep\n", HTMLOptions{HeadingShift: 3})
		has(t, out, "<h4>Top</h4>", "<h5>Next</h5>", "<hr class=\"scene\">", "<h6>Deep</h6>")
	})
	t.Run("lists, quotes, code, tables", func(t *testing.T) {
		out := r("- one\n- two\n\n3. three\n4. four\n\n> quoted\n\n```go\nx < y\n```\n\n| a | b |\n|:--|--:|\n| 1 | 2 |\n")
		has(t, out, "<ul>\n<li>one</li>\n<li>two</li>", `<ol start="3">`, "<blockquote>", `<pre><code class="language-go">x &lt; y`, `<th class="al">a</th>`, `<td class="ar">2</td>`)
	})
	t.Run("directive is shown as written", func(t *testing.T) {
		has(t, r(":::note\nbe *careful*\n:::\n"), "<pre class=\"source\">:::note\nbe *careful*\n:::</pre>")
	})
	t.Run("hard and soft breaks", func(t *testing.T) {
		has(t, r("a\\\nb\nc\n"), "a<br>\nb\nc")
	})
}

// visible is the text a reader sees: tags removed, entities decoded, runs of
// white space folded. The parts a reader sees but the counter does not read
// (footnote numbers, omitted-image notes, raw HTML shown as text) are removed
// first, and one line per block as SearchBody has it.
func visible(h string) string {
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?s)<sup class="fnref".*?</sup>`),
		regexp.MustCompile(`(?s)<span class="omitted">.*?</span>`),
		regexp.MustCompile(`(?s)<code class="raw">.*?</code>`),
		regexp.MustCompile(`(?s)<a class="fnback".*?</a>`),
	} {
		h = re.ReplaceAllString(h, "")
	}
	h = regexp.MustCompile(`</(p|h[1-6]|li|td|th|pre)>`).ReplaceAllString(h, "\n")
	h = regexp.MustCompile(`<br>`).ReplaceAllString(h, " ")
	h = regexp.MustCompile(`<[^>]*>`).ReplaceAllString(h, "")
	h = html.UnescapeString(h)
	var lines []string
	for _, l := range strings.Split(h, "\n") {
		if f := strings.Join(strings.Fields(l), " "); f != "" {
			lines = append(lines, f)
		}
	}
	return strings.Join(lines, "\n")
}

// Every document in the editor's corpus renders, inertly, and shows the text
// the counter and the search index read from it: nothing a reader should see
// goes missing when a custom construct (a wiki link, a footnote, a literal link)
// is rendered.
func TestRenderHTMLCoversTheCorpus(t *testing.T) {
	files, err := filepath.Glob("../editor/testdata/corpus/*.md")
	if err != nil || len(files) < 50 {
		t.Fatalf("corpus not found: %v (%d files)", err, len(files))
	}
	for _, f := range files {
		if strings.HasSuffix(f, ".golden.md") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		md := string(raw)
		t.Run(filepath.Base(f), func(t *testing.T) {
			out := RenderHTML(md, HTMLOptions{WikiHref: func(string) (string, bool) { return "#x", true }})
			assertInert(t, out)
			shown := strings.ReplaceAll(visible(out), "\n", " ")
			for _, line := range strings.Split(strings.ReplaceAll(SearchBody(md), "~~", ""), "\n") {
				line = strings.Join(strings.Fields(line), " ")
				if line == "" {
					continue
				}
				// Table cells and list items are separate blocks in the HTML but the
				// counter reads tables as source, so compare line by line where it
				// has them and word by word where the layout differs.
				if strings.Contains(shown, line) {
					continue
				}
				for _, w := range strings.Fields(line) {
					if !strings.Contains(shown, w) {
						t.Errorf("the text %q (word %q) is in the search text but not in the HTML:\n%s\n---\n%s", line, w, md, out)
						break
					}
				}
			}
		})
	}
}

func TestRenderHTMLStaysBoundedOnHostileText(t *testing.T) {
	size := 1 << 20 / 4
	rep := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	for name, md := range map[string]string{
		"brackets":  rep("[a]("),
		"stars":     rep("*_"),
		"quotes":    rep("> "),
		"private":   rep("%% a "),
		"footnotes": rep("[^x]"),
		"wiki":      rep("[[a]]"),
		"percent":   rep("%"),
	} {
		start := time.Now()
		out := RenderHTML(md, HTMLOptions{})
		d := time.Since(start)
		t.Run(name+" time", func(t *testing.T) {
			testutil.Timing(t)
			if d > 30*time.Second*timingFactor {
				t.Errorf("%s took %v", name, d)
			}
		})
		if len(out) == 0 {
			t.Errorf("%s: no output", name)
		}
	}
}

// LocalFiles lets the reading view show a project's own attachments; it is off for exports, which stay as they were.
func TestRenderHTMLLocalFiles(t *testing.T) {
	md := "![A map](/files/att_0123456789abcdefghij)\n\n![Out](/files/att_x)\n\n![Up](/files/att_0123456789abcdefghij/../../x)\n\n![Far](https://evil.example/a.png)\n"
	off := RenderHTML(md, HTMLOptions{})
	if strings.Contains(off, "<img") || strings.Contains(off, "/files/att_") {
		t.Errorf("an image appeared without LocalFiles: %s", off)
	}
	on := RenderHTML(md, HTMLOptions{LocalFiles: true})
	if !strings.Contains(on, `<img src="/files/att_0123456789abcdefghij" alt="A map" loading="lazy">`) {
		t.Errorf("the attachment image is missing: %s", on)
	}
	if strings.Count(on, "<img") != 1 || strings.Contains(on, "evil.example") && strings.Contains(on, `src="https`) {
		t.Errorf("only the one well-formed local image may become a tag: %s", on)
	}
	if !strings.Contains(on, "Far") {
		t.Errorf("the remote image lost its alt text: %s", on)
	}
}

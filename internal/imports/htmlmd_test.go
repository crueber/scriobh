package imports

import (
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/text"
)

func htmlMD(t testing.TB, src string) (string, *htmlStats) {
	t.Helper()
	s := newSink()
	st := &htmlStats{}
	if err := htmlToBlocks(s, src, htmlOpts{}, st); err != nil {
		t.Fatal(err)
	}
	return s.take(), st
}

func TestHTMLSubset(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<p>Hello <em>there</em> and <strong>you</strong>, <code>x</code>.</p>`, "Hello *there* and **you**, `x`."},
		{`<h2>Title</h2><p>Body</p>`, "## Title\n\nBody"},
		{`<ul><li>one</li><li>two<ul><li>nested</li></ul></li></ul>`, "- one\n- two\n    - nested"},
		{`<ol start="3"><li>a</li><li>b</li></ol>`, "3. a\n4. b"},
		{`<blockquote><p>Quoted</p><p>Two</p></blockquote><p>After</p>`, "> Quoted\n>\n> Two\n\nAfter"},
		{`<pre><code>a  b
c</code></pre>`, "```\na  b\nc\n```"},
		{`<p>One<br>two</p><hr><p>Three</p>`, "One\\\ntwo\n\n* * *\n\nThree"},
		{`<p>See <a href="https://example.org/a b">this</a> and <a href="javascript:alert(1)">that</a> and <a href="/relative">rel</a>.</p>`, "See [this](https://example.org/a%20b) and that and rel."},
		{`<p><img src="https://cdn.example/x.png" alt="A map"> text</p>`, `\[image: A map\] text`},
		{`<table><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>x|y</td></tr></table>`, "| A | B |\n| --- | --- |\n| 1 | x\\|y |"},
		{`<p>Before</p><script>alert(1)</script><style>p{}</style><iframe src="x">fallback</iframe><form><input value="v">Name</form><p>After</p>`, "Before\n\nAfter"},
		{`<div>bare <b>text</b></div><div>second</div>`, "bare **text**\n\nsecond"},
		{`<p>a&nbsp;&amp;&lt;b&gt;</p>`, `a \&\<b\>`},
		{"<p>  spaced \n  out  </p>", "spaced out"},
		{`<figure><img src="a.jpg" alt="Pic"><figcaption>The caption</figcaption></figure>`, "\\[image: Pic\\]\n\nThe caption"},
		{`<!-- wp:paragraph --><p>Block</p><!-- /wp:paragraph -->`, "Block"},
		{`<p>Caption text [caption id="a" width="1"]kept[/caption] [gallery ids="1,2"] done [sic]</p>`, "Caption text kept done \\[sic\\]"},
	}
	for _, c := range cases {
		got, _ := htmlMD(t, c.in)
		if strings.TrimSpace(got) != c.want {
			t.Errorf("%s\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}

// Nothing a post says becomes a Scríobh construct.
func TestHTMLCannotInject(t *testing.T) {
	src := `<p>%% secret %% [[Wiki]] <scriobh-ghost>g</scriobh-ghost> ![x](y) # not a heading</p>` +
		`<p>- not a list</p><p>1. nor this</p><p>:::directive</p><p>[^1] and [^1]: def</p>` +
		`<h3>Head %%x%% [[W]]</h3><ul><li># item %%y</li></ul>` +
		`<p><a href="https://e.com/a%%b)(x [[W]]">link %% text</a></p>` +
		`<p>---</p><p>===</p><p>    code?</p>`
	got, _ := htmlMD(t, src)
	d := text.Structure(got, text.StructOptions{})
	plain := text.PlainText(d.Nodes)
	for _, want := range []string{"%% secret %%", "[[Wiki]]", "# not a heading", "- not a list", "1. nor this", ":::directive", "---", "==="} {
		if !strings.Contains(plain, want) {
			t.Errorf("%q is not plain text in %q\nmarkdown: %q", want, plain, got)
		}
	}
	if links := text.WikiLinks(got); len(links) != 0 {
		t.Errorf("a wiki link was made: %+v\n%q", links, got)
	}
	for _, n := range d.Nodes {
		switch n.Kind {
		case text.NPara, text.NHeading, text.NList:
		default:
			t.Errorf("unexpected block kind %v in %q", n.Kind, got)
		}
	}
	if strings.Contains(text.SearchBody(got), "secret") == false {
		t.Errorf("literal %%%% text lost from the search text")
	}
	// A heading is one heading, a list one item.
	var heads, items int
	for _, n := range d.Nodes {
		if n.Kind == text.NHeading {
			heads++
		}
		if n.Kind == text.NList {
			items += len(n.Items)
		}
	}
	if heads != 1 || items != 1 {
		t.Errorf("headings %d, items %d in %q", heads, items, got)
	}
}

func TestHTMLDeepAndHuge(t *testing.T) {
	// Fifty thousand open divs and spans cost a counter, not a stack.
	src := "<p>" + strings.Repeat("<span><b>", 50_000) + "text" + strings.Repeat("</b></span>", 50_000) + "</p>"
	got, st := htmlMD(t, src)
	if !strings.Contains(got, "text") || st.deep == 0 {
		t.Errorf("deep: %q deep=%d", got[:min(len(got), 40)], st.deep)
	}
	// A million tiny runs are merged.
	many := "<p>" + strings.Repeat("<span>a</span>", 400_000) + "</p>"
	got, _ = htmlMD(t, many)
	if len(strings.TrimSpace(got)) != 400_000 {
		t.Errorf("runs: %d", len(got))
	}
}

// A picture beside a word is set in plain text: no asterisk touches a letter, so Markdown prints none.
func TestHTMLPictureBesideAWordNeverLeavesStrayAsterisks(t *testing.T) {
	stray := regexp.MustCompile(`[\pL\pN]\*|\*[\pL\pN]`)
	for _, src := range []string{`<p>Alpha<img src="https://cdn.example/x.png" alt="">APPY</p>`, `<p>Alpha <img src="https://cdn.example/x.png" alt="Pic">APPY</p>`} {
		out, _ := htmlMD(t, src)
		if stray.MatchString(out) {
			t.Errorf("stray asterisks next to a letter for %q:\n%s", src, out)
		}
		if !strings.Contains(out, "APPY") {
			t.Errorf("the word after the picture is kept: %q", out)
		}
	}
}

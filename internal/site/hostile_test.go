package site

import (
	"archive/zip"
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/site/check"
	"github.com/crueber/scriobh/internal/testutil"
)

// canaryCorpus has a secret in every place the site must not show: private
// notes (inline and block, with a footnote only they refer to), ghosted text,
// HTML comments, front matter, the alternatives annotation, image descriptions
// and wiki labels that hide a private note, and fields of the input that are
// never written (document ids). The whole output tree, the zip and the feeds
// are searched for each canary.
var canaries = []string{"CANARY_PRIVATE_INLINE", "CANARY_PRIVATE_BLOCK", "CANARY_GHOST", "CANARY_COMMENT", "CANARY_FRONT", "CANARY_ALTS",
	"CANARY_FOOTNOTE_ONLY", "CANARY_ALT_NOTE", "CANARY_WIKI_NOTE", "CANARY_SCENE_PRIVATE", "CANARY_ID_secret", "CANARY_TAGLESS"}

func canaryInput() Input {
	in := sample()
	in.Entries = []Entry{
		{ID: "doc_CANARY_ID_secret_1", Title: "Visible post", Role: RolePost, Published: day(0), Tags: []string{"Public"},
			Content: "---\ntitle: CANARY_FRONT\nsecret: CANARY_FRONT\n---\n" +
				"Open text before. %%CANARY_PRIVATE_INLINE%% open text after.\n\n" +
				"%%CANARY_PRIVATE_BLOCK%%\n\n" +
				"A ghost: <scriobh-ghost>CANARY_GHOST words</scriobh-ghost> gone.\n\n" +
				"<!-- CANARY_COMMENT -->\n\n" +
				"A private footnote reference.%%[^p]%% and a public one.[^q]\n\n" +
				"[^p]: CANARY_FOOTNOTE_ONLY\n[^q]: A fine public note.\n\n" +
				"![fine %%CANARY_ALT_NOTE%% alt](/files/att_boat) and [[Target|label %%CANARY_WIKI_NOTE%% here]].\n\n" +
				"<!-- scriobh:alts {\"v\":1,\"alts\":[{\"text\":\"CANARY_ALTS\"}]} -->\n"},
		{ID: "doc_two", Title: "Scenes", Role: RolePost, Published: day(1), Content: "Main.", Scenes: []string{"Scene one.\n\n%%CANARY_SCENE_PRIVATE%%\n\nafter."}},
	}
	return in
}

func TestNothingHiddenReachesTheSite(t *testing.T) {
	in := canaryInput()
	r := mustBuild(t, in)
	mustCheck(t, r, in.Options)
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	for _, f := range r.Files {
		w, _ := zw.Create(f.Path)
		w.Write(f.Data)
	}
	zw.Close()
	hay := map[string][]byte{}
	for _, f := range r.Files {
		hay[f.Path] = f.Data
		for _, c := range canaries {
			if strings.Contains(f.Path, c) {
				t.Errorf("canary %s in the name %s", c, f.Path)
			}
		}
	}
	for _, c := range canaries {
		for p, d := range hay {
			if bytes.Contains(d, []byte(c)) {
				t.Errorf("canary %s is in %s", c, p)
			}
		}
	}
	// Words of the visible text are there.
	page := string(hay["posts/visible-post.html"])
	for _, want := range []string{"Open text before.", "open text after.", "A fine public note."} {
		if !strings.Contains(page, want) {
			t.Errorf("visible text %q is missing", want)
		}
	}
	// The image description keeps its public words.
	if strings.Contains(page, "CANARY") || strings.Contains(string(hay["feed.xml"]), "CANARY") {
		t.Error("a canary")
	}
	// Notes only a private note refers to are not published.
	if strings.Count(page, `<li id="fn-`) != 1 {
		t.Errorf("want exactly the public footnote:\n%s", page)
	}
	// Titles of excluded documents never get here: the generator was never handed them.
	for p, d := range hay {
		if bytes.Contains(d, []byte("doc_")) {
			t.Errorf("a document id in %s", p)
		}
	}
}

func TestHostileMarkdown(t *testing.T) {
	in := sample()
	in.Options.ImageMaxWidth = 700
	hostile := strings.Join([]string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`[a](javascript:alert(1)) [b](JaVaScRiPt:alert(1)) [c](data:text/html;base64,PHNjcmlwdD4=) [d](vbscript:x) [e](/etc/passwd) [f](file:///etc/passwd)`,
		`[g](http://example.com/ "title" onmouseover="x") [h](https://ex.org/a"onclick="y) [i](https://u:p@evil.example/) [j](//evil.example/x)`,
		`[k](mailto:a@b.example) [l](mailto:a b@c.d) [m](https://example.org/a%20b)`,
		`![svg](/files/att_svg) ![data](data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=) ![remote](https://tracker.example/p.gif) ![traversal](../../etc/passwd)`,
		"<style>body{background:url(http://evil)}</style>",
		"<iframe src=\"https://evil.example\"></iframe>",
		"`</code><script>x</script>`",
		"    </pre><script>x</script>",
		"<a href=\"javascript:alert(1)\">raw anchor</a>",
		"&lt;script&gt; &#60;script&#62; &#x3c;b&#x3e;",
		"‮evil‬ \x00 \x1b[31m ￾",
		"# <script>h</script>",
		"- [ ] task\n- <b onclick=x>bold</b>",
		"> quote <img src=//evil.example/x.png>",
	}, "\n\n")
	in.Entries = []Entry{{ID: "h1", Title: `<b>"Hostile"</b> & <script>x</script> '` + "‮" + `'`, Role: RolePost, Published: day(0), Content: hostile, Tags: []string{`<script>`, `"quote"`, "a&b", "../../x", "CON"}, Category: `<i>`}}
	in.Options.Title = `Site "<name>" & co`
	in.Options.Tagline = `tag <line> "quoted"`
	in.Options.FooterText = `foot </footer><script>`
	in.Project.Author = `Au<th>or & "name"`
	r := mustBuild(t, in)
	mustCheck(t, r, in.Options)
	for _, f := range r.Files {
		if !strings.HasSuffix(f.Path, ".html") {
			continue
		}
		s := string(f.Data)
		for _, bad := range []string{"<script", `href="javascript`, `href="data:`, `href="file:`, `href="//`, "<iframe", "<style", "evil.example/x.png\"", "tracker.example"} {
			if strings.Contains(strings.ToLower(s), bad) {
				t.Errorf("%s contains %q", f.Path, bad)
			}
		}
		for _, r := range s {
			if r == 0 || r == 0x1b || r == '‮' || r == 0xFFFE {
				t.Errorf("%s holds the character %U", f.Path, r)
			}
		}
	}
	page := string(files(r)[r.Posts[0].Path])
	if strings.Contains(page, "u:p@") {
		t.Error("credentials in a link survived")
	}
}

func TestSVGAndRemoteImagesAreNeverPublished(t *testing.T) {
	in := sample()
	in.Images = map[string]Image{}
	in.Entries = []Entry{{ID: "i1", Title: "Pictures", Role: RolePost, Published: day(0), Content: "![svg](/files/att_svg)\n\n![remote](https://tracker.example/p.gif)\n\n![data](data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=)\n"}}
	r := mustBuild(t, in)
	mustCheck(t, r, in.Options)
	for _, f := range r.Files {
		if strings.HasPrefix(f.Path, "assets/img/") {
			t.Errorf("an image was published: %s", f.Path)
		}
	}
	got := map[string]bool{}
	for _, w := range r.Warnings {
		got[w.Code] = true
	}
	if !got["image_remote"] || !got["image_missing"] {
		t.Errorf("warnings: %v", got)
	}
}

func TestEveryFileNameIsSafe(t *testing.T) {
	in := sample()
	var es []Entry
	titles := []string{"CON", "con", "Nul", "AUX", "com1", "LPT9", "clock$", "....", "a.b.c", " leading", "trailing ", "tab\there", "ÀÉÎ", "ÀÉÎ", "àéî", strings.Repeat("x", 500), "日本語", "😀😀", "..", ".", "%2e%2e", "a b", "A B"}
	for i, ti := range titles {
		es = append(es, Entry{ID: fmt.Sprintf("n%d", i), Title: ti, Role: RolePost, Published: day(i), Content: "text " + ti, Tags: []string{ti, strings.ToUpper(ti)}, Category: ti})
	}
	in.Entries = es
	r := mustBuild(t, in)
	mustCheck(t, r, in.Options)
	seen := map[string]string{}
	for _, f := range r.Files {
		k := strings.ToLower(f.Path)
		if o, ok := seen[k]; ok {
			t.Errorf("%s and %s collide on a case-insensitive disk", o, f.Path)
		}
		seen[k] = f.Path
		if len(f.Path) > 220 {
			t.Errorf("long path %s", f.Path)
		}
	}
}

func TestManyPostsAndTags(t *testing.T) {
	in := sample()
	in.Options.PostsPerPage = 7
	var es []Entry
	for i := 0; i < 300; i++ {
		tags := []string{fmt.Sprintf("tag%d", i), fmt.Sprintf("group%d", i%5), "all"}
		for j := 0; j < 20; j++ { // more than the limit: trimmed
			tags = append(tags, fmt.Sprintf("extra%d-%d", i, j))
		}
		es = append(es, Entry{ID: fmt.Sprintf("m%d", i), Title: fmt.Sprintf("Post %d", i), Role: RolePost, Published: day(i % 40), Content: "Words " + strings.Repeat("w ", 50), Tags: tags})
	}
	in.Entries = es
	r := mustBuild(t, in)
	mustCheck(t, r, in.Options)
	if r.Tags < 300 {
		t.Errorf("tags: %d", r.Tags)
	}
	// "all" is on every post: 300/7 = 43 pages.
	if _, ok := files(r)["tags/all/43.html"]; !ok {
		t.Error("the large tag is not paginated into 43 pages")
	}
	if _, ok := files(r)["page/43.html"]; !ok {
		t.Error("the index is not paginated into 43 pages")
	}
}

// ---- the checker itself: each rule fails on a site that breaks it ----------------------------

func TestTheCheckerNamesEachRule(t *testing.T) {
	in := sample()
	r := mustBuild(t, in)
	base := files(r)
	o := check.Options{Base: baseOf(in.Options)}
	mutate := func(name, file, from, to string, want string) {
		t.Helper()
		m := map[string][]byte{}
		for k, v := range base {
			m[k] = v
		}
		if from == "" {
			delete(m, file)
		} else {
			s := string(m[file])
			if !strings.Contains(s, from) {
				t.Fatalf("%s: %q is not in %s", name, from, file)
			}
			m[file] = []byte(strings.Replace(s, from, to, 1))
		}
		rep := check.Site(m, o)
		for _, p := range rep.Problems {
			if strings.Contains(p, want) {
				return
			}
		}
		t.Errorf("%s: no problem mentions %q; got %v", name, want, rep.Problems)
	}
	mutate("script", "index.html", "</body>", "<script>x</script></body>", "<script> is not allowed")
	mutate("inline style", "index.html", `<main id="main">`, `<main id="main" style="color:red">`, "inline style")
	mutate("handler", "index.html", `<main id="main">`, `<main id="main" onclick="x">`, "event handler")
	mutate("broken link", "index.html", `href="posts/fourth-post.html"`, `href="posts/nope.html"`, "does not resolve")
	mutate("broken fragment", "posts/first-post.html", `href="#fn-1"`, `href="#fn-9"`, "fragment")
	mutate("external image", "posts/first-post.html", `src="../assets/img/`, `src="https://evil.example/`, "outside the site")
	mutate("javascript", "index.html", `<main id="main">`, `<main id="main"><a href="javascript:x">x</a>`, "scheme")
	mutate("duplicate id", "posts/first-post.html", `id="fn-1"`, `id="fnref-1"`, "appears twice")
	mutate("malformed", "index.html", `</main>`, ``, "not well-formed")
	mutate("no alt", "posts/first-post.html", `alt="A boat" `, ``, "no alt")
	mutate("lang", "index.html", `lang="en" `, ``, "lang")
	mutate("missing 404", "404.html", "", "", "404.html is missing")
	mutate("missing feed", "feed.xml", "", "", "feed.xml is missing")
	mutate("bad date", "feed.xml", "<pubDate>", "<pubDate>2026-01-01T00:00:00Z", "RFC 822")
	mutate("relative link in feed", "feed.xml", "<link>https://example.org/blog/posts/first-post.html</link>", "<link>posts/first-post.html</link>", "not absolute")
	mutate("cdata", "feed.xml", "<content:encoded>", "<content:encoded><![CDATA[x]]>", "CDATA")
	mutate("atom date", "atom.xml", "<updated>", "<updated>yesterday", "RFC 3339")
	mutate("sitemap relative", "sitemap.xml", "<loc>https://example.org/blog/posts/", "<loc>posts/", "absolute loc")
	mutate("css gradient", "assets/site.css", "--bg:", "x: linear-gradient(red,blue); --bg:", "gradient")
	mutate("robots", "robots.txt", "Allow: /", "Disallow: /", "disallows")
	m := map[string][]byte{}
	for k, v := range base {
		m[k] = v
	}
	m["Posts/First-Post.html"] = []byte("x")
	m["assets/CON.txt"] = []byte("x")
	m["a/../b.html"] = []byte("x")
	rep := check.Site(m, o)
	var haveCase, haveWin, haveUnsafe bool
	for _, p := range rep.Problems {
		haveCase = haveCase || strings.Contains(p, "differ only by case")
		haveWin = haveWin || strings.Contains(p, "Windows")
		haveUnsafe = haveUnsafe || strings.Contains(p, "unsafe path")
	}
	if !haveCase || !haveWin || !haveUnsafe {
		t.Errorf("name checks: %v", rep.Problems)
	}
}

func TestPerformance2000Posts150kWords(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	in := sample()
	in.Options.PostsPerPage = 20
	para := strings.Repeat("The quick brown fox jumps over the lazy dog and keeps going for a while longer. ", 3)
	var es []Entry
	for i := 0; i < 2000; i++ {
		body := fmt.Sprintf("%s\n\nA second paragraph with *emphasis*, a [link](https://example.org/%d) and a note.[^1]\n\n[^1]: Note %d.\n\n## Part\n\n%s\n", para, i, i, para)
		es = append(es, Entry{ID: fmt.Sprintf("p%d", i), Title: fmt.Sprintf("Post number %d about things", i), Role: RolePost, Published: day(i % 700), Content: body,
			Tags: []string{fmt.Sprintf("t%d", i%50), "all"}, Category: fmt.Sprintf("Part %d", i%9)})
	}
	in.Entries = es
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	start := time.Now()
	r := mustBuild(t, in)
	took := time.Since(start)
	runtime.ReadMemStats(&m1)
	total := 0
	for _, p := range r.Posts {
		total += p.Words
	}
	zstart := time.Now()
	zb, err := Zip(r.Files, r.Modified)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("2000 posts, %d words: build %v and %.0f MB allocated, %d files, %.1f MB, zip %.1f MB in %v, %d tags", total, took, float64(m1.TotalAlloc-m0.TotalAlloc)/1e6, len(r.Files), float64(r.Bytes)/1e6, float64(len(zb))/1e6, time.Since(zstart), r.Tags)
	if alloc := m1.TotalAlloc - m0.TotalAlloc; alloc > 1<<30 {
		t.Errorf("allocated %d MB", alloc>>20)
	}
	budget := 6 * time.Second
	if raceEnabled {
		budget *= 10
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > budget {
			t.Errorf("took %v, budget %v", took, budget)
		}
	})
	rep := check.Site(files(r), check.Options{Base: baseOf(in.Options)})
	if !rep.OK() {
		t.Fatalf("%v", rep.Problems[:min(10, len(rep.Problems))])
	}
}

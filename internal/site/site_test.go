package site

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/site/check"
)

// png1 is a 1x1 PNG with a tEXt chunk that says where it was taken.
func tinyPNG(comment string) []byte {
	raw, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	if comment == "" {
		return raw
	}
	// insert a tEXt chunk after IHDR (8 sig + 25 IHDR)
	chunk := textChunk("Comment", comment)
	out := append([]byte(nil), raw[:33]...)
	out = append(out, chunk...)
	return append(out, raw[33:]...)
}

func textChunk(k, v string) []byte {
	data := append([]byte(k+"\x00"), []byte(v)...)
	var b bytes.Buffer
	n := len(data)
	b.Write([]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
	b.WriteString("tEXt")
	b.Write(data)
	b.Write([]byte{0, 0, 0, 0}) // a CRC nobody checks here
	return b.Bytes()
}

func day(n int) time.Time { return time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC).AddDate(0, 0, n) }

func sample() Input {
	o := Defaults()
	o.Title = "The Salt Road"
	o.Tagline = "Notes from the coast"
	o.BaseURL = "https://example.org/blog/"
	o.PostsPerPage = 2
	e := []Entry{
		{ID: "doc_a", Title: "First post", Role: RolePost, Published: day(0), Tags: []string{"Sea", "walking"}, Category: "Part One",
			Content: "Hello **world**, a [link](https://example.net/x) and a [bad](javascript:alert(1)) one.[^1]\n\n[^1]: A note with *emphasis*.\n\n## A heading\n\nMore text.\n\n![A boat](/files/att_boat)\n\n![](/files/att_boat)\n"},
		{ID: "doc_b", Title: "Second post", Role: RolePost, Published: day(1), Tags: []string{"sea"}, Excerpt: "Chosen summary.", Content: "Second body with <script>alert(1)</script> text.\n\n| a | b |\n|---|--:|\n| 1 | 2 |\n", Scenes: []string{"A scene folded in."}},
		{ID: "doc_c", Title: "Third post", Role: RolePost, Published: day(2), Content: "Third.\n\n```\ncode & <b>\n```\n"},
		{ID: "doc_d", Title: "Fourth post", Role: RolePost, Created: day(3), Content: "Fourth.", Tags: []string{"Sea"}},
		{ID: "doc_p", Title: "About", Role: RolePage, Content: "About me.\n"},
	}
	return Input{Project: Project{ID: "prj_1", Title: "The Salt Road", Author: "Máire Ní Bhriain", Language: "en", Updated: day(10)}, Options: o, Entries: e,
		Images:   map[string]Image{"/files/att_boat": {Data: tinyPNG("gps 52.1,-9.2"), Mime: "image/png", Ext: "png", Width: 1, Height: 1}},
		CoverSVG: []byte("<?xml version=\"1.0\"?>\n<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 600 800\"><rect width=\"600\" height=\"800\" fill=\"#2F5D50\"/></svg>\n")}
}

func files(r *Result) map[string][]byte {
	m := map[string][]byte{}
	for _, f := range r.Files {
		m[f.Path] = f.Data
	}
	return m
}

func mustBuild(t testing.TB, in Input) *Result {
	t.Helper()
	r, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mustCheck(t testing.TB, r *Result, o Options) {
	t.Helper()
	rep := check.Site(files(r), check.Options{Base: baseOf(o), NoIndex: o.NoIndex})
	if !rep.OK() {
		t.Fatalf("the site does not pass its check:\n%s", strings.Join(rep.Problems, "\n"))
	}
}

func baseOf(o Options) string {
	if o.BaseURL == "" {
		return ""
	}
	u, _ := ParseBase(o.BaseURL)
	return u.String()
}

func TestSampleSitePassesItsCheck(t *testing.T) {
	in := sample()
	r := mustBuild(t, in)
	mustCheck(t, r, in.Options)
	m := files(r)
	for _, want := range []string{"index.html", "page/2.html", "posts/first-post.html", "pages/about.html", "tags/sea/index.html", "tags/walking/index.html", "categories/part-one/index.html",
		"feed.xml", "atom.xml", "sitemap.xml", "robots.txt", "404.html", "assets/site.css"} {
		if _, ok := m[want]; !ok {
			t.Errorf("missing %s; have %v", want, r.Paths())
		}
	}
	// Four posts at two a page: two pages. Tag "sea" has three posts at two a page: two pages.
	if _, ok := m["tags/sea/2.html"]; !ok {
		t.Error("the tag page is not paginated")
	}
	first := string(m["posts/first-post.html"])
	for _, want := range []string{`<h1 dir="auto">First post</h1>`, `rel="noopener noreferrer"`, `<h3 dir="auto">A heading</h3>`, `id="fn-1"`, `src="../assets/img/`, `width="1" height="1"`, `min read`, `rel="next"`} {
		if !strings.Contains(first, want) {
			t.Errorf("post page lacks %q", want)
		}
	}
	if strings.Contains(first, `href="javascript`) {
		t.Error("a javascript: link survived")
	}
	if !strings.Contains(string(m["posts/second-post.html"]), "&lt;script&gt;") {
		t.Error("raw HTML in a post is not shown as text")
	}
	if !strings.Contains(string(m["posts/second-post.html"]), `class="ar"`) {
		t.Error("table alignment is not a class")
	}
	if strings.Contains(string(m["posts/second-post.html"]), "<script") {
		t.Error("a script element")
	}
	if !strings.Contains(string(m["index.html"]), `rel="canonical" href="https://example.org/blog/"`) {
		t.Errorf("canonical link missing from the index")
	}
}

func (r *Result) Paths() []string {
	var out []string
	for _, f := range r.Files {
		out = append(out, f.Path)
	}
	return out
}

func TestDeterministic(t *testing.T) {
	a := mustBuild(t, sample())
	in := sample()
	b := mustBuild(t, in)
	if a.Hash != b.Hash {
		t.Fatal("two builds of the same input differ")
	}
	// Reordering the entries that have no order of their own (the dates decide) is the same site.
	in2 := sample()
	in2.Entries[0], in2.Entries[2] = in2.Entries[2], in2.Entries[0]
	c := mustBuild(t, in2)
	for i := range a.Posts {
		if a.Posts[i].Slug != c.Posts[i].Slug {
			t.Fatalf("slugs depend on the order of the input: %v vs %v", a.Posts[i], c.Posts[i])
		}
	}
}

func TestImagesAreStrippedAndNamedByHash(t *testing.T) {
	r := mustBuild(t, sample())
	for _, f := range r.Files {
		if strings.HasPrefix(f.Path, "assets/img/") {
			if bytes.Contains(f.Data, []byte("gps 52.1")) || bytes.Contains(f.Data, []byte("tEXt")) {
				t.Fatalf("%s still carries its metadata", f.Path)
			}
			if !strings.HasSuffix(f.Path, ".png") || len(f.Path) != len("assets/img/")+16+4 {
				t.Fatalf("the name %q is not a hash", f.Path)
			}
			return
		}
	}
	t.Fatal("no image written")
}

func TestWarnings(t *testing.T) {
	in := sample()
	r := mustBuild(t, in)
	codes := map[string]int{}
	for _, w := range r.Warnings {
		codes[w.Code]++
	}
	for _, c := range []string{"no_excerpt", "no_date", "missing_alt"} {
		if codes[c] == 0 {
			t.Errorf("no %s warning: %v", c, codes)
		}
	}
	in.Options.BaseURL = ""
	r = mustBuild(t, in)
	var got bool
	for _, w := range r.Warnings {
		got = got || w.Code == "no_base_url"
	}
	if !got {
		t.Error("no no_base_url warning")
	}
	mustCheck(t, r, in.Options)
	for _, f := range []string{"feed.xml", "atom.xml", "sitemap.xml"} {
		if _, ok := files(r)[f]; ok {
			t.Errorf("%s written without a base address", f)
		}
	}
}

func TestSlugs(t *testing.T) {
	cases := map[string]string{
		"Hello, World!":         "hello-world",
		"Scríobh & Café":        "scriobh-cafe",
		"Straße":                "strasse",
		"../../etc/passwd":      "etc-passwd",
		"CON":                   "con-post",
		"com1":                  "com1-post",
		"🙂🙂":                    "",
		"مرحبا بالعالم":         "مرحبا-بالعالم",
		"日本語のタイトル":              "日本語のタイトル",
		"  --  ":                "",
		"a/b\\c:d*e?f\"g<h>i|j": "a-b-c-d-e-f-g-h-i-j",
		"हिन्दी लेख":            "हिन्दी-लेख",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 200)
	if got := Slugify(long); len(got) > MaxSlugBytes {
		t.Errorf("slug of %d bytes", len(got))
	}
}

func TestDuplicateAndHostileSlugs(t *testing.T) {
	in := sample()
	in.Entries = []Entry{
		{ID: "d1", Title: "Same", Role: RolePost, Published: day(0), Content: "one"},
		{ID: "d2", Title: "SAME", Role: RolePost, Published: day(1), Content: "two"},
		{ID: "d3", Title: "same", Role: RolePost, Published: day(2), Content: "three"},
		{ID: "d4", Title: "🙂", Role: RolePost, Published: day(3), Content: "four"},
		{ID: "d5", Title: "x", Slug: "../../../etc/passwd", Role: RolePost, Published: day(4), Content: "five"},
		{ID: "d6", Title: "y", Slug: "SAME", Role: RolePost, Published: day(5), Content: "six"},
		{ID: "d7", Title: "z", Slug: "nul", Role: RolePost, Published: day(6), Content: "seven"},
	}
	r := mustBuild(t, in)
	mustCheck(t, r, in.Options)
	seen := map[string]bool{}
	for _, p := range r.Posts {
		if seen[strings.ToLower(p.Slug)] {
			t.Fatalf("slug %q twice", p.Slug)
		}
		seen[strings.ToLower(p.Slug)] = true
	}
	// An explicit slug wins over a generated one; the oldest generated keeps the plain slug.
	bySlug := map[string]string{}
	for _, p := range r.Posts {
		bySlug[p.Slug] = p.DocumentID
	}
	if bySlug["same"] != "d6" {
		t.Errorf("the explicit slug did not win: %v", bySlug)
	}
	if bySlug["etc-passwd"] != "d5" {
		t.Errorf("a traversal slug was not made safe: %v", bySlug)
	}
}

func TestContrastOfEveryAccent(t *testing.T) {
	for _, n := range AccentNames {
		if !AccentReadable(n) {
			t.Errorf("accent %s is not readable in both themes", n)
		}
		o := Defaults()
		o.Accent = n
		if err := o.Validate(); err != nil {
			t.Error(err)
		}
	}
	if Contrast("#000000", "#FFFFFF") < 20.9 || Contrast("#777777", "#FFFFFF") > 4.6 {
		t.Error("the contrast ratio is wrong")
	}
}

func TestOptionsPatch(t *testing.T) {
	o := Defaults()
	title := "T"
	n := 5
	o2, err := o.Apply(Patch{Title: &title, PostsPerPage: &n, Nav: &[]NavLink{{Label: "Home", URL: "https://example.org"}}})
	if err != nil || o2.Title != "T" || o2.PostsPerPage != 5 || len(o2.Nav) != 1 {
		t.Fatalf("%+v %v", o2, err)
	}
	o3, err := o2.Apply(Patch{Clear: []string{"title", "nav", "posts_per_page"}})
	if err != nil || o3.Title != "" || o3.PostsPerPage != 10 || len(o3.Nav) != 0 {
		t.Fatalf("%+v %v", o3, err)
	}
	if _, err := o.Apply(Patch{Clear: []string{"nope"}}); err == nil {
		t.Error("an unknown name was cleared")
	}
	for _, bad := range []NavLink{{"x", "javascript:alert(1)"}, {"x", "/relative"}, {"", "https://a.b"}, {"x", "https://u:p@a.b"}, {"x", "https://a b.c"}, {"x", "data:text/html,hi"}, {"x", "//evil.example"}} {
		o := Defaults()
		o.Nav = []NavLink{bad}
		if err := o.Validate(); err == nil {
			t.Errorf("nav link %+v accepted", bad)
		}
	}
	for _, bad := range []string{"ftp://x.y/", "https://", "https://x.y/?q=1", "https://x.y/#f", "https://u@x.y/", "javascript:1", "https://x y.z/", "https://ex.org/a/../b"} {
		o := Defaults()
		o.BaseURL = bad
		if err := o.Validate(); err == nil {
			t.Errorf("base URL %q accepted", bad)
		}
	}
	for _, bad := range []int{0, 101} {
		o := Defaults()
		o.PostsPerPage = bad
		if o.Validate() == nil {
			t.Errorf("posts_per_page %d accepted", bad)
		}
	}
}

func TestSubpathBaseAndStaging(t *testing.T) {
	in := sample()
	in.Options.NoIndex = true
	r := mustBuild(t, in)
	mustCheck(t, r, in.Options)
	m := files(r)
	if !strings.Contains(string(m["robots.txt"]), "Disallow: /") || strings.Contains(string(m["robots.txt"]), "Sitemap") {
		t.Errorf("robots.txt: %s", m["robots.txt"])
	}
	if _, ok := m["sitemap.xml"]; ok {
		t.Error("a staging site carries a sitemap")
	}
	if !strings.Contains(string(m["index.html"]), `content="noindex, nofollow"`) || strings.Contains(string(m["index.html"]), "canonical") {
		t.Error("staging pages must say noindex and carry no canonical link")
	}
	// The 404 page has links that work at any depth.
	nf := string(m["404.html"])
	if !strings.Contains(nf, `href="https://example.org/blog/assets/site.css"`) {
		t.Errorf("the 404 page uses relative links: %s", nf)
	}
	in.Options.BaseURL = ""
	r = mustBuild(t, in)
	if !strings.Contains(string(files(r)["404.html"]), `href="/assets/site.css"`) {
		t.Error("the 404 page without a base address should use root-relative links")
	}
}

func TestFeedsHoldAbsoluteLinksAndStableIDs(t *testing.T) {
	in := sample()
	a := mustBuild(t, in)
	in.Entries[0].Title = "Renamed first post"
	in.Entries[0].Slug = "renamed"
	b := mustBuild(t, in)
	for _, p := range []string{"urn:uuid:"} {
		if !strings.Contains(string(files(a)["feed.xml"]), p) {
			t.Error("no urn:uuid guid")
		}
	}
	idsA := allBetween(string(files(a)["feed.xml"]), "<guid isPermaLink=\"false\">", "</guid>")
	idsB := allBetween(string(files(b)["feed.xml"]), "<guid isPermaLink=\"false\">", "</guid>")
	if len(idsA) != 4 {
		t.Fatalf("guids: %v", idsA)
	}
	have := map[string]bool{}
	for _, g := range idsB {
		have[g] = true
	}
	for _, g := range idsA {
		if !have[g] {
			t.Errorf("a rename changed the guid %s", g)
		}
	}
	rss := string(files(a)["feed.xml"])
	if !strings.Contains(rss, "&lt;img src=&quot;https://example.org/blog/assets/img/") {
		t.Errorf("the feed body does not carry absolute image links:\n%s", rss)
	}
	if !strings.Contains(rss, "href=&quot;https://example.org/blog/posts/first-post.html#fn-1&quot;") {
		t.Error("the footnote link in the feed is not absolute")
	}
	in.Options.FeedContent = "excerpt"
	c := mustBuild(t, in)
	if strings.Contains(string(files(c)["feed.xml"]), "content:encoded") {
		t.Error("an excerpt feed carries full content")
	}
	mustCheck(t, c, in.Options)
}

func allBetween(s, a, b string) []string {
	var out []string
	for {
		i := strings.Index(s, a)
		if i < 0 {
			return out
		}
		s = s[i+len(a):]
		j := strings.Index(s, b)
		if j < 0 {
			return out
		}
		out = append(out, s[:j])
		s = s[j:]
	}
}

func TestRTLAndEmojiTitles(t *testing.T) {
	in := sample()
	in.Project.Language = "ar"
	in.Entries = []Entry{
		{ID: "r1", Title: "مرحبا بالعالم 🙂", Role: RolePost, Published: day(0), Content: "نص عربي.\n", Tags: []string{"عربي", "🙂", "日本"}},
		{ID: "r2", Title: "🙂", Role: RolePost, Published: day(1), Content: "emoji only title", Tags: []string{"🙂"}},
	}
	r := mustBuild(t, in)
	mustCheck(t, r, in.Options)
	idx := string(files(r)["index.html"])
	if !strings.Contains(idx, `dir="rtl"`) || !strings.Contains(idx, `lang="ar"`) {
		t.Error("no rtl direction")
	}
}

func TestEmptyAndNoPosts(t *testing.T) {
	in := sample()
	in.Entries = []Entry{{ID: "x", Title: "T", Role: RolePost, Content: ""}}
	if _, err := Build(in); err != ErrNoPosts {
		t.Errorf("got %v", err)
	}
	in.Entries = nil
	if _, err := Build(in); err != ErrNoPosts {
		t.Errorf("got %v", err)
	}
	in = sample()
	in.Entries = []Entry{{ID: "x", Title: "T", Role: RolePage, Content: "a page only"}}
	if _, err := Build(in); err != ErrNoPosts {
		t.Errorf("pages alone are not a blog: %v", err)
	}
}

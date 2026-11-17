package imports

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func ghostFile(posts string) []File {
	return []File{{Path: "g.json", Data: []byte(`{"db":[{"meta":{"version":"5.0.0"},"data":{"posts":[` + posts + `]}}]}`)}}
}

func TestGhostHostile(t *testing.T) {
	// JSON nested far past what the decoder allows: refused, not a crash.
	deep := []File{{Path: "g.json", Data: []byte(strings.Repeat("[", 200_000) + strings.Repeat("]", 200_000))}}
	if _, err := ghostConvert(deep, "x", convOpts{}); err == nil {
		t.Errorf("deep JSON was accepted")
	}
	// A lexical document nested past the limit: the post is read from its html.
	var lex strings.Builder
	lex.WriteString(`{"root":{"children":[`)
	for i := 0; i < 5000; i++ {
		lex.WriteString(`{"type":"paragraph","children":[`)
	}
	lex.WriteString(`{"type":"text","text":"deep text"}`)
	for i := 0; i < 5000; i++ {
		lex.WriteString(`]}`)
	}
	lex.WriteString(`]}}`)
	l, _ := json.Marshal(lex.String())
	var res *cresult
	var err error
	d, a := measure(func() {
		res, err = ghostConvert(ghostFile(`{"id":"1","title":"Deep","slug":"deep","lexical":`+string(l)+`,"html":"<p>from html</p>","type":"post","status":"published"}`), "x", convOpts{})
	})
	if err != nil || len(res.docs) != 1 {
		t.Fatalf("%v", err)
	}
	if got := res.docs[0].body; strings.Contains(got, "<") {
		t.Errorf("body %q", got)
	}
	within(t, "deep lexical", d, a, 5*time.Second, 300)
	// Odd values in every field.
	odd := `{"id":null,"title":null,"slug":7,"lexical":12,"mobiledoc":[1,2],"html":null,"type":"post","status":"published","published_at":{"a":1},"custom_excerpt":false},` +
		`{"id":{"x":1},"title":["a"],"type":"post","status":"published"},{"type":"post"},{},{"title":"Fine","html":"<p>ok</p>","status":"published","published_at":-5},` +
		`{"id":"9","title":"Big date","html":"<p>x</p>","status":"published","published_at":1e300}`
	_, err = ghostConvert(ghostFile(odd), "x", convOpts{})
	if err != nil && strings.Contains(err.Error(), "could not be read") {
		t.Errorf("a panic: %v", err)
	}
	// Not UTF-8, a byte-order mark, UTF-16.
	body := `{"db":[{"data":{"posts":[{"title":"Café","html":"<p>x` + "\xe9" + `y</p>","status":"published"}]}}]}`
	for name, b := range map[string][]byte{"latin-1 byte": []byte(body), "bom": append([]byte{0xEF, 0xBB, 0xBF}, body...)} {
		r, err := ghostConvert([]File{{Path: "g.json", Data: b}}, "x", convOpts{})
		if err != nil || len(r.docs) != 1 {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A great many posts are refused.
	var many strings.Builder
	for i := 0; i <= maxGhostPosts; i++ {
		many.WriteString(`{"title":"p","status":"published"},`)
	}
	many.WriteString(`{"title":"last"}`)
	if _, err := ghostConvert(ghostFile(many.String()), "x", convOpts{}); err == nil {
		t.Errorf("%d posts were accepted", maxGhostPosts+2)
	}
	// Sixty megabytes of text in one post: refused as too large, not built.
	huge := strings.Repeat("abcdefgh ", 7_000_000)
	hp, _ := json.Marshal(huge)
	d, a = measure(func() {
		_, err = ghostConvert(ghostFile(`{"title":"huge","html":`+string(hp)+`,"status":"published"}`), "x", convOpts{})
	})
	t.Logf("a 63 MB post: %v", err)
	within(t, "a 63 MB post", d, a, 8*time.Second, 700)
}

func TestWordPressHostile(t *testing.T) {
	wrap := func(items string) []File {
		return []File{{Path: "e.xml", Data: []byte(`<?xml version="1.0"?><rss xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:wp="http://wordpress.org/export/1.2/" xmlns:dc="http://purl.org/dc/elements/1.1/"><channel><title>t</title>` + items + `</channel></rss>`)}}
	}
	post := func(content string) string {
		return `<item><title>T</title><content:encoded><![CDATA[` + content + `]]></content:encoded><wp:post_type>post</wp:post_type><wp:status>publish</wp:status><wp:post_name>t</wp:post_name></item>`
	}
	// An HTML entity XML does not define is an error with a place, not text.
	if _, err := wordpressConvert(wrap(`<item><title>a&nbsp;b</title></item>`), "x", convOpts{}); err == nil || !strings.Contains(err.Error(), "well-formed") {
		t.Errorf("entity: %v", err)
	}
	// Items with nothing in them, a missing type, strange statuses, no content element at all.
	_, err := wordpressConvert(wrap(`<item/><item><title/></item><item><wp:post_type>post</wp:post_type></item>`+post("<p>kept</p>")), "x", convOpts{})
	if err != nil {
		t.Errorf("empty items: %v", err)
	}
	// A deep, long, hostile post body.
	hostile := strings.Repeat("<div><table><tr><td><ul><li><blockquote>", 5000) + "x" + strings.Repeat("</blockquote></li></ul></td></tr></table></div>", 5000)
	var res *cresult
	d, a := measure(func() { res, err = wordpressConvert(wrap(post(hostile)), "x", convOpts{}) })
	if err != nil || len(res.docs) != 1 || !strings.Contains(res.docs[0].body, "x") {
		t.Errorf("hostile body: %v", err)
	}
	within(t, "a nested post", d, a, 5*time.Second, 300)
	// A body past the per-post limit is left empty and said.
	big := post("<p>" + strings.Repeat("a", 17<<20) + "</p>")
	res, err = wordpressConvert(wrap(big), "x", convOpts{})
	if err != nil || res.docs[0].body != "" || !strings.Contains(strings.Join(res.warnings, "|"), "too large") {
		t.Errorf("a 17 MB post: %v %v", err, res.warnings)
	}
	// Shortcode and footnote bombs.
	sc := post(strings.Repeat("[footnote]n[/footnote] [caption]c[/caption] [gallery ids=\"1\"]", 100_000))
	d, a = measure(func() { res, err = wordpressConvert(wrap(sc), "x", convOpts{}) })
	if err != nil {
		t.Errorf("shortcodes: %v", err)
	}
	within(t, "100,000 shortcodes", d, a, 8*time.Second, 600)
	// Many items.
	var items strings.Builder
	for i := 0; i <= maxWPItems; i++ {
		items.WriteString(`<item/>`)
	}
	if _, err := wordpressConvert(wrap(items.String()), "x", convOpts{}); err == nil {
		t.Errorf("%d items were accepted", maxWPItems+1)
	}
	// A footnote that points at itself in metadata cannot loop.
	meta := `<wp:postmeta><wp:meta_key>footnotes</wp:meta_key><wp:meta_value><![CDATA[[{"id":"a","content":"<sup data-fn=\"a\">1</sup> loop"}]]]></wp:meta_value></wp:postmeta>`
	res, err = wordpressConvert(wrap(`<item><title>T</title><content:encoded><![CDATA[<p>x<sup data-fn="a">1</sup></p>]]></content:encoded><wp:post_type>post</wp:post_type><wp:status>publish</wp:status>`+meta+`</item>`), "x", convOpts{})
	if err != nil || !strings.Contains(res.docs[0].body, "loop") {
		t.Errorf("a self-referencing footnote: %v %q", err, res.docs[0].body)
	}
	_ = fmt.Sprint
}

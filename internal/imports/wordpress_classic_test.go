package imports

import (
	"strings"
	"testing"
)

// A classic-editor post that also holds a few block tags keeps its paragraphs:
// blank lines are paragraphs and single new lines are breaks, around and between
// the tags.
func TestWordPressClassicWithBlockTags(t *testing.T) {
	post := "Intro line one\nline two\n\n<h2>A heading</h2>\n\nPara after the heading\nwith a break\n\n<blockquote>Quoted text</blockquote>\n\n" +
		"<ul>\n<li>one\nstill one</li>\n<li>two</li>\n</ul>\n\n<picture><img src=\"a.jpg\" alt=\"pic\"></picture>\n\nEnd with <em>emphasis</em>\nand more."
	x := `<?xml version="1.0"?><rss xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:wp="http://wordpress.org/export/1.2/"><channel><title>t</title><item><title>T</title><content:encoded><![CDATA[` +
		post + `]]></content:encoded><wp:post_type>post</wp:post_type><wp:status>publish</wp:status></item></channel></rss>`
	res, err := wordpressConvert([]File{{Path: "e.xml", Data: []byte(x)}}, "x", convOpts{})
	if err != nil {
		t.Fatal(err)
	}
	want := "Intro line one\\\nline two\n\n## A heading\n\nPara after the heading\\\nwith a break\n\n> Quoted text\n\n- one still one\n- two\n\n\\[image: pic\\]\n\nEnd with *emphasis*\\\nand more.\n"
	if got := res.docs[0].body; got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
	// A Gutenberg post keeps collapsing white space.
	g := strings.Replace(x, "Intro line one\nline two", "<!-- wp:paragraph --><p>a\nb</p><!-- /wp:paragraph -->", 1)
	res, _ = wordpressConvert([]File{{Path: "e.xml", Data: []byte(g)}}, "x", convOpts{})
	if !strings.HasPrefix(res.docs[0].body, "a b\n") {
		t.Errorf("block post: %q", res.docs[0].body)
	}
}

package imports

import (
	"strings"
	"testing"
	"time"
)

// A token larger than the tokenizer's old buffer limit does not end the post.
func TestHTMLBigTokensDoNotEndThePost(t *testing.T) {
	data := strings.Repeat("A", 5<<20)
	for name, src := range map[string]string{
		"a data: image":         `<p>before</p><img src="data:image/png;base64,` + data + `" alt="pic"><p>after</p>`,
		"a long paragraph":      `<p>before</p><p>` + data + `</p><p>after</p>`,
		"a long attribute":      `<p>before</p><a href="https://e.com/` + data + `">link</a><p>after</p>`,
		"a long comment":        `<p>before</p><!-- ` + data + ` --><p>after</p>`,
		"a long script":         `<p>before</p><script>` + data + `</script><p>after</p>`,
		"a tag that never ends": `<p>before</p><p>after</p><div class="` + data,
	} {
		var got string
		var st *htmlStats
		d, a := measure(func() { got, st = htmlMD(t, src) })
		if !strings.Contains(got, "before") || (name != "a tag that never ends" && !strings.Contains(got, "after")) {
			t.Errorf("%s: the text after was lost (%d bytes of Markdown)", name, len(got))
		}
		if name == "a tag that never ends" && st.tooBig == 0 && !strings.Contains(got, "after") {
			t.Errorf("%s: lost without a count", name)
		}
		within(t, name, d, a, 3*time.Second, 200)
	}
}

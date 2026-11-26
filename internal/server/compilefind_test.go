package server

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// section is the text from the first start to the next end after it, for finding one nav or menu in a page.
func section(body, start, end string) string {
	i := strings.Index(body, start)
	if i < 0 {
		return ""
	}
	rest := body[i:]
	if j := strings.Index(rest, end); j >= 0 {
		return rest[:j]
	}
	return rest
}

// Compile is found where an editor looks for it (U4): a Compile tab in the project's top tabs, the Library card's menu with
// Compile above the export item, and the writing view's own tabs. A reviewer and a reader are not offered it, as the
// compile page is not theirs (compileui_test.go).
func TestCompileIsOfferedInTheTabsAndTheCardToEditorsOnly(t *testing.T) {
	w := newShareWorld(t, nil)
	reader := w.s.newCaller(t, "reader", ops.RoleReader)
	w.s.addMember(t, w.pid, reader.userID, ops.ProjectReader)
	const link = `href="/p/shared-book/compile"`
	for _, c := range []struct {
		who  caller
		want bool
	}{{w.owner, true}, {w.editor, true}, {w.reviewer, false}, {reader, false}} {
		for _, page := range []string{"/p/shared-book/outline", "/p/shared-book/d/" + w.doc1} {
			rec := w.s.do(c.who, "GET", page, "")
			if rec.Code != 200 {
				t.Fatalf("%s opens %s: %d", c.who.name, page, rec.Code)
			}
			body := rec.Body.String()
			tabs := section(body, `<nav class="ptabs"`, "</nav>")
			if tabs == "" {
				t.Fatalf("%s: no project tabs on %s", c.who.name, page)
			}
			if got := strings.Contains(tabs, link); got != c.want {
				t.Errorf("%s sees Compile in the top tabs of %s: %v, want %v", c.who.name, page, got, c.want)
			}
			if got := strings.Contains(section(body, `id="account"`, `</div>`), link); got != c.want {
				t.Errorf("%s sees Compile in the account menu of %s: %v, want %v", c.who.name, page, got, c.want)
			}
		}
		// The Library card's menu: Compile above the export item, and the export item is named for what it offers.
		rec := w.s.do(c.who, "GET", "/ui/projects/"+w.pid+"/menu", "")
		if rec.Code != 200 {
			t.Fatalf("%s opens the card menu: %d", c.who.name, rec.Code)
		}
		menu := rec.Body.String()
		if got := strings.Contains(menu, link); got != c.want {
			t.Errorf("%s sees Compile on the card: %v, want %v", c.who.name, got, c.want)
		}
		if strings.Contains(menu, "Export as Markdown") {
			t.Errorf("%s: the card's export item still says Markdown, but the page also offers HTML and a zip", c.who.name)
		}
		if c.want {
			at, export := strings.Index(menu, link), strings.Index(menu, "Export&hellip;")
			if export < 0 {
				export = strings.Index(menu, "Export…")
			}
			if at < 0 || export < 0 || at > export {
				t.Errorf("the card's Compile (%d) is not above Export (%d)", at, export)
			}
		}
	}
}

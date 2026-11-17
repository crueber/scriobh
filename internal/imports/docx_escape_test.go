package imports_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

// What a Word document says is text: nothing in it becomes Markdown, a private
// note, a wiki link, a ghost or a directive.
func TestDocxEscapesText(t *testing.T) {
	e := newEnv(t)
	lines := []string{"100% real %% not a note", "[[not a link]] and [^1] and ![x](y)", "<scriobh-ghost>nor a ghost</scriobh-ghost> <!-- nor a comment -->", ":::not a directive", "# not a heading", "- not a list", "1. not a list", "snake_case and _under_ and *star* & more", "a | b"}
	var ps string
	for _, l := range lines {
		ps += para("", strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(l))
	}
	d := miniDocx(t, map[string]string{"word/document.xml": body(para("Heading1", "One") + ps), "word/styles.xml": headingStyles})
	pv := e.previewDocx(d, imports.PreviewIn{})
	ap := e.applyDocx(d, pv, imports.ApplyIn{})
	got := e.docContent(ap.ProjectID, "One")
	for _, want := range []string{`100% real \%\% not a note`, `\[\[not a link\]\] and \[^1\] and !\[x\](y)`, `\<scriobh-ghost\>nor a ghost\</scriobh-ghost\> \<!-- nor a comment --\>`, `\:::not a directive`, `\# not a heading`, `\- not a list`, `1\. not a list`, `snake_case and \_under\_ and \*star\* \& more`, `a \| b`} {
		if !strings.Contains(got, want) {
			t.Errorf("text lacks %q:\n%s", want, got)
		}
	}
	// And it counts as the words it shows: no private note swallowed a line.
	if ap.Words < 25 {
		t.Errorf("words %d", ap.Words)
	}
}

package server

import "github.com/crueber/scriobh/internal/publish"

// textDir is the direction the text of a manuscript is set in, taken from its language by the rule the DOCX and
// EPUB writers use (publish.IsRTL), so the writing view, the reading view, the compile preview and a share page
// agree with the file that is made. "rtl" or "ltr"; never empty. A paragraph of the other script inside it is set by
// the browser (dir="auto" on the blocks), so a quotation in Latin inside Arabic reads correctly either way.
func textDir(language string) string {
	if publish.IsRTL(language) {
		return "rtl"
	}
	return "ltr"
}

package server

import (
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/i18n"
)

// projectTabs is what the project's tab strip needs: the project, the document a
// "Write" tab returns to, and which tab is the current page.
type projectTabs struct{ Slug, Write, Active, Role, Lang string }

// T is a message for the project menu, in the page's language (English when the page did not say).
func (t projectTabs) T(key string) string {
	if t.Lang == "" {
		return i18n.Default.T("en", key)
	}
	return i18n.Default.T(t.Lang, key)
}

// inLang gives the menu the page's language.
func inLang(lang string, t projectTabs) projectTabs { t.Lang = lang; return t }

// ptabs is the data of the project tabs and menu. A page that knows the caller's role passes it, and the menu then offers
// only what that role can use (Review to a reviewer and up, Compile to an editor and up, Share links to the owner); without
// one the menu offers everything, as before.
func ptabs(slug, docID, active string, role ...string) projectTabs {
	w := "/p/" + slug
	if docID != "" {
		w += "/d/" + docID
	}
	r := ""
	if len(role) > 0 {
		r = role[0]
	}
	return projectTabs{Slug: slug, Write: w, Active: active, Role: r}
}

// commas formats a count with thousands separators, as the editor shows it.
func commas(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// statuses are the document statuses in working order, with their words.
var statusOrder = []string{"idea", "outline", "draft", "revise", "done"}

func statusLabel(s string) string {
	switch s {
	case "idea":
		return "Idea"
	case "outline":
		return "Outline"
	case "draft":
		return "Draft"
	case "revise":
		return "Revise"
	case "done":
		return "Done"
	}
	return s
}

func docKindLabel(kind string) string {
	switch kind {
	case "part":
		return "Part"
	case "chapter":
		return "Chapter"
	case "scene":
		return "Scene"
	case "note":
		return "Note"
	case "front_matter":
		return "Front matter"
	case "back_matter":
		return "Back matter"
	}
	return "Document"
}

package server

import (
	"strings"
	"testing"
)

// The project menu offers what a role can use, where the page knows the role.
func TestTheProjectMenuOffersWhatTheRoleCanUse(t *testing.T) {
	w := newShareWorld(t, nil)
	read := "/p/shared-book/read/" + w.doc1
	has := func(c caller, link string) bool {
		return strings.Contains(w.s.do(c, "GET", read, "").Body.String(), `href="/p/shared-book/`+link+`"`)
	}
	for _, c := range []struct {
		who                  caller
		review, share, compo bool
	}{{w.owner, true, true, true}, {w.editor, true, false, true}, {w.reviewer, true, false, false}} {
		if has(c.who, "review") != c.review || has(c.who, "share") != c.share || has(c.who, "compile") != c.compo {
			t.Errorf("%s: review %v share %v compile %v", c.who.name, has(c.who, "review"), has(c.who, "share"), has(c.who, "compile"))
		}
	}
}

// Package fold is the search key of a title: text.FoldTitle, in a package of its own with no dependency on the rest of the
// application, so that the database (which registers it as a SQL function, scr_fold) can use it without importing text.
package fold

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Title is the folded key of a title or a name, as text.FoldTitle returns it: the same words, Unicode normal form C, and the full Unicode
// case fold.
func Title(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToLower(s)
	}
	// A caser keeps state, so each call makes its own: this runs in parallel
	// workers during an import.
	return norm.NFC.String(cases.Fold().String(norm.NFC.String(s)))
}

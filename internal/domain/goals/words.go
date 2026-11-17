package goals

import (
	"strings"
	"unicode"
)

// Counting words added.
//
// Credit for a document inside a session is measured against a baseline: the
// text the document had when the session first saved it. Credit is the number
// of words in the current text that the baseline does not account for, a
// multiset difference (each word the text holds more often than the baseline
// did counts once per extra occurrence), added to what was banked before, and
// it never goes down.
//
// When credited words leave the text (a cut, or a rewrite), the credit is
// banked and the baseline moves to the text as it stood just before that save.
// New writing then adds to the banked credit instead of competing with it.
//
// Against the cases a writer actually produces:
//
//   - Cut in one save, paste back in the next: the pasted text is the baseline
//     again, so it earns nothing, and the credit already earned stands. The same
//     holds for text that was typed an hour ago in this session and is cut and
//     pasted back.
//   - Delete 500 old words, write 500 new ones: the new words are not in the
//     baseline, so they count in full. A deletion never subtracts.
//   - Write 300 words, delete them, write 300 different ones: 600.
//   - Rewrite a paragraph at the same length: the new words count.
//   - Write 300 words, then delete them: the 300 stand.
//   - A word typed letter by letter across autosaves ("qu", "quick") is one word
//     in the text, so it is credited once, not once per save.
//
// Approximations, all documented because they decide whether a streak holds:
//
//   - Words are whitespace-delimited runs containing a letter or a digit, with
//     punctuation trimmed from the edges and case folded, so bolding a word or
//     fixing a comma earns nothing. They are counted on the raw Markdown, so a
//     pasted code block or %%note%% counts although the document word count
//     ignores it.
//   - A replacement by the same word (one "the" for another) earns nothing: the
//     difference is by word, not by position.
//   - Select-all and paste-over in a single save is credited as the larger of
//     the two texts' new words, not their sum, because no save sits between
//     them to bank the first. Two saves (delete, then write) are credited in full.
//   - Text that was cut before a rebase and pasted back after it is credited as
//     new: only the text as it stood just before the rebasing save is remembered.
//   - A move between documents (cut here, paste there) credits the pasted words
//     in the second document: the baselines are per document.
//   - When the text under a save is not the text this session left (another
//     member saved or restored in between), the session banks its credit and
//     measures from the text as it stands: their words earn this writer nothing.
//   - A baseline lasts one session. Cut in one session and pasted back in a
//     later one is credited as new text.
//
// Net words are separate and exact: the new document word count minus the old.

// addedWords counts the words cur holds beyond what base accounts for.
//
// Only the region where the two texts differ is tokenised: the common prefix
// and suffix are identical on both sides, so they cancel, and an ordinary edit
// of a 25,000-word document tokenises a few words, not the document.
func addedWords(base, cur string) int {
	if base == cur {
		return 0
	}
	p := commonPrefix(base, cur)
	// Do not split a word across the cut: back up to just after whitespace.
	if i := strings.LastIndexAny(base[:p], " \t\r\n"); i >= 0 {
		p = i + 1
	} else {
		p = 0
	}
	s := commonSuffix(base[p:], cur[p:])
	// Likewise at the far cut: begin the shared suffix at whitespace.
	bs, cs := len(base)-s, len(cur)-s
	for s > 0 && !isASCIISpace(base[bs]) {
		bs++
		cs++
		s--
	}
	oldMid, newMid := base[p:bs], cur[p:cs]
	counts := make(map[string]int32)
	forEachWord(oldMid, func(w string) { counts[w]-- })
	added := 0
	forEachWord(newMid, func(w string) {
		counts[w]++
		if counts[w] > 0 {
			added++
		}
	})
	return added
}

func isASCIISpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func commonPrefix(a, b string) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// commonSuffix is the length of the longest shared ending of a and b.
func commonSuffix(a, b string) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[len(a)-1-i] == b[len(b)-1-i] {
		i++
	}
	return i
}

// forEachWord calls fn with every word of s, normalised for comparison.
func forEachWord(s string, fn func(string)) {
	for _, f := range strings.FieldsFunc(s, unicode.IsSpace) {
		w := strings.TrimFunc(f, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if w == "" {
			continue
		}
		fn(strings.ToLower(w))
	}
}

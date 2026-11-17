package imports

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/ops"
)

// Tracked changes and comments of a Word file.
//
// What the importer does with them, and why:
//
//	suggest (default)  The document holds the text as its author left it, every
//	                   change rejected. Each change is proposed as a pending
//	                   suggestion (author: the Word name as a label, time: the
//	                   revision's own), so accepting them all in the review
//	                   screens gives the text the reviewer meant. Nothing the
//	                   author wrote is lost, and nothing a reviewer did is applied
//	                   without an editor's decision.
//	accept             The final view: insertions kept, deletions dropped.
//	reject             The original, and no suggestions.
//
// A suggestion is a change inside a paragraph. A change that cannot be one is
// taken as the reviewer left it (the final view) and counted: a whole
// paragraph inserted or deleted, a tracked paragraph mark, a change in a
// heading, a table, a footnote, a code paragraph; a change over a cap (300 for
// a document, 1,000 for an import); one that cannot be placed exactly once in
// the text; one the review checks would refuse to apply (a text that starts a
// heading or a list the paragraph did not have, a link it did not have ...).
// The preview and the apply make the same decision from the same function, so
// the numbers in the preview are the numbers that are written.
//
// Markers. While a paragraph is read, the start and the end of a revision and
// of a comment's range are put into its text as private-use characters, so
// that formatting, escaping and line breaking treat them as they treat the
// text around them; they are taken out again before anything is composed.
// Characters of that range in the document itself are dropped as it is read,
// so a file cannot forge one.

const (
	maxSuggestionsPerDoc    = 300
	maxSuggestionsPerImport = 1000
	maxWordComments         = 2000
	maxCommentText          = 10000
	maxQuote                = 600
	maxAnchorContext        = 60
)

// Marker kinds.
const (
	kRev = iota
	kComment
)

var (
	mkStartOpen = [2]rune{'', ''}
	mkStartEnd  = [2]rune{'', ''}
	mkEndOpen   = [2]rune{'', ''}
	mkEndClose  = [2]rune{'', ''}
)

func isMarkRune(r rune) bool { return r >= '' && r <= '' }

// mark is the text of a marker.
func mark(kind int, end bool, id int) string {
	if end {
		return string(mkEndOpen[kind]) + strconv.Itoa(id) + string(mkEndClose[kind])
	}
	return string(mkStartOpen[kind]) + strconv.Itoa(id) + string(mkStartEnd[kind])
}

// noMarks removes private-use marker characters from text read from a file.
func noMarks(s string) string {
	for _, r := range s {
		if isMarkRune(r) || isOverride(r) {
			return strings.Map(func(r rune) rune {
				if isMarkRune(r) || isOverride(r) {
					return -1
				}
				return r
			}, s)
		}
	}
	return s
}

// isOverride is a line or paragraph separator, or a bidirectional embedding,
// override or isolate control: none of them belongs in a manuscript's text, and
// each can reorder what a reader sees. Directional marks and letters stay.
func isOverride(r rune) bool {
	return r == 0x2028 || r == 0x2029 || r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069
}

type markPos struct {
	kind int
	end  bool
	id   int
	off  int // byte offset in the clean text
}

// extractMarks takes the markers out of s and says where each was.
func extractMarks(s string) (string, []markPos) {
	if !strings.ContainsAny(s, "") {
		return s, nil
	}
	var b strings.Builder
	var out []markPos
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		if r >= '' && r <= '' && (r-'')%2 == 0 {
			// an opening marker: digits, then its closing character
			j := i + w
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if c, cw := utf8.DecodeRuneInString(s[j:]); c == r+1 && j > i+w {
				id, _ := strconv.Atoi(s[i+w : j])
				kind := int(r-'') / 4
				end := (r-'')%4 == 2
				out = append(out, markPos{kind: kind, end: end, id: id, off: b.Len()})
				i = j + cw
				continue
			}
		}
		if isMarkRune(r) {
			i += w
			continue
		}
		b.WriteString(s[i : i+w])
		i += w
	}
	return b.String(), out
}

// stripMarkTokens removes whole markers (opening character, digits, closing
// character) from s.
func stripMarkTokens(s string) string {
	c, _ := extractMarks(s)
	return c
}

// widen takes in the formatting marks that stand right outside a range and
// close exactly around it ("*quiet*" for the range "quiet"), so that replacing
// the range cannot leave half a pair behind.
func widen(s string, a, b int) (int, int) {
	isMarkCh := func(c byte) bool { return c == '*' || c == '~' }
	k := 0
	for a-k > 0 && isMarkCh(s[a-k-1]) {
		k++
	}
	m := 0
	for b+m < len(s) && isMarkCh(s[b+m]) {
		m++
	}
	for n := min(k, m); n > 0; n-- {
		if s[a-n:a] == s[b:b+n] || reverseStr(s[a-n:a]) == s[b:b+n] {
			return a - n, b + n
		}
	}
	return a, b
}

func reverseStr(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

// around gives up to maxAnchorContext bytes of the same line before and after
// a range, cut at character boundaries.
func around(s string, a, b int) (before, after string) {
	lo := max(0, a-maxAnchorContext)
	if i := strings.LastIndexByte(s[:a], '\n'); i >= lo {
		lo = i + 1
	}
	for lo < a && !utf8.RuneStart(s[lo]) {
		lo++
	}
	hi := min(len(s), b+maxAnchorContext)
	if i := strings.IndexByte(s[b:], '\n'); i >= 0 && b+i < hi {
		hi = b + i
	}
	for hi > b && !utf8.RuneStart(s[hi-1]) && hi < len(s) && !utf8.RuneStart(s[hi]) {
		hi--
	}
	return s[lo:a], s[b:hi]
}

// ---- what a block carries ---------------------------------------------------------------

// revCluster is one tracked change as read: who, when.
type revCluster struct {
	author, date string
}

// blockRev is a change inside a block's text: the range of the original text it
// replaces (a == b for an insertion) and what goes there.
type blockRev struct {
	id       int
	label    string
	at       string
	a, b     int
	repl     string
	accepted bool // applied to the text: not proposed
	why      string
}

// blockComment is a comment's place in a block's text.
type blockComment struct {
	id       int
	a, b     int
	hasRange bool
}

// proposal is a suggestion to write: where it goes, by quote, and what it is.
type proposal struct {
	Label   string
	At      string
	Changes []review.ChangeIn
}

// wordComment is a comment of a Word file.
type wordComment struct {
	id     string
	Author string
	Date   string
	Text   string
	Quote  string // the text it was made on, when it has a range in a paragraph that could carry it
	Before string
	After  string
}

// trackedSet is what a Word file's changes and comments become, by the file of
// Markdown (chapter) they belong to.
type trackedSet struct {
	mode      string
	props     map[string][]proposal
	comments  map[string][]wordComment
	order     []string // files, in order
	total     int
	nComments int
	hash      string
	notes     trackedNotes
}

// trackedNotes are the numbers behind the preview's sentences.
type trackedNotes struct {
	Revisions        int // tracked changes read (a replacement is one)
	Suggestions      int
	Accepted         int // taken as the reviewer left them, in all
	InsertedParas    int
	DeletedParas     int
	ParaMarks        int
	Headings         int
	Tables           int
	Notes            int
	Code             int
	OverCap          int
	NotPlaced        int
	Refused          int
	AuthorConflicts  int      // changes of different authors close together: the later is taken as accepted
	ConflictPairs    []string // "Ann / Bo", the first few, each once
	Boundary         int      // changes taken as accepted because proposing them would not give the final text
	Moves            int
	FormatChanges    int
	CommentsRead     int
	CommentsMade     int
	CommentsAnchored int
	CommentsLost     int
	Authors          map[string]int
}

func newTracked(mode string) *trackedSet {
	return &trackedSet{mode: mode, props: map[string][]proposal{}, comments: map[string][]wordComment{}, notes: trackedNotes{Authors: map[string]int{}}}
}

// sealed fixes the hash that goes into the token.
func (t *trackedSet) seal() {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00", t.mode)
	files := append([]string(nil), t.order...)
	sort.Strings(files)
	for _, f := range files {
		fmt.Fprintf(h, "F%s\x00", f)
		for _, p := range t.props[f] {
			fmt.Fprintf(h, "S%s\x00%s\x00", p.Label, p.At)
			for _, ch := range p.Changes {
				fmt.Fprintf(h, "%q\x00%q\x00%q\x00%q\x00%s\x00", ch.Quote, ch.Before, ch.After, ch.Text, ch.Where)
			}
		}
		for _, c := range t.comments[f] {
			fmt.Fprintf(h, "C%s\x00%s\x00%q\x00%q\x00%q\x00%q\x00", c.Author, c.Date, c.Text, c.Quote, c.Before, c.After)
		}
	}
	t.hash = hex.EncodeToString(h.Sum(nil))
}

// when turns a Word date into the time a suggestion or comment is written
// with; a date that is not one, or that is absurd, is no date (the import's
// own time is used instead).
func wordTime(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 40 {
		return ""
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			if t.Year() < 1995 || t.Year() > 2200 {
				return ""
			}
			return db.Format(t)
		}
	}
	return ""
}

// ---- placing a file's changes -----------------------------------------------------------

// bodyOfText is the document's text for a chapter file: the file without its
// title heading, which becomes the title.
func bodyOfText(ft string) string {
	if strings.HasPrefix(ft, "# ") {
		if i := strings.IndexByte(ft, '\n'); i >= 0 {
			return strings.TrimLeft(ft[i+1:], "\n")
		}
		return ""
	}
	return ft
}

// changeFor makes the review change for a range of a block's text.
func changeFor(text string, r blockRev) (review.ChangeIn, bool) {
	a, b := r.a, r.b
	if a < 0 || b < a || b > len(text) {
		return review.ChangeIn{}, false
	}
	before, after := around(text, a, b)
	if a < b {
		q := text[a:b]
		if len(q) > maxQuote {
			return review.ChangeIn{}, false
		}
		return review.ChangeIn{Quote: q, Before: before, After: after, Text: r.repl}, true
	}
	// An insertion: beside the words before it, or after the words behind it.
	const k = 40
	switch {
	case before != "":
		q := before
		if len(q) > k {
			q = q[len(q)-k:]
			for len(q) > 0 && !utf8.RuneStart(q[0]) {
				q = q[1:]
			}
		}
		pre := before[:len(before)-len(q)]
		return review.ChangeIn{Quote: q, Before: pre, After: after, Where: "after", Text: r.repl}, true
	case after != "":
		q := after
		if len(q) > k {
			q = q[:k]
			for len(q) > 0 && !utf8.ValidString(q) {
				q = q[:len(q)-1]
			}
		}
		return review.ChangeIn{Quote: q, Before: before, After: after[len(q):], Where: "before", Text: r.repl}, true
	}
	return review.ChangeIn{}, false
}

// placeable says whether a change resolves to exactly one place in the body and
// would pass the checks an accepted suggestion passes.
func placeable(body string, ch review.ChangeIn) (string, bool) {
	a, b, err := alts.ResolveQuote(body, ch.Quote, ch.Before, ch.After, nil)
	if err != nil {
		return "the text it was made on is not found exactly once", false
	}
	switch ch.Where {
	case "before":
		b = a
	case "after":
		a = b
	}
	if len(ch.Text) > review.MaxSuggestionBytes {
		return "the review checks would refuse it (too much new text)", false
	}
	if a < b && body[a:b] == ch.Text && ch.Where != "before" && ch.Where != "after" {
		return "noop", false // the change puts back what was there: nothing to propose or to apply
	}
	if a == b && ch.Text == "" {
		return "noop", false
	}
	ix := alts.NewIndex(body)
	e := docs.AIEdit{Start: ix.Unit(a), End: ix.Unit(b), Expect: body[a:b], New: ch.Text}
	if err := docs.CheckEditsOnBody(body, []docs.AIEdit{e}); err != nil {
		return "the review checks would refuse it", false
	}
	return "", true
}

// resolveRevs decides, for the changes of one chapter's text, which become
// suggestions: the others are applied to the text as accepted, and the text is
// composed again, until what is left can all be placed. blocks are changed in
// place (a block's text takes an accepted change). compose makes the file's text.
func (c *docxConv) resolveRevs(blocks []mdBlock, compose func([]mdBlock) string) (text string, props []proposal) {
	count := 0
	for _, b := range blocks {
		count += len(b.revs)
	}
	text = compose(blocks)
	if count == 0 {
		return text, nil
	}
	var groups [][]revAt
	exhausted := true
	placeLeft := review.PlaceBudget // the work of the checks of step 4, for all the rounds of this chapter
	for round := 0; round < 40; round++ {
		// Changes become neighbours as others are taken as accepted: join the ones that touch again.
		merged := false
		for bi := range blocks {
			live := blocks[bi].revs[:0]
			for _, r := range blocks[bi].revs {
				if !(r.accepted && r.why == "done") {
					live = append(live, r)
				}
			}
			var pairs []string
			blocks[bi].revs, pairs = mergeTouching(live)
			for _, p := range pairs {
				c.tn.AuthorConflicts += 2 // both halves are taken as accepted
				c.notePair(p)
				merged = true
			}
		}
		if merged {
			c.applyAll(blocks)
			text = compose(blocks)
		}
		body := bodyOfText(text)
		changed := false
		accept := func(rv revAt, why string, count *int) {
			r := &blocks[rv.bi].revs[rv.ri]
			if r.accepted {
				return
			}
			r.accepted, r.why = true, why
			changed = true
			if count != nil {
				*count++
			}
		}
		// 1. each change on its own: anchored, not a no-op, small enough, placeable
		for bi := range blocks {
			for ri := range blocks[bi].revs {
				r := blocks[bi].revs[ri]
				if r.accepted {
					continue
				}
				rv := revAt{bi, ri}
				ch, ok := changeFor(blocks[bi].text, r)
				if !ok {
					accept(rv, "no anchor", &c.tn.NotPlaced)
					continue
				}
				switch why, ok2 := placeable(body, ch); {
				case ok2:
				case why == "noop":
					accept(rv, "noop", nil)
				case strings.HasPrefix(why, "the review"):
					accept(rv, why, &c.tn.Refused)
				default:
					accept(rv, why, &c.tn.NotPlaced)
				}
			}
		}
		if changed {
			c.applyAll(blocks)
			text = compose(blocks)
			continue
		}
		// 2. changes of two authors that are close cannot both be suggestions: the later is taken as accepted
		for {
			rv, with, found := authorConflicts(blocks)
			if !found {
				break
			}
			accept(rv, "author conflict", &c.tn.AuthorConflicts)
			c.notePair(blocks[rv.bi].revs[rv.ri].label + " / " + with)
		}
		if changed {
			c.applyAll(blocks)
			text = compose(blocks)
			continue
		}
		// 3. changes of one author that are close are one suggestion; each suggestion is checked whole
		groups = groupRevs(blocks)
		for len(groups) > maxSuggestionsPerDoc {
			for _, rv := range groups[len(groups)-1] {
				accept(rv, "cap", &c.tn.OverCap)
			}
			groups = groups[:len(groups)-1]
		}
		var chs [][]review.ChangeIn
		for _, g := range groups {
			cg := make([]review.ChangeIn, 0, len(g))
			for _, rv := range g {
				ch, _ := changeFor(blocks[rv.bi].text, blocks[rv.bi].revs[rv.ri])
				cg = append(cg, ch)
			}
			if why, ok := placeableGroup(body, cg); !ok {
				for _, rv := range g {
					accept(rv, why, &c.tn.Refused)
				}
			}
			chs = append(chs, cg)
		}
		if changed {
			c.applyAll(blocks)
			text = compose(blocks)
			continue
		}
		// 4. each suggestion must still be found when the others have been accepted
		// Suggestions that get in each other's way are found together (two lines of a refrain are each unique
		// only once the other is gone), so while there are few, take one and ask again: the rest may be fine.
		var unsound []int
		for gi, ok := range review.PlaceableAfterOthersWithin(body, chs, &placeLeft) {
			if !ok {
				unsound = append(unsound, gi)
			}
		}
		if len(unsound) > 0 && len(unsound) <= 4 {
			unsound = unsound[:1]
		}
		for _, gi := range unsound {
			for _, rv := range groups[gi] {
				accept(rv, "the review checks would refuse it (it cannot be found again once its neighbours are accepted)", &c.tn.Refused)
			}
		}
		if !changed {
			exhausted = false
			break
		}
		c.applyAll(blocks)
		text = compose(blocks)
	}
	if exhausted {
		// The rounds ran out with changes still being taken as accepted: what is left was never checked whole.
		// Take it all as accepted rather than propose what the review screens might not be able to apply.
		groups = nil
		for bi := range blocks {
			for ri := range blocks[bi].revs {
				if r := &blocks[bi].revs[ri]; !r.accepted {
					r.accepted, r.why = true, "rounds"
					c.tn.Refused++
				}
			}
		}
		c.applyAll(blocks)
		text = compose(blocks)
	}
	// What is left: write the proposals.
	for _, g := range groups {
		var p proposal
		for _, rv := range g {
			r := blocks[rv.bi].revs[rv.ri]
			if r.accepted {
				continue
			}
			ch, ok := changeFor(blocks[rv.bi].text, r)
			if !ok {
				continue
			}
			p.Changes = append(p.Changes, ch)
			p.Label = r.label
			if r.at != "" && (p.At == "" || r.at < p.At) {
				p.At = r.at
			}
		}
		if len(p.Changes) > 0 {
			props = append(props, p)
		}
	}
	return text, props
}

// notePair records that changes of two authors were too close, once for each pair of names, the first few.
func (c *docxConv) notePair(p string) {
	if len(c.tn.ConflictPairs) >= 5 {
		return
	}
	for _, q := range c.tn.ConflictPairs {
		if q == p {
			return
		}
	}
	c.tn.ConflictPairs = append(c.tn.ConflictPairs, p)
}

// revAt names a change: the block and its place in the block's list.
type revAt struct{ bi, ri int }

func (c *docxConv) applyAll(blocks []mdBlock) {
	for bi := range blocks {
		applyAccepted(&blocks[bi])
	}
}

// mergeTouching joins changes of one author that touch, or whose ranges meet: an insertion directly before a
// deletion (or after one) is one replacement. Changes of two authors are never one: when they only touch they stay
// two (the conflict pass takes the later as accepted); when their ranges overlap they cannot stay apart, so the
// two are taken as accepted together and reported (the pair of labels is returned).
func mergeTouching(revs []blockRev) ([]blockRev, []string) {
	if len(revs) < 2 {
		return revs, nil
	}
	var pairs []string
	sort.SliceStable(revs, func(i, j int) bool { return revs[i].a < revs[j].a })
	out := revs[:1]
	for _, r := range revs[1:] {
		p := &out[len(out)-1]
		same := p.label == r.label
		if !p.accepted && !r.accepted && r.a <= p.b && (same || r.a < p.b) {
			p.repl += r.repl
			p.b = max(p.b, r.b)
			if !same {
				p.accepted, p.why = true, "author conflict"
				pairs = append(pairs, p.label+" / "+r.label)
			}
			if p.at == "" || r.at != "" && r.at < p.at {
				p.at = r.at
			}
			continue
		}
		out = append(out, r)
	}
	return out, pairs
}

// liveItem is a change that is still to be proposed, with its place counted in characters.
type liveItem struct {
	rv      revAt
	start   int // distance from the start of the stretch of consecutive blocks
	end     int
	stretch int
}

// liveItems lists the changes not yet accepted, in the order of the text. An insertion's stored context is not cut
// at a blank line, so blocks are neighbours too: the break between two of them (and the marker of a list item)
// counts as a few characters.
func liveItems(blocks []mdBlock) []liveItem {
	var items []liveItem
	off := 0
	for bi, b := range blocks {
		if bi > 0 {
			off += 8
		}
		for ri, r := range b.revs {
			if r.accepted {
				continue
			}
			items = append(items, liveItem{rv: revAt{bi, ri}, start: off + utf8.RuneCountInString(b.text[:min(r.a, len(b.text))]), end: off + utf8.RuneCountInString(b.text[:min(r.b, len(b.text))])})
		}
		off += utf8.RuneCountInString(b.text)
	}
	return items
}

// closeWindow is how near two changes must be for their stored contexts to overlap.
const closeWindow = 2*alts.ContextRunes + 4

// authorConflicts finds changes of two different authors that are close enough to share context. A suggestion
// has one author and the review screens find a suggestion again by its context, so two such changes cannot both
// be suggestions: of each pair the later (by the time Word gave, else by place) is returned as the one to take as
// accepted, with the other's label. Callers apply it and ask again.
func authorConflicts(blocks []mdBlock) (loser revAt, with string, found bool) {
	items := liveItems(blocks)
	for i := 1; i < len(items); i++ {
		prev, it := items[i-1], items[i]
		if it.start-prev.end >= closeWindow {
			continue
		}
		p, q := blocks[prev.rv.bi].revs[prev.rv.ri], blocks[it.rv.bi].revs[it.rv.ri]
		if p.label == q.label {
			continue
		}
		if p.at != "" && q.at != "" && p.at > q.at {
			return prev.rv, q.label, true
		}
		return it.rv, p.label, true
	}
	return revAt{}, "", false
}

// groupRevs puts the live changes into suggestions: changes of one author whose stored contexts would overlap (a
// window of a few words either side) are one suggestion, so that accepting either never makes the other stale.
// Changes of consecutive list items or quote lines count as neighbours too. A suggestion holds at most 50 changes
// and 50,000 bytes of new text. A change of another author always starts a new suggestion (authorConflicts has
// taken the ones that are close).
func groupRevs(blocks []mdBlock) [][]revAt {
	items := liveItems(blocks)
	var out [][]revAt
	var cur []revAt
	bytesNew := 0
	for i, it := range items {
		if i > 0 {
			prev := items[i-1]
			r := blocks[it.rv.bi].revs[it.rv.ri]
			pr := blocks[prev.rv.bi].revs[prev.rv.ri]
			near := it.start-prev.end < closeWindow && r.label == pr.label
			if !near || len(cur) >= review.MaxChangesPerSuggestion || bytesNew+len(r.repl) > review.MaxSuggestionBytes {
				out = append(out, cur)
				cur, bytesNew = nil, 0
			}
		}
		cur = append(cur, it.rv)
		bytesNew += len(blocks[it.rv.bi].revs[it.rv.ri].repl)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// placeableGroup is placeable for the changes of one suggestion together.
func placeableGroup(body string, chs []review.ChangeIn) (string, bool) {
	if len(chs) > review.MaxChangesPerSuggestion {
		return "the review checks would refuse it (too many changes)", false
	}
	ix := alts.NewIndex(body)
	var edits []docs.AIEdit
	total := 0
	for _, ch := range chs {
		a, b, err := alts.ResolveQuote(body, ch.Quote, ch.Before, ch.After, nil)
		if err != nil {
			return "the text it was made on is not found exactly once", false
		}
		switch ch.Where {
		case "before":
			b = a
		case "after":
			a = b
		}
		total += len(ch.Text)
		edits = append(edits, docs.AIEdit{Start: ix.Unit(a), End: ix.Unit(b), Expect: body[a:b], New: ch.Text})
	}
	if total > review.MaxSuggestionBytes {
		return "the review checks would refuse it (too much new text)", false
	}
	if err := docs.CheckEditsOnBody(body, edits); err != nil {
		return "the review checks would refuse it", false
	}
	return "", true
}

// applyAccepted puts the accepted changes of a block into its text, right to
// left, and moves the ranges of the rest.
func applyAccepted(b *mdBlock) {
	var acc []int
	for i, r := range b.revs {
		if r.accepted && r.why != "done" {
			acc = append(acc, i)
		}
	}
	if len(acc) == 0 {
		return
	}
	sort.SliceStable(acc, func(i, j int) bool {
		x, y := b.revs[acc[i]], b.revs[acc[j]]
		if x.a != y.a {
			return x.a > y.a
		}
		if x.b != y.b {
			return x.b > y.b
		}
		return acc[i] > acc[j]
	})
	for _, i := range acc {
		r := b.revs[i]
		if r.a < 0 || r.b > len(b.text) || r.b < r.a {
			b.revs[i].why = "done"
			continue
		}
		delta := len(r.repl) - (r.b - r.a)
		b.text = b.text[:r.a] + r.repl + b.text[r.b:]
		for j := range b.revs {
			// a change that is at the same point as an insertion keeps its place before it when it came
			// before it in the file, and moves after it when it came after
			if j != i && b.revs[j].a >= r.b && (r.a != r.b || b.revs[j].a > r.a || j > i) {
				b.revs[j].a += delta
				b.revs[j].b += delta
			}
		}
		for j := range b.cms {
			o := &b.cms[j]
			if o.a >= r.b {
				o.a += delta
				o.b += delta
			} else if o.b > r.a {
				o.b = max(o.a, o.b+delta)
			}
		}
		b.revs[i].why = "done"
	}
}

// ---- comments.xml ---------------------------------------------------------------------------

// readWordComments reads the author, date and text of each comment, bounded.
func readWordComments(b []byte) map[string]*wordComment {
	out := map[string]*wordComment{}
	if b == nil {
		return out
	}
	dec := newDecoder(b)
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Space != nsWord || se.Name.Local != "comment" {
			continue
		}
		budget := 60000
		n, err := readSubtree(dec, se, 0, &budget)
		if err != nil {
			return out
		}
		id, ok := noteKeyZero(n.attr("id"))
		if !ok || len(out) >= maxWordComments {
			continue
		}
		var paras []string
		size := 0
		walkText(n, func(p *dnode) {
			var sb strings.Builder
			var rec func(x *dnode)
			rec = func(x *dnode) {
				for _, k := range x.kids {
					switch k.name {
					case "r", "ins", "hyperlink", "smartTag", "sdt", "sdtContent", "fldSimple":
						rec(k)
					case "t":
						sb.WriteString(noMarks(k.text))
					case "tab":
						sb.WriteByte(' ')
					case "br":
						sb.WriteByte(' ')
					}
				}
			}
			rec(p)
			t := strings.TrimSpace(sb.String())
			if t != "" && size < maxCommentText {
				paras = append(paras, t)
				size += len(t)
			}
		})
		author, _ := cleanLine(noMarks(n.attr("author")), review.MaxLabel)
		if author == "" {
			author = n.attr("initials")
		}
		date := wordTime(n.attr("date"))
		text := strings.Join(paras, "\n\n")
		if len(text) > maxCommentText {
			text = text[:maxCommentText]
			for len(text) > 0 && !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
		}
		clean, cerr := review.CleanBody(text, review.MaxBody)
		if cerr != nil || clean == "" {
			continue // an empty comment, or one that review would refuse to hold: not imported
		}
		out[id] = &wordComment{id: id, Author: author, Date: date, Text: clean}
	}
}

// noteKeyZero is noteKey that allows 0 (comment ids start there).
func noteKeyZero(raw string) (string, bool) {
	if raw == "" || len(raw) > 9 {
		return "", false
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return "", false
	}
	return strconv.Itoa(n), true
}

// walkText calls f for each w:p under n (a comment's paragraphs).
func walkText(n *dnode, f func(*dnode)) {
	for _, k := range n.kids {
		switch k.name {
		case "p":
			f(k)
		case "tbl", "tr", "tc", "sdt", "sdtContent":
			walkText(k, f)
		}
	}
}

var _ = io.EOF

// ---- reading a paragraph that holds changes or comments -------------------------------------

// revScan says what a paragraph holds.
type revScan struct {
	revs       int      // tracked insertions and deletions inside it
	markIns    bool     // its paragraph mark is a tracked insertion
	markDel    bool     // ... a tracked deletion
	fmtChanges int      // formatting changes (ignored)
	refs       []string // comment references
	ranges     bool     // comment ranges start or end here
}

// scanRev walks a paragraph's elements.
func (c *docxConv) scanRev(p *dnode) revScan {
	var sc revScan
	visits := 0
	var walk func(n *dnode, inPPr bool)
	walk = func(n *dnode, inPPr bool) {
		for _, k := range n.kids {
			if visits++; visits > 200_000 {
				return
			}
			switch k.name {
			case "ins", "del", "moveFrom", "moveTo":
				if inPPr {
					if k.name == "ins" || k.name == "moveTo" {
						sc.markIns = true
					} else {
						sc.markDel = true
					}
					continue
				}
				sc.revs++
			case "rPrChange", "pPrChange", "sectPrChange", "tblPrChange", "tcPrChange", "trPrChange", "tblGridChange", "numberingChange":
				sc.fmtChanges++
				continue
			case "commentReference":
				if id, ok := noteKeyZero(k.attr("id")); ok {
					sc.refs = append(sc.refs, id)
				}
			case "commentRangeStart", "commentRangeEnd":
				sc.ranges = true
			}
			if len(k.kids) > 0 {
				walk(k, inPPr || k.name == "pPr" || k.name == "rPr" && inPPr)
			}
		}
	}
	walk(p, false)
	return sc
}

// collectView reads a paragraph's segments in one view with the markers asked for.
func (c *docxConv) collectView(p *dnode, view int, rev, cm bool) []seg {
	saveV, saveR, saveC := c.view, c.revMarks, c.cmMarks
	saveF, saveFL := c.fields, c.fieldLink
	c.fields, c.fieldLink = nil, ""
	c.view, c.revMarks, c.cmMarks = view, rev, cm
	segs := c.collectPara(p)
	c.view, c.revMarks, c.cmMarks = saveV, saveR, saveC
	c.fields, c.fieldLink = saveF, saveFL
	return segs
}

// dualSegs reads a paragraph twice, as the original and as the final text, with
// the same markers in both. why is empty when the paragraph can carry
// suggestions; "inserted" or "deleted" when it is wholly one of those (then the
// final view is used and the change is counted).
func (c *docxConv) dualSegs(p *dnode) (segsO, segsF []seg, why string) {
	c.revBase = c.nextRev
	c.paraRevN = 0
	segsO = c.collectView(p, viewOriginal, true, true)
	used := c.paraRevN
	save := c.snapshot()
	c.second = true
	c.paraRevN = 0
	segsF = c.collectView(p, viewFinal, true, true)
	c.second = false
	c.restore(save)
	c.nextRev = c.revBase + used
	po, pf := plainOf(segsO), plainOf(segsF)
	switch {
	case po == "" && pf != "":
		return nil, nil, "inserted"
	case pf == "" && po != "":
		return nil, nil, "deleted"
	}
	return segsO, segsF, ""
}

// snapshot and restore keep the counters of reading from running twice for one paragraph.
type counters struct {
	underline, tracked, images, badLinks, tables, gapScenes, paragraphs, drawings int
	moves                                                                         int
	noteSeen                                                                      []string
}

func (c *docxConv) snapshot() counters {
	return counters{c.underline, c.tracked, c.images, c.badLinks, c.tables, c.gapScenes, c.paragraphs, c.drawings, c.tn.Moves, c.noteSeen}
}

func (c *docxConv) restore(s counters) {
	c.underline, c.tracked, c.images, c.badLinks, c.tables, c.gapScenes, c.paragraphs, c.drawings, c.tn.Moves, c.noteSeen = s.underline, s.tracked, s.images, s.badLinks, s.tables, s.gapScenes, s.paragraphs, s.drawings, s.moves, s.noteSeen
}

// markEdges splits text into the run of spaces and markers at its start, the
// middle, and the run at its end.
func markEdges(text string) (lead, core, trail string) {
	if !strings.ContainsAny(text, "") {
		core = strings.Trim(text, " ")
		return text[:len(text)-len(strings.TrimLeft(text, " "))], core, text[len(strings.TrimRight(text, " ")):]
	}
	i := 0
	for i < len(text) {
		if text[i] == ' ' {
			i++
			continue
		}
		if n := markLen(text[i:]); n > 0 {
			i += n
			continue
		}
		break
	}
	j := len(text)
	for j > i {
		if text[j-1] == ' ' {
			j--
			continue
		}
		if n := markLenBefore(text[:j]); n > 0 {
			j -= n
			continue
		}
		break
	}
	return text[:i], text[i:j], text[j:]
}

// markLen is the length of the marker at the start of s, or 0.
func markLen(s string) int {
	r, w := utf8.DecodeRuneInString(s)
	if r < '' || r > '' || (r-'')%2 != 0 {
		return 0
	}
	j := w
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if c, cw := utf8.DecodeRuneInString(s[j:]); j > w && c == r+1 {
		return j + cw
	}
	return 0
}

// markLenBefore is the length of the marker that ends s, or 0.
func markLenBefore(s string) int {
	r, w := utf8.DecodeLastRuneInString(s)
	if r < '' || r > '' || (r-'')%2 != 1 {
		return 0
	}
	j := len(s) - w
	k := j
	for k > 0 && s[k-1] >= '0' && s[k-1] <= '9' {
		k--
	}
	if k < j {
		if o, ow := utf8.DecodeLastRuneInString(s[:k]); o == r-1 {
			return len(s) - (k - ow)
		}
	}
	return 0
}

// leadMarks is the run of markers at the very start of s.
func leadMarks(s string) string {
	i := 0
	for i < len(s) {
		n := markLen(s[i:])
		if n == 0 {
			break
		}
		i += n
	}
	return s[:i]
}

// blockMarks makes the revisions and comments a paragraph carries from its two
// renderings (original and final, both with markers). The ranges in the
// original text are placed later, when the text is final (placeMarks).
func (c *docxConv) blockMarks(rendO, rendF string, scan revScan) ([]blockRev, []blockComment) {
	var revs []blockRev
	cleanF, marksF := extractMarks(rendF)
	start, end := map[int]int{}, map[int]int{}
	for _, m := range marksF {
		if m.kind != kRev {
			continue
		}
		if m.end {
			end[m.id] = m.off
		} else {
			start[m.id] = m.off
		}
	}
	_, marksO := extractMarks(rendO)
	seen := map[int]bool{}
	for _, m := range marksO {
		if m.kind != kRev || m.end || seen[m.id] {
			continue
		}
		seen[m.id] = true
		cl := c.clusters[m.id]
		fa, ok1 := start[m.id]
		fb, ok2 := end[m.id]
		if cl == nil || !ok1 || !ok2 || fb < fa {
			continue
		}
		fa, fb = widen(cleanF, fa, fb)
		revs = append(revs, blockRev{id: m.id, label: authorOrWord(cl.author), at: cl.date, repl: cleanF[fa:fb]})
	}
	var cms []blockComment
	have := map[string]bool{}
	for _, id := range scan.refs {
		if have[id] {
			continue
		}
		have[id] = true
		n, _ := strconv.Atoi(id)
		cms = append(cms, blockComment{id: n})
	}
	return revs, cms
}

// placeMarks puts the ranges of revisions and comments, from the markers in the
// paragraph's final (escaped) original text, on the blocks' lists.
func placeMarks(revs []blockRev, cms []blockComment, marks []markPos, clean string) ([]blockRev, []blockComment) {
	start, end := map[[2]int]int{}, map[[2]int]int{}
	for _, m := range marks {
		if m.end {
			end[[2]int{m.kind, m.id}] = m.off
		} else {
			start[[2]int{m.kind, m.id}] = m.off
		}
	}
	out := revs[:0]
	for _, r := range revs {
		a, ok1 := start[[2]int{kRev, r.id}]
		b, ok2 := end[[2]int{kRev, r.id}]
		if !ok1 || !ok2 || b < a {
			continue
		}
		if a < b {
			a, b = widen(clean, a, b)
		}
		r.a, r.b = a, b
		out = append(out, r)
	}
	have := map[int]int{}
	for i, cm := range cms {
		have[cm.id] = i
	}
	for key, a := range start {
		if key[0] != kComment {
			continue
		}
		b, hasEnd := end[key]
		if !hasEnd || b < a {
			b = len(clean)
		}
		if i, ok := have[key[1]]; ok {
			cms[i].a, cms[i].b, cms[i].hasRange = a, b, true
		} else {
			cms = append(cms, blockComment{id: key[1], a: a, b: b, hasRange: true})
			have[key[1]] = len(cms) - 1
		}
	}
	return out, cms
}

// ---- the files ---------------------------------------------------------------------------

// placeFile finishes one chapter's changes and comments once its text is
// composed, and returns the text.
func (c *docxConv) placeFile(path, title string, blocks []mdBlock) string {
	text, props := c.resolveRevs(blocks, func(bs []mdBlock) string { return c.compose(title, bs) })
	body := bodyOfText(text)
	var comments []wordComment
	seen := map[int]bool{}
	for _, b := range blocks {
		for _, cm := range b.cms {
			if seen[cm.id] {
				continue
			}
			wc := c.wcomments[strconv.Itoa(cm.id)]
			if wc == nil {
				continue
			}
			seen[cm.id] = true
			out := *wc
			if cm.hasRange && cm.b > cm.a && cm.b <= len(b.text) && cm.b-cm.a <= maxQuote {
				q := b.text[cm.a:cm.b]
				before, after := around(b.text, cm.a, cm.b)
				if _, _, err := alts.ResolveQuote(body, q, before, after, nil); err == nil {
					out.Quote, out.Before, out.After = q, before, after
				}
			}
			comments = append(comments, out)
			c.cmSeen[strconv.Itoa(cm.id)] = true
		}
	}
	if len(props) > 0 || len(comments) > 0 {
		c.ts.order = append(c.ts.order, path)
		c.ts.props[path] = props
		c.ts.comments[path] = comments
	}
	c.ts.total += len(props)
	c.ts.nComments += len(comments)
	return text
}

// finishTracked counts what is left and what was lost.
func (c *docxConv) finishTracked(comments int) {
	c.tn.Revisions = c.tracked
	c.tn.Suggestions = c.ts.total
	c.tn.FormatChanges = c.fmtChange
	c.tn.CommentsRead = comments
	c.tn.CommentsMade = c.ts.nComments
	for _, list := range c.ts.comments {
		for _, cm := range list {
			if cm.Quote != "" {
				c.tn.CommentsAnchored++
			}
		}
	}
	c.tn.CommentsLost = comments - c.ts.nComments
	if c.tn.CommentsLost < 0 {
		c.tn.CommentsLost = 0
	}
	accepted := c.tn.InsertedParas + c.tn.DeletedParas + c.tn.ParaMarks + c.tn.Headings + c.tn.Tables + c.tn.Notes + c.tn.Code + c.tn.OverCap + c.tn.NotPlaced + c.tn.Refused + c.tn.Boundary + c.tn.AuthorConflicts
	c.tn.Accepted = accepted
	c.ts.notes = c.tn
}

func (c *docxConv) trackedMeta() map[string]any {
	n := c.tn
	m := map[string]any{"changes_mode": c.mode}
	if c.mode != DocxSuggest {
		m["tracked_changes_taken_as"] = map[string]string{DocxAccept: "accepted (the final view)", DocxReject: "rejected (the original text)"}[c.mode]
	}
	m["tracked"] = map[string]any{"revisions": n.Revisions, "suggestions": n.Suggestions, "taken_as_accepted": n.Accepted,
		"inserted_paragraphs": n.InsertedParas, "deleted_paragraphs": n.DeletedParas, "paragraph_marks": n.ParaMarks, "in_headings": n.Headings,
		"in_tables": n.Tables, "in_footnotes": n.Notes, "in_code": n.Code, "over_cap": n.OverCap, "not_placed": n.NotPlaced, "refused_by_review_checks": n.Refused, "author_conflicts": n.AuthorConflicts, "author_conflict_pairs": n.ConflictPairs, "at_formatting_edges": n.Boundary,
		"moves": n.Moves, "formatting_changes_ignored": n.FormatChanges, "authors": n.Authors,
		"comments_read": n.CommentsRead, "comments_imported": n.CommentsMade, "comments_anchored": n.CommentsAnchored, "comments_not_imported": n.CommentsLost,
		"caps": map[string]int{"suggestions_per_document": maxSuggestionsPerDoc, "suggestions_per_import": maxSuggestionsPerImport, "comments": maxWordComments}}
	return m
}

// trackedWarnings says in words what became of the changes and comments.
func (c *docxConv) trackedWarnings(comments int) []string {
	n := c.tn
	var w []string
	add := func(k int, format string, args ...any) {
		if k > 0 {
			w = append(w, fmt.Sprintf(format, args...))
		}
	}
	switch c.mode {
	case DocxAccept:
		add(n.Revisions, "The file has %d tracked change(s). The final view was imported, as asked (insertions kept, deletions dropped); none became a suggestion.", n.Revisions)
	case DocxReject:
		add(n.Revisions, "The file has %d tracked change(s). The original text was imported, as asked (every change rejected); none became a suggestion.", n.Revisions)
	default:
		if n.Revisions > 0 {
			w = append(w, fmt.Sprintf("The file has %d tracked change(s). The text imported is the original, as its author left it with every change rejected; %d change(s) become pending suggestions by %d Word author(s) (names as labels, not accounts), dated as Word dated them. Changes of one author that are close together are one suggestion. Accepting them all in the review screens gives the reviewers' text.", n.Revisions, n.Suggestions, len(n.Authors)))
		}
		add(n.Accepted, "%d tracked change(s) could not be a suggestion and were taken as the reviewer left them (the final view): %d in whole paragraphs inserted, %d in whole paragraphs deleted (their text is not kept), %d in paragraphs whose mark changed, %d in headings, %d in tables, %d in footnotes, %d in code, %d over the caps (%d a document, %d an import), %d not found exactly once in the text, %d that the review checks would refuse, %d close to a change of another author (see below), %d at the edge of bold or italic text where proposing them would not give the reviewer's text.",
			n.Accepted, n.InsertedParas, n.DeletedParas, n.ParaMarks, n.Headings, n.Tables, n.Notes, n.Code, n.OverCap, maxSuggestionsPerDoc, maxSuggestionsPerImport, n.NotPlaced, n.Refused, n.AuthorConflicts, n.Boundary)
	}
	if c.mode == DocxSuggest && n.AuthorConflicts > 0 {
		w = append(w, fmt.Sprintf("%d tracked change(s) of different authors were too close together to be separate suggestions (a suggestion has one author, and is found again by the words around it): the later change of each pair was taken as accepted, so its author's change is in the text and is not a suggestion. Pairs: %s.", n.AuthorConflicts, strings.Join(n.ConflictPairs, "; ")))
	}
	add(n.Moves, "%d tracked move(s) are a deletion and an insertion in two places: they became two separate changes.", n.Moves)
	add(n.FormatChanges, "%d tracked formatting change(s) were ignored.", n.FormatChanges)
	if comments > 0 {
		w = append(w, fmt.Sprintf("%d Word comment(s): %d imported as comments by their Word authors (%d on the text they were made on, the rest on the document); %d not imported (in a heading, a table, a footnote, or over the limit of %d).", comments, n.CommentsMade, n.CommentsAnchored, n.CommentsLost, maxWordComments))
	}
	return w
}

// ---- writing them ---------------------------------------------------------------------------------

// fileOf is the file a node's text came from (a split scene names its file too).
func fileOf(n *pnode) string {
	if n.SourcePath != "" {
		return n.SourcePath
	}
	if i := strings.IndexByte(n.Key, '#'); i >= 0 {
		return n.Key[:i]
	}
	return n.Key
}

// countFor counts the suggestions and comments of the files the nodes come from.
func (t *trackedSet) countFor(nodes []*pnode) (suggestions, comments int) {
	if t == nil {
		return 0, 0
	}
	seen := map[string]bool{}
	for _, n := range nodes {
		f := fileOf(n)
		if seen[f] {
			continue
		}
		seen[f] = true
		suggestions += len(t.props[f])
		comments += len(t.comments[f])
	}
	return
}

// clampTime keeps a Word date that is in the past, and otherwise is now.
func clampTime(at string) string {
	if at == "" {
		return ""
	}
	t, err := db.Parse(at)
	if err != nil || t.After(db.NowTime()) {
		return ""
	}
	return at
}

// writeTracked writes the suggestions and comments of a Word file in the
// import's transaction, after its documents. A suggestion is placed against the
// text as stored; one that is not found exactly once or fails the review checks
// there (which the conversion has already answered for the same text) is
// counted and said, never half written. The project's own limit on open
// suggestions refuses the whole import before anything is written: nothing a
// reviewer proposed is dropped to make room.
func writeTracked(c *ops.Ctx, tx *sql.Tx, projectID string, ts *trackedSet, nodes []*pnode, ids map[string]string, out *ApplyOut) error {
	if ts == nil || ts.total+ts.nComments == 0 {
		return nil
	}
	byFile := map[string][]*pnode{}
	for _, n := range nodes {
		f := fileOf(n)
		byFile[f] = append(byFile[f], n)
	}
	need := 0
	for _, path := range ts.order {
		if len(byFile[path]) > 0 {
			need += len(ts.props[path])
		}
	}
	if need > 0 {
		open, err := review.OpenSuggestions(tx, projectID)
		if err != nil {
			return err
		}
		if open+need > review.MaxOpenSuggestionsPerProj {
			return ops.Conflict(fmt.Sprintf("This import would add %d suggestions to the %d this project already holds open, over the limit of %d. Decide some first, import into a new project, or import with docx_changes accept or reject.", need, open, review.MaxOpenSuggestionsPerProj))
		}
	}
	lost, lostComments := 0, 0
	for _, path := range ts.order {
		cands := byFile[path]
		if len(cands) == 0 {
			continue
		}
		for _, p := range ts.props[path] {
			placed := false
			for _, n := range cands {
				err := review.ImportSuggestionTx(c, tx, review.ImportedSuggestion{ProjectID: projectID, DocumentID: ids[n.Key], Label: p.Label, CreatedAt: clampTime(p.At), Changes: p.Changes})
				if err == nil {
					placed = true
					out.Suggestions++
					break
				}
				if !errors.Is(err, review.ErrNotPlaced) {
					return err
				}
			}
			if !placed {
				lost++
			}
		}
		for _, cm := range ts.comments[path] {
			placed := false
			for _, n := range cands {
				_, err := review.ImportCommentTx(c, tx, review.ImportedComment{ProjectID: projectID, DocumentID: ids[n.Key], Label: cm.Author, Body: cm.Text, CreatedAt: clampTime(cm.Date),
					Quote: cm.Quote, Before: cm.Before, After: cm.After, RequireAnchor: len(cands) > 1})
				if err == nil {
					placed = true
					out.Comments++
					break
				}
				var oe *ops.Error
				if errors.Is(err, review.ErrNotPlaced) {
					continue
				}
				if errors.As(err, &oe) && (oe.Code == ops.CodeConflict || oe.Code == ops.CodeInvalid) {
					break
				}
				return err
			}
			if !placed {
				lostComments++
			}
		}
	}
	if lost > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d tracked change(s) could not be placed in the stored text and were not kept.", lost))
	}
	if lostComments > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d Word comment(s) could not be written (an empty comment, or the project's limit) and were not kept.", lostComments))
	}
	return nil
}

// applyAll puts every change into text, right to left.
func applyAll(text string, revs []blockRev) string {
	// Right to left; at one place the wider range first, and of two insertions the later first, so that the
	// text goes in in the order it was written.
	idx := make([]int, len(revs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool {
		x, y := revs[idx[i]], revs[idx[j]]
		if x.a != y.a {
			return x.a > y.a
		}
		if x.b != y.b {
			return x.b > y.b
		}
		return idx[i] > idx[j]
	})
	for _, k := range idx {
		r := revs[k]
		if r.a < 0 || r.b < r.a || r.b > len(text) {
			return text + "\x00bad"
		}
		text = text[:r.a] + r.repl + text[r.b:]
	}
	return text
}

// fixMarkSegs gives each marker the formatting of the text it touches, so that
// markers never cut a bold or italic span in two: a start marker takes the
// formatting of the text after it, an end marker of the text before it, and a
// start and an end with nothing between them those of the text before.
func fixMarkSegs(segs []seg) []seg {
	isMark := func(s seg) bool { return s.text != "" && s.note == "" && !s.brk && stripMarkTokens(s.text) == "" }
	for i := 0; i < len(segs); {
		if !isMark(segs[i]) {
			i++
			continue
		}
		j := i
		starts, ends := false, false
		for j < len(segs) && isMark(segs[j]) {
			r, _ := utf8.DecodeRuneInString(segs[j].text)
			if (r-'\uE000')%4 == 2 {
				ends = true
			} else {
				starts = true
			}
			j++
		}
		var prev, next *seg
		for k := i - 1; k >= 0; k-- {
			if !isMark(segs[k]) {
				prev = &segs[k]
				break
			}
		}
		if j < len(segs) {
			next = &segs[j]
		}
		src := prev
		if starts && !ends && next != nil {
			src = next
		}
		if src == nil {
			src = next
		}
		if src != nil {
			f := *src
			for k := i; k < j; k++ {
				segs[k].b, segs[k].i, segs[k].s, segs[k].code, segs[k].link = f.b, f.i, f.s, f.code, f.link
			}
		}
		i = j
	}
	return segs
}

package imports

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/crueber/scriobh/internal/ops"
)

// Keys that a draft stored before a key was renamed.
//
// Phase 2 named the file of a chapter with no title of its own for its place ("0000123-chapter-2.md"); phase 3 names it for its paragraph
// alone ("0000123-chapter.md"), so a break above it leaves the key where it was. A draft staged before the change holds the old key in its
// edits, in what the writer left out or joined, in what is set aside, in the order lists and in the state Undo goes back to. When a Word
// draft is read, a stored key that the cut no longer has is mapped to the key the same chapter has now, when there is one, and one that
// has no chapter at all is set aside (as every edit of a chapter that is not in the cut is) instead of making the preview fail.

var (
	reLegacyKey = regexp.MustCompile(`(\d{7})-(chapter|section)-\d+`)
	reHasLegacy = regexp.MustCompile(`\d{7}-(chapter|section)-\d+`)
	// A PDF or Pandoc document with no headings used to be one "00-front-matter.md"; the detector (F-1244) now makes chapters named for the
	// paragraph they start at, so a draft staged before that holds a key the cut no longer has. A scene's key ("a.md#2") is the place of its
	// heading among every line that looked like one, and a heading that is now left as text (inside a comment, F-1245) is no scene: a draft
	// staged before holds a key the cut may no longer have, in a Markdown folder, a zip, a PDF or any file split at its second-level headings.
	reStale = regexp.MustCompile(`\d{7}-(chapter|section)-\d+|00-front-matter\.md|#\d+`)
)

// keyBase is a key without the "#N" of a scene.
func keyBase(k string) string {
	if i := strings.IndexByte(k, '#'); i > 0 {
		return k[:i]
	}
	return k
}

// mapKeys rewrites every key a state holds through f. Where two keys come to one, the one already there stays.
func (st *draftState) mapKeys(f func(string) string) {
	e := &st.Edits
	pf := func(p *string) *string {
		if p == nil {
			return nil
		}
		v := f(*p)
		return &v
	}
	edit := func(ed Edit) Edit {
		if ed.MergeInto != "" {
			ed.MergeInto = f(ed.MergeInto)
		}
		ed.Parent = pf(ed.Parent)
		return ed
	}
	gone := func(g Gone) Gone {
		if g.To != "" {
			g.To = f(g.To)
		}
		return g
	}
	if e.Edits != nil {
		out := make(map[string]Edit, len(e.Edits))
		for _, k := range sortedKeys(e.Edits) {
			nk := f(k)
			if _, dup := out[nk]; !dup || nk == k {
				out[nk] = edit(e.Edits[k])
			}
		}
		e.Edits = out
	}
	if e.Gone != nil {
		out := make(map[string]Gone, len(e.Gone))
		for _, k := range sortedKeys(e.Gone) {
			nk := f(k)
			if _, dup := out[nk]; !dup || nk == k {
				out[nk] = gone(e.Gone[k])
			}
		}
		e.Gone = out
	}
	if e.Stash != nil {
		out := make(map[string]Stashed, len(e.Stash))
		for _, k := range sortedKeys(e.Stash) {
			nk := f(k)
			s := e.Stash[k]
			s.Edit = edit(s.Edit)
			if s.Gone != nil {
				g := gone(*s.Gone)
				s.Gone = &g
			}
			if _, dup := out[nk]; !dup || nk == k {
				out[nk] = s
			}
		}
		e.Stash = out
	}
	if e.Order != nil {
		out := make(map[string][]string, len(e.Order))
		for _, p := range sortedKeys(e.Order) {
			keys := make([]string, len(e.Order[p]))
			for i, k := range e.Order[p] {
				keys[i] = f(k)
			}
			np := p
			if p != "" {
				np = f(p)
			}
			if _, dup := out[np]; !dup || np == p {
				out[np] = keys
			}
		}
		e.Order = out
	}
}

// storedKeys is every key a state holds, for the check of whether the cut has them.
func (st *draftState) storedKeys() []string {
	var out []string
	e := st.Edits
	for k, ed := range e.Edits {
		out = append(out, k)
		if ed.MergeInto != "" {
			out = append(out, ed.MergeInto)
		}
		if ed.Parent != nil && *ed.Parent != "" {
			out = append(out, *ed.Parent)
		}
	}
	for k, g := range e.Gone {
		out = append(out, k)
		if g.To != "" {
			out = append(out, g.To)
		}
	}
	for p, ks := range e.Order {
		if p != "" {
			out = append(out, p)
		}
		out = append(out, ks...)
	}
	return out
}

// keyVersion marks a state whose keys are the ones the cut makes now. A state without it (a draft staged before the rename) is settled the
// first time it is read, and the next change that is saved holds the mark.
const keyVersion = 2

// cutKeys is every key the cut has with these options and none of the writer's edits (and, for a Word file, the keys of what the import
// leaves out on its own).
func cutKeys(c *ops.Ctx, dr *Draft, o DraftOptions, project ProjectEdit) (func(string) bool, bool) {
	fresh := *dr
	fresh.Edits = DraftEdits{Project: project}
	// the cut with the writer's own splitting of files into scenes in it, so that the key of a scene is checked against a cut that has it
	pv, err := cutWithSplits(c, &fresh, o, dr.Edits)
	if err != nil || pv == nil {
		return nil, false
	}
	have := map[string]bool{}
	for _, n := range pv.Nodes {
		have[n.Key] = true
	}
	for _, n := range pv.LeftOut {
		have[n.Key] = true
	}
	return func(k string) bool { return k == "" || have[k] }, true
}

// settleState maps the keys of a state that are the old name of a chapter the cut has to the new name, and sets aside the edits whose
// chapter the cut does not have.
func settleState(known func(string) bool, st *draftState) {
	remap := func(k string) string {
		if known(k) {
			return k
		}
		if cand := reLegacyKey.ReplaceAllString(k, "${1}-${2}"); cand != k && known(cand) {
			return cand
		}
		return k
	}
	st.mapKeys(remap)
	settleUnknown(known, st)
	st.Edits.KV = keyVersion
}

// normaliseKeys settles a Word draft's stored keys against the cut it has now, when one of them has the shape the old name had. A state
// that Undo goes back to is settled against the cut of its own options. It changes the draft in memory only.
func normaliseKeys(c *ops.Ctx, dr *Draft) {
	if dr.Status != DraftOpen || c.DryRun() || dr.Edits.KV >= keyVersion {
		return
	}
	old := reStale
	raw, _ := json.Marshal(dr.state())
	if !old.Match(raw) && !old.MatchString(dr.Prev) {
		dr.Edits.KV = keyVersion
		return
	}
	st := dr.state().clone()
	if known, ok := cutKeys(c, dr, dr.Options, dr.Edits.Project); ok {
		settleState(known, &st)
		dr.Options, dr.Edits = st.Options, st.Edits
	}
	if dr.Prev != "" && old.MatchString(dr.Prev) {
		var prev draftState
		if json.Unmarshal([]byte(dr.Prev), &prev) == nil {
			if known, ok := cutKeys(c, dr, prev.Options, prev.Edits.Project); ok {
				settleState(known, &prev)
				b, _ := json.Marshal(prev)
				dr.Prev = string(b)
			}
		}
	}
	dr.Edits.KV = keyVersion
}

// settleUnknown sets aside, in the stash, every edit whose chapter (or the chapter it names) is not in the cut, so a stored state the
// cut cannot hold does not make the preview fail. The edits come back when their chapter does.
func settleUnknown(known func(string) bool, st *draftState) {
	e := &st.Edits
	for _, k := range sortedKeys(e.Edits) {
		ed := e.Edits[k]
		bad := !known(k) || (ed.MergeInto != "" && !known(ed.MergeInto)) || (ed.Parent != nil && !known(*ed.Parent))
		if g, ok := e.Gone[k]; ok && g.To != "" && !known(g.To) {
			bad = true
		}
		if !bad {
			continue
		}
		title := k
		if ed.Title != nil {
			title = *ed.Title
		}
		var gp *Gone
		if g, ok := e.Gone[k]; ok {
			gg := g
			gp = &gg
			title = firstNonEmpty(g.Title, title)
		}
		if e.Stash == nil {
			e.Stash = map[string]Stashed{}
		}
		if len(e.Stash) < MaxStash {
			e.Stash[k] = Stashed{Edit: ed, Gone: gp, Title: title}
		}
		delete(e.Edits, k)
		delete(e.Gone, k)
	}
	for k := range e.Gone { // a Gone entry with no edit of its own
		if _, ok := e.Edits[k]; !ok {
			delete(e.Gone, k)
		}
	}
	for _, p := range sortedKeys(e.Order) {
		var keys []string
		for _, k := range e.Order[p] {
			if known(k) {
				keys = append(keys, k)
			}
		}
		if !known(p) || len(keys) == 0 {
			delete(e.Order, p)
			continue
		}
		e.Order[p] = keys
	}
}

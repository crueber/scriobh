package imports

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Research notes of a converted import (Scrivener's Research folder). They are
// not documents: they are written into the project's research with
// research.InsertImported, in the same transaction as the documents.

// MaxResearch is the most research notes (folders included) one import writes.
const MaxResearch = 5_000

// rplan is a research note while the plan is built.
type rplan struct {
	*rnote
	title string // after the writer's edits
	kids  []*rplan
	cut   string
}

// addResearch copies the converter's notes into the plan.
func (p *plan) addResearch(ex *extras) {
	if ex == nil {
		return
	}
	p.rByKey = map[string]*rplan{}
	skippedOver := 0
	for _, n := range ex.research {
		if len(p.rByKey) >= MaxResearch {
			skippedOver++
			continue
		}
		c := *n
		rp := &rplan{rnote: &c, title: n.title}
		// Clean to the research limits now, so the preview shows what apply writes.
		im := research.Imported{Kind: c.kind, Title: c.title, Body: c.body, SourceURL: c.sourceURL, Tags: c.tags}
		rp.cut = im.Clean()
		c.kind, rp.title, c.body, c.sourceURL, c.tags = im.Kind, im.Title, im.Body, im.SourceURL, im.Tags
		c.words = text.WordCount(c.body)
		p.rByKey[c.key] = rp
		if parent, ok := p.rByKey[c.parentKey]; ok && c.parentKey != "" && parent.kind == "folder" {
			parent.kids = append(parent.kids, rp)
		} else {
			p.rRoots = append(p.rRoots, rp)
		}
	}
	if skippedOver > 0 {
		p.warnings = append(p.warnings, fmt.Sprintf("%d research note(s) were left out: an import writes at most %d.", skippedOver, MaxResearch))
	}
	cut := 0
	for _, rp := range p.rByKey {
		if rp.cut != "" {
			cut++
		}
	}
	if cut > 0 {
		p.warnings = append(p.warnings, fmt.Sprintf("%d research note(s) were cut to the limits of a research note (1 MB of text, 20 tags).", cut))
	}
}

// flattenResearch lists the research notes that remain, parents before children.
func (p *plan) flattenResearch(skip map[string]bool) []*rplan {
	var out []*rplan
	var walk func(ns []*rplan)
	walk = func(ns []*rplan) {
		for _, n := range ns {
			if skip[n.key] {
				continue
			}
			out = append(out, n)
			walk(n.kids)
		}
	}
	walk(p.rRoots)
	return out
}

// researchNodes is the preview's view of the notes that remain.
func researchNodes(rs []*rplan) []ResearchNode {
	kept := map[string]bool{}
	out := make([]ResearchNode, 0, len(rs))
	for _, r := range rs {
		kept[r.key] = true
		parent := r.parentKey
		if !kept[parent] {
			parent = ""
		}
		out = append(out, ResearchNode{Key: r.key, ParentKey: parent, Kind: r.kind, Title: r.title, Words: r.words, Tags: r.tags, Candidate: r.candidate})
	}
	return out
}

// editResearch applies one edit to a research note. Only a new title and
// leaving it (with what is inside it) out are allowed.
func (p *plan) editResearch(i int, e Edit, skip map[string]bool) error {
	rp := p.rByKey[e.Key]
	if e.Kind != nil || e.Split != nil {
		return ops.Invalid("edits["+strconv.Itoa(i)+"].kind", "A research note keeps its kind: rename it or leave it out.")
	}
	if e.Title != nil {
		t, cut := cleanTitle(*e.Title)
		if t == "" || cut {
			return ops.Invalid(fmt.Sprintf("edits[%d].title", i), "A title is one to 300 characters.")
		}
		rp.title = t
	}
	if e.Skip {
		skip[e.Key] = true
	}
	return nil
}

// insertResearch writes the remaining research notes in the caller's transaction
// and returns how many.
func insertResearch(tx *sql.Tx, projectID, userID, now string, rs []*rplan) (int, error) {
	if len(rs) == 0 {
		return 0, nil
	}
	idx := map[string]int{}
	notes := make([]research.Imported, len(rs))
	for i, r := range rs {
		parent := -1
		if j, ok := idx[r.parentKey]; ok && r.parentKey != "" {
			parent = j
		}
		idx[r.key] = i
		notes[i] = research.Imported{Parent: parent, Kind: r.kind, Title: r.title, Body: r.body, SourceURL: r.sourceURL, Tags: r.tags}
	}
	if err := research.InsertImported(tx, projectID, userID, now, notes); err != nil {
		return 0, err
	}
	for _, n := range notes { // the pictures a note shows (stored earlier in this transaction)
		if strings.Contains(n.Body, "/files/att_") {
			if err := attachments.SyncRefs(tx, attachments.RefResearch, n.ID, attachments.OwnerProject, projectID, n.Body); err != nil {
				return 0, err
			}
		}
	}
	return len(notes), nil
}

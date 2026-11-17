package site

import (
	"fmt"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/publish"
	sitegen "github.com/crueber/scriobh/internal/site"
)

// Excluded is a document of the manuscript that the site leaves out, and why.
type Excluded struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// docRow is what the site keeps about a document beside the manuscript.
type docRow struct {
	Role      string
	Slug      string
	Pinned    string // the address first given to the document, kept
	Published string
	Excerpt   string
	Tags      []string
	UpdatedAt string
}

type selection struct {
	entries  []sitegen.Entry
	excluded []Excluded
	notes    int // documents of kind note, never published
	drafts   int // published although they are drafts
}

// statusOK says whether a document's status lets it be published: done, and
// with include_drafts also draft and revise. An idea or an outline is a plan,
// never a post.
func statusOK(status string, drafts bool) (ok, draft bool) {
	switch status {
	case "done":
		return true, false
	case "draft", "revise":
		return drafts, true
	}
	return false, false
}

// choose decides which documents are posts or pages and which fold into one.
//
//   - A document marked do not compile, or inside one, is never published:
//     leaving a chapter out of a compile leaves it out of the site.
//   - Notes (kind note) are never published. Research and the Compendium are
//     not in the source at all.
//   - Posts are the chapters (`chapters`) or the top-level documents
//     (`top_level`); a scene inside a post folds into it, after its own text,
//     with a scene break between. A scene outside any chapter is a post of its
//     own. A part is a container: its chapters are posts and the part's title is
//     their category.
//   - Front and back matter documents are pages, and what is inside one is part
//     of it.
//   - A document's own choice (role post, page or skip) wins over all of this.
//   - Only documents whose status is done are published, unless include_drafts
//     is on, which adds draft and revise.
func choose(src *export.Source, o sitegen.Options, rows map[string]docRow, created map[string]time.Time) selection {
	docs := src.Docs
	byID := make(map[string]*export.SourceDoc, len(docs))
	for i := range docs {
		byID[docs[i].ID] = &docs[i]
	}
	// flagged: the document or an ancestor says do not compile.
	flagged := map[string]int8{}
	var isFlagged func(d *export.SourceDoc, depth int) bool
	isFlagged = func(d *export.SourceDoc, depth int) bool {
		if v, ok := flagged[d.ID]; ok {
			return v < 0
		}
		flagged[d.ID] = 1
		ex := !d.Include
		if !ex && depth < 10_000 {
			if p, ok := byID[d.ParentID]; ok && p != d {
				ex = isFlagged(p, depth+1)
			}
		}
		if ex {
			flagged[d.ID] = -1
		}
		return ex
	}
	const (
		kExcluded = iota
		kPost
		kPage
		kFold
		kContainer
	)
	kind := map[string]int{}
	// blocked is a document that is not published, and so is nothing inside it:
	// the state is inherited from the parent (the documents come parents first),
	// so a done scene under a draft scene under a done chapter is out, however
	// many levels lie between it and the chapter.
	blocked := map[string]bool{}
	var sel selection
	exclude := func(d *export.SourceDoc, reason string) {
		kind[d.ID] = kExcluded
		blocked[d.ID] = true
		sel.excluded = append(sel.excluded, Excluded{ID: d.ID, Title: d.Title, Kind: d.Kind, Reason: reason})
	}
	// structural: is d a root (a post or page candidate) by itself, whatever its status?
	override := func(d *export.SourceDoc) string { return rows[d.ID].Role }
	isFrontBack := func(d *export.SourceDoc) bool { return d.Kind == "front_matter" || d.Kind == "back_matter" }
	// nearestAnchor: the closest ancestor that holds what is inside it, by the rules above.
	var nearestAnchor func(d *export.SourceDoc, depth int) *export.SourceDoc
	nearestAnchor = func(d *export.SourceDoc, depth int) *export.SourceDoc {
		for p, ok := byID[d.ParentID]; ok && p != d && depth < 10_000; p, ok = byID[p.ParentID] {
			depth++
			switch {
			case isFrontBack(p), override(p) == "post", override(p) == "page":
				return p
			case o.Posts == "chapters" && p.Kind == "chapter":
				return p
			case o.Posts == "top_level" && p.ParentID == "":
				return p
			}
		}
		return nil
	}
	// partOf is the title of the part a document sits in, when that title may be
	// published: a part that is marked do not compile, that the owner skipped or
	// whose status does not allow publishing gives its chapters no category,
	// because its title is as unpublished as its text.
	partOf := func(d *export.SourceDoc) string {
		for p, ok := byID[d.ParentID]; ok; p, ok = byID[p.ParentID] {
			if p.Kind == "part" {
				if st, _ := statusOK(p.Status, o.IncludeDrafts); !st || isFlagged(p, 0) || override(p) == "skip" || p.Note {
					return ""
				}
				return strings.TrimSpace(p.Title)
			}
			if p.ParentID == "" {
				break
			}
		}
		return ""
	}
	entryIndex := map[string]int{}
	for i := range docs {
		d := &docs[i]
		if d.Note {
			sel.notes++
			kind[d.ID] = kExcluded
			blocked[d.ID] = true
			continue
		}
		if d.ParentID != "" {
			p, ok := byID[d.ParentID]
			switch {
			case !ok: // the parent is in the trash (or gone): nothing hangs from it
				exclude(d, "its parent is in the trash")
				continue
			case blocked[p.ID]:
				exclude(d, "inside a document that is not published")
				continue
			}
		}
		if isFlagged(d, 0) {
			reason := "marked do not compile (the site leaves out what a compile leaves out)"
			if d.Include {
				reason = "inside a document marked do not compile"
			}
			exclude(d, reason)
			continue
		}
		ov := override(d)
		if ov == "skip" {
			exclude(d, "you chose to leave it out of the site")
			continue
		}
		anchor := nearestAnchor(d, 0)
		forced := ov == "post" || ov == "page"
		var k int
		switch {
		case forced:
			k = kPost
			if ov == "page" {
				k = kPage
			}
		case anchor != nil:
			k = kFold
		case isFrontBack(d):
			k = kPage
		case o.Posts == "top_level" && d.ParentID == "":
			k = kPost
		case o.Posts == "chapters" && d.Kind == "chapter":
			k = kPost
		case o.Posts == "chapters" && d.Kind == "part":
			k = kContainer
		case o.Posts == "chapters":
			k = kPost // a scene outside any chapter
		default:
			k = kFold // top_level with no anchor cannot happen; folded into nothing is excluded below
		}
		if k == kContainer {
			kind[d.ID] = kContainer // its status keeps only its title out of a category; its chapters stay
			if strings.TrimSpace(d.Content) != "" {
				sel.excluded = append(sel.excluded, Excluded{ID: d.ID, Title: d.Title, Kind: d.Kind, Reason: "a part is a container: its own text is not published (its chapters are posts, its title their category); make it a post with role post to publish it"})
			}
			continue
		}
		if ok, draft := statusOK(d.Status, o.IncludeDrafts); !ok {
			reason := "its status is " + d.Status + " (only done is published; include_drafts adds draft and revise)"
			if d.Status == "idea" || d.Status == "outline" {
				reason = "its status is " + d.Status + " (a plan, never published)"
			}
			exclude(d, reason)
			continue
		} else if draft {
			sel.drafts++
		}
		if k == kFold {
			a := anchor
			if a == nil || (kind[a.ID] != kPost && kind[a.ID] != kPage) {
				exclude(d, "inside a document that is not published")
				continue
			}
			kind[d.ID] = kFold
			ei := entryIndex[a.ID]
			sel.entries[ei].Scenes = append(sel.entries[ei].Scenes, d.Content)
			continue
		}
		kind[d.ID] = k
		role := sitegen.RolePost
		if k == kPage {
			role = sitegen.RolePage
		}
		r := rows[d.ID]
		e := sitegen.Entry{ID: d.ID, Title: d.Title, Content: d.Content, Role: role, Slug: r.Slug, Excerpt: r.Excerpt, Tags: r.Tags, Created: created[d.ID]}
		if r.Published != "" {
			if t, err := time.Parse(time.RFC3339, r.Published); err == nil {
				e.Published = t
			}
		}
		if role == sitegen.RolePost {
			e.Category = partOf(d)
		}
		entryIndex[d.ID] = len(sel.entries)
		sel.entries = append(sel.entries, e)
	}
	return sel
}

// ParsePublished reads a publication date: a date (2026-03-01) or a time
// (2026-03-01T09:30:00+01:00). It returns the time as RFC 3339 in UTC.
func ParsePublished(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, s); err != nil {
			return "", fmt.Errorf("Use a date such as 2026-03-01, or a time such as 2026-03-01T09:30:00Z.")
		}
	}
	if y := t.UTC().Year(); y < 1970 || y > 9999 {
		return "", fmt.Errorf("That year is out of range (1970 to 9999).")
	}
	return t.UTC().Format(time.RFC3339), nil
}

// cleanTags trims, de-duplicates (case-folded) and bounds a tag list.
func cleanTags(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, t := range in {
		t = publish.CleanLine(t)
		if t == "" {
			continue
		}
		if len([]rune(t)) > 40 {
			return nil, fmt.Errorf("A tag is limited to 40 characters.")
		}
		k := strings.ToLower(t)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t)
	}
	if len(out) > 10 {
		return nil, fmt.Errorf("A post may have at most 10 tags.")
	}
	return out, nil
}

package imports

import (
	"strings"
)

// What each section is (part, chapter, scene, front or back matter) and where it
// sits. The book says what it can (epub:type, the landmarks, the nesting of its
// contents); the titles say the rest; and what nothing says is a chapter. Every
// choice is a proposal: the preview shows it and the writer edits it.

// typeKind is the kind the structural types of a section say, or "".
func typeKind(types string) string {
	switch {
	case typeAny(types, "part", "volume", "division"):
		return "part"
	case typeAny(types, "chapter", "subchapter", "prologue", "epilogue", "conclusion"):
		return "chapter"
	case typeAny(types, "afterword", "appendix", "colophon", "glossary", "index", "bibliography", "endnotes", "credits", "backmatter", "keywords", "revision-history", "errata"):
		return "back_matter"
	case typeAny(types, "frontmatter", "titlepage", "halftitlepage", "copyright-page", "dedication", "epigraph", "foreword", "preface", "imprint", "contributors", "other-credits", "abstract", "preamble"):
		return "front_matter"
	case typeAny(types, "bodymatter"):
		return "chapter"
	}
	return ""
}

// classifyAndNest decides each section's kind and parent, makes the documents
// of the tree (cresult.docs) and fills the edition's chapter list, in reading order.
func classifyAndNest(secs []*esec, docs []*epubDoc, byPath map[string]int, ed *epubData, book *epubBook) []*cdoc {
	// the first file of the body, when the book's landmarks name one
	bodyStart := -1
	for _, l := range book.lands {
		if l.typ == "bodymatter" && l.path != "" {
			if di, ok := byPath[l.path]; ok {
				bodyStart = di
			}
		}
	}
	var live []*esec
	for _, s := range secs {
		if s.skipWhy == "" {
			live = append(live, s)
		}
	}
	seenChapter, afterBack := false, false
	nested := false
	for _, s := range live {
		if s.cut.listed && s.cut.depth > 0 {
			nested = true
		}
	}
	for i, s := range live {
		t := strings.TrimSpace(s.title)
		explicit := typeKind(s.cut.types)
		ack := typeAny(s.cut.types, "acknowledgments", "acknowledgements")
		switch {
		case ack && !seenChapter:
			s.kind = "front_matter"
		case ack:
			s.kind = "back_matter"
		case explicit != "":
			s.kind = explicit
		case bodyStart >= 0 && s.cut.doc < bodyStart:
			s.kind = "front_matter"
		case !seenChapter && reFrontTitle.MatchString(t):
			s.kind = "front_matter"
		case seenChapter && reBackTitle.MatchString(t):
			s.kind = "back_matter"
		case rePartTitle.MatchString(t) && s.words <= 200:
			s.kind = "part"
		case s.kind == "front_matter": // a file before the first entry of the contents: front matter unless it is plainly a chapter
			if !seenChapter && s.titleSrc == "file" && s.words > 600 && bodyStart < 0 {
				s.kind = "chapter"
			}
		default:
			s.kind = "chapter"
		}
		if s.kind == "" {
			s.kind = "chapter"
		}
		// Back matter begun by what the book or its titles say carries on to the end of the book.
		if s.kind == "back_matter" && (explicit == "back_matter" || ack || reBackTitle.MatchString(t)) {
			afterBack = true
		} else if afterBack && s.kind == "chapter" {
			s.kind = "back_matter"
		}
		if s.kind == "chapter" || s.kind == "part" {
			seenChapter = true
		}
		// An entry with entries under it and little text of its own is a part.
		if s.kind == "chapter" && s.cut.listed && i+1 < len(live) && live[i+1].cut.listed && live[i+1].cut.depth > s.cut.depth && s.words <= 60 && !typeAny(s.cut.types, "chapter") {
			s.kind = "part"
		}
	}
	// nesting
	var roots []*esec
	var stack []*esec
	var curPart *esec
	for _, s := range live {
		d := s.cut.depth
		if d > len(stack) {
			d = len(stack)
		}
		stack = stack[:d]
		var parent *esec
		for j := len(stack) - 1; j >= 0; j-- {
			if stack[j] != nil && (stack[j].kind == "part" || stack[j].kind == "chapter") {
				parent = stack[j]
				break
			}
		}
		switch s.kind {
		case "front_matter", "back_matter":
			parent, curPart = nil, nil
		case "part":
			parent, curPart = nil, s
		case "chapter":
			if parent == nil && curPart != nil && !nested { // a contents with no nesting: a part holds the chapters that follow it
				parent = curPart
			}
			if parent != nil && parent.kind == "chapter" {
				s.kind = "scene"
			}
		}
		if parent != nil && parent.kind == "scene" {
			parent = parent.parent
		}
		s.parent = parent
		if parent == nil {
			roots = append(roots, s)
		} else {
			parent.kids = append(parent.kids, s)
		}
		stack = append(stack, s)
	}
	// documents and the edition's chapter list
	var rootDocs []*cdoc
	idx := map[*esec]int{}
	var mk func(s *esec) *cdoc
	mk = func(s *esec) *cdoc {
		cd := &cdoc{kind: s.kind, title: s.title, body: s.body, source: s.source}
		s.doc = cd
		for _, k := range s.kids {
			cd.kids = append(cd.kids, mk(k))
		}
		return cd
	}
	for _, r := range roots {
		rootDocs = append(rootDocs, mk(r))
	}
	// the chapter list is in reading order: the order of live
	for i, s := range live {
		idx[s] = i
		s.chapter = i
	}
	for _, s := range live {
		pk := -1
		if s.parent != nil {
			pk = idx[s.parent]
		}
		ed.chapters = append(ed.chapters, epubChapter{cd: s.doc, parentIdx: pk, kind: s.kind, title: s.title, source: s.source, words: s.words, baseline: s.body})
	}
	return rootDocs
}

// finalizeEpub fills in what is known once the documents have paths: the chapters'
// keys and the file map's.
func finalizeEpub(ed *epubData) {
	for i := range ed.chapters {
		c := &ed.chapters[i]
		if c.cd != nil {
			c.key = c.cd.path
		}
		if c.parentIdx >= 0 && c.parentIdx < len(ed.chapters) {
			c.parentKey = ed.chapters[c.parentIdx].key
		}
	}
	for i := range ed.info.Files {
		f := &ed.info.Files[i]
		if f.chapter >= 0 && f.chapter < len(ed.chapters) {
			c := ed.chapters[f.chapter]
			f.Key, f.Kind = c.key, c.kind
		}
	}
}

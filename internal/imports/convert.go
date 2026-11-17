package imports

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/domain/projects"
)

// What the converters for Scrivener, WordPress and Ghost hand the importer.
//
// A converter reads a foreign project into a tree of documents (cdoc) and,
// beside it, research notes (rnote). toFiles turns the tree into the Scríobh
// layout the folder importer already reads (manuscript/NN-title/00-title.md,
// with a manifest that carries titles, kinds, statuses and the outline fields),
// so a converted import goes through the same preview, token, edits, caps and
// provenance as any folder. The manifest is made here, never read from the
// source: a foreign file cannot name a path, a kind or an entity.

// Layout names of the converted sources. They are chosen by the writer or
// recognised from the file's own markers; a folder is never guessed.
const (
	LayoutScrivener = "scrivener"
	LayoutWordPress = "wordpress"
	LayoutGhost     = "ghost"
)

// cdoc is one document a converter made.
type cdoc struct {
	kind, title, status string
	synopsis            string // the one-line synopsis (cut to 300 characters); the rest goes to synopsisLong
	synopsisLong        string
	storyDate           string // a date or year in the form doc.update accepts, or ""
	source              string // where it came from in the source (a binder path, a slug)
	exclude             bool   // do not compile
	body                string
	kids                []*cdoc
	path                string // the path toFiles gave it: the plan's key for the document
}

// rnote is one research note (or a folder of them) a converter made.
type rnote struct {
	key, parentKey string
	kind           string // note, folder or clipping
	title, body    string
	sourceURL      string
	tags           []string
	words          int
	candidate      string // "character" or "place": would be an entity in a Compendium
}

// cproject is what the source states about the project.
type cproject struct {
	title, subtitle, kind, author, language string
}

// cresult is everything a converter reports.
type cresult struct {
	project  cproject
	docs     []*cdoc
	research []*rnote
	layout   string
	srcHash  string
	meta     map[string]any
	warnings []string
	skipped  []Skipped
	pics     *picBox // the pictures that come with it
	srcKind  string  // the provenance kind when it is not markdown (epub)
	epub     *epubData
}

func (r *cresult) warn(format string, args ...any) {
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

// warnN adds a warning when n is not zero.
func (r *cresult) warnN(n int, format string, args ...any) {
	if n > 0 {
		r.warn(format, args...)
	}
}

const (
	// convMaxDirs is the folders a converted tree may nest: the importer refuses
	// a path deeper than MaxDepth, and manuscript/ is one of them.
	convMaxDirs = MaxDepth - 1
	// manifestBudget keeps the manifest under the importer's own limit for one
	// (MaxManifest); long synopses are dropped past it.
	manifestBudget = MaxManifest * 3 / 4
)

// manifestDoc is a document of the made manifest (the importer's own reader's
// shape, plus source_path, which only a converted import is allowed to use).
type manifestDoc struct {
	Path             string `json:"path"`
	Kind             string `json:"kind"`
	Title            string `json:"title"`
	Status           string `json:"status,omitempty"`
	SynopsisShort    string `json:"synopsis_short,omitempty"`
	SynopsisLong     string `json:"synopsis_long,omitempty"`
	StoryDate        string `json:"story_date,omitempty"`
	IncludeInCompile *bool  `json:"include_in_compile,omitempty"`
	SourcePath       string `json:"source_path,omitempty"`
}

type manifestOut struct {
	Project struct {
		Title      string `json:"title"`
		Subtitle   string `json:"subtitle,omitempty"`
		Kind       string `json:"kind,omitempty"`
		AuthorName string `json:"author_name,omitempty"`
		Language   string `json:"language,omitempty"`
	} `json:"project"`
	Documents []manifestDoc `json:"documents"`
}

// toFiles lays the tree out as the Scríobh layout: a folder for a document with
// children (its own text in 00-name.md), a file for the rest, numbered in order,
// plus the manifest. Documents past the importer's file limit are skipped and
// said so; a tree nested deeper than a path may be is flattened into its deepest
// folder, and said so.
func (r *cresult) toFiles() []File {
	var files []File
	var m manifestOut
	m.Project.Title, m.Project.Subtitle, m.Project.Kind = r.project.title, r.project.subtitle, r.project.kind
	m.Project.AuthorName, m.Project.Language = r.project.author, r.project.language
	var tooMany, flattened, dropLong, longBytes int
	var walk func(docs []*cdoc, dir string, depth int)
	walk = func(docs []*cdoc, dir string, depth int) {
		width := max(2, len(fmt.Sprint(len(docs))))
		for i, d := range docs {
			name := fmt.Sprintf("%0*d-%s", width, i+1, slugOf(d.title))
			if len(files) >= MaxMarkdown-1 {
				tooMany += 1 + countKids(d)
				continue
			}
			entry := func(path string) {
				md := manifestDoc{Path: path, Kind: d.kind, Title: d.title, Status: d.status, SynopsisShort: d.synopsis, StoryDate: d.storyDate, SourcePath: d.source}
				if d.synopsisLong != "" {
					if longBytes += len(d.synopsisLong); longBytes > manifestBudget {
						dropLong++
					} else {
						md.SynopsisLong = d.synopsisLong
					}
				}
				if d.exclude {
					f := false
					md.IncludeInCompile = &f
				}
				m.Documents = append(m.Documents, md)
				files = append(files, File{Path: path, Data: []byte(d.body)})
				d.path = path
			}
			if len(d.kids) == 0 {
				entry(dir + name + ".md")
				continue
			}
			if depth >= convMaxDirs {
				// Too deep for a path: this document and what is under it sit in the
				// folder above, in reading order.
				entry(dir + name + ".md")
				flattened += countKids(d)
				var flat []*cdoc
				var collect func(ks []*cdoc)
				collect = func(ks []*cdoc) {
					for _, k := range ks {
						flat = append(flat, &cdoc{kind: k.kind, title: k.title, status: k.status, synopsis: k.synopsis, synopsisLong: k.synopsisLong,
							storyDate: k.storyDate, source: k.source, exclude: k.exclude, body: k.body})
						collect(k.kids)
					}
				}
				collect(d.kids)
				for j, k := range flat {
					if len(files) >= MaxMarkdown-1 {
						tooMany += len(flat) - j
						break
					}
					p := fmt.Sprintf("%s%s-%0*d-%s.md", dir, name, max(2, len(fmt.Sprint(len(flat)))), j+1, slugOf(k.title))
					md := manifestDoc{Path: p, Kind: k.kind, Title: k.title, Status: k.status, SynopsisShort: k.synopsis, StoryDate: k.storyDate, SourcePath: k.source}
					if k.exclude {
						f := false
						md.IncludeInCompile = &f
					}
					m.Documents = append(m.Documents, md)
					files = append(files, File{Path: p, Data: []byte(k.body)})
					k.path = p
				}
				continue
			}
			sub := dir + name + "/"
			entry(sub + "00-" + slugOf(d.title) + ".md")
			walk(d.kids, sub, depth+1)
		}
	}
	walk(r.docs, "manuscript/", 0)
	r.warnN(tooMany, "%d document(s) were left out: an import holds at most %d Markdown files.", tooMany, MaxMarkdown)
	r.warnN(flattened, "%d document(s) were nested deeper than a path may be; they were placed in the deepest folder allowed, in reading order.", flattened)
	r.warnN(dropLong, "%d long synopsis(es) were left out: together they would not fit the manifest.", dropLong)
	if tooMany > 0 {
		r.skipped = append(r.skipped, Skipped{Path: "(binder)", Reason: fmt.Sprintf("over the %d-document limit", MaxMarkdown)})
	}
	mb, _ := json.Marshal(m)
	files = append(files, File{Path: "scriobh.json", Data: mb})
	return files
}

func countKids(d *cdoc) int {
	n := 0
	for _, k := range d.kids {
		n += 1 + countKids(k)
	}
	return n
}

// slugOf is a short file-name slug of a title.
func slugOf(title string) string {
	s := projects.Slugify(title)
	if s == "" {
		return "untitled"
	}
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		return "untitled"
	}
	return s
}

// asPrepass turns a converter's report into what the intake keeps.
func (r *cresult) asPrepass(ex *extras) *prepass {
	kind := "markdown"
	if r.srcKind != "" {
		kind = r.srcKind
	}
	return &prepass{layout: r.layout, srcKind: kind, srcHash: r.srcHash, warnings: r.warnings, skipped: r.skipped, meta: r.meta, extras: ex, keepManifest: true, epub: r.epub}
}

// cleanText makes text from a source into one clean line: control characters
// become spaces, white space is collapsed, and it is cut at max characters.
func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError || unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if rs := []rune(s); len(rs) > max {
		s = strings.TrimSpace(string(rs[:max]))
	}
	return s
}

// synopsisOf splits a synopsis into the one-line field and the long field: the
// long one is kept when there is more than the line holds.
func synopsisOf(s string) (short, long string) {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"))
	if s == "" {
		return "", ""
	}
	short = cleanText(s, maxSynopsisShort)
	if short != s {
		long = s
	}
	return short, long
}

// ---- research and the things a Compendium would hold -------------------------------------------

// extras is what a converted import writes besides documents. It is part of
// the token, so apply cannot differ from the preview.
type extras struct {
	research []*rnote
	pictures []*cpicture
	hash     string
}

func newExtras(notes []*rnote, pics *picBox) *extras {
	var list []*cpicture
	if pics != nil {
		list = pics.list
	}
	if len(notes) == 0 && len(list) == 0 {
		return nil
	}
	h := sha256.New()
	for _, n := range notes {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\n", n.key, n.parentKey, n.kind, n.title, n.sourceURL, strings.Join(n.tags, "\x01"), n.candidate, n.body)
	}
	for _, p := range list {
		fmt.Fprintf(h, "picture\x00%d\x00%s\x00%s\n", p.n, p.pic.SHA256, p.pic.Name)
	}
	return &extras{research: notes, pictures: list, hash: hex.EncodeToString(h.Sum(nil))}
}

// ResearchNode is one proposed research note of a preview.
type ResearchNode struct {
	Key       string   `json:"key" desc:"Stable identifier for edits: research/ and its position in the source."`
	ParentKey string   `json:"parent_key" desc:"Key of the containing folder; empty at the top."`
	Kind      string   `json:"kind" enum:"note,folder,clipping"`
	Title     string   `json:"title"`
	Words     int      `json:"words"`
	Tags      []string `json:"tags,omitempty" desc:"From the source's keywords."`
	Candidate string   `json:"entity_candidate,omitempty" enum:"character,place" desc:"The note looks like a character or place sheet. With scrivener_sheets: entities the apply makes it an entity of the project's primary Compendium where its name is free; otherwise it is a research note, and an entity can be made from it later (entity.create)."`
}

// sortedKeys lists a map's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// maxReportKeys is how many distinct names a report lists; the rest are one "(other)".
const maxReportKeys = 30

// counter counts names that come out of a foreign file (statuses, types, tags,
// visibility, extensions ...). The names are the file's to choose, so each is made
// into one short clean line and only the first maxReportKeys distinct ones are kept:
// fifty thousand different tags are thirty tags and a count of the others.
type counter struct {
	m        map[string]int
	overflow bool // more distinct names than were kept
}

func newCounter() *counter { return &counter{m: map[string]int{}} }

func (c *counter) add(key string) { c.addN(key, 1) }

func (c *counter) addN(key string, n int) {
	key = cleanText(key, 60)
	if key == "" {
		key = "(none)"
	}
	if _, ok := c.m[key]; !ok && len(c.m) >= maxReportKeys {
		key, c.overflow = "(other)", true
	}
	c.m[key] += n
}

// total is the sum of the counts.
func (c *counter) total() int {
	n := 0
	for _, v := range c.m {
		n += v
	}
	return n
}

// size says how many distinct names there were, as far as it is known ("30 or more").
func (c *counter) size() string {
	if c.overflow {
		return fmt.Sprintf("%d or more", len(c.m)-1)
	}
	return fmt.Sprint(len(c.m))
}

func (c *counter) empty() bool { return len(c.m) == 0 }

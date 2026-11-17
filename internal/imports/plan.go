package imports

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Layout names.
const (
	LayoutMarkdown = "markdown-folder"
	LayoutScriobh  = "scriobh"
	// LayoutObsidian and LayoutDocx are chosen by the writer, never detected.
	LayoutObsidian = Obsidian
	LayoutDocx     = "docx"
	LayoutPdf      = "pdf"
)

// Node is one proposed document. The nodes of a preview are in reading order;
// a client nests them by parent_key.
type Node struct {
	Key        string   `json:"key" desc:"Stable identifier for edits: the source path (a split scene adds #N; a folder with no text of its own is its folder path with a trailing slash)."`
	ParentKey  string   `json:"parent_key" desc:"Key of the containing node; empty for the top level."`
	Kind       string   `json:"kind" enum:"part,chapter,scene,note,front_matter,back_matter"`
	Title      string   `json:"title"`
	Status     string   `json:"status,omitempty"`
	SourcePath string   `json:"source_path,omitempty" desc:"The file the text comes from; empty for a folder that becomes an empty part."`
	Words      int      `json:"words"`
	Headings   int      `json:"headings,omitempty" desc:"Second-level headings in the file, which split_scenes=h2 would turn into scenes."`
	Split      bool     `json:"split,omitempty" desc:"This file was split into scenes at its second-level headings."`
	Warnings   []string `json:"warnings,omitempty"`
}

// pnode is a node with its text and children while the plan is built.
type pnode struct {
	Node
	body  string
	hash  string
	kids  []*pnode
	order int
	meta  docMeta
}

// manifest is the part of scriobh.json / project.json the importer reads. It
// is read tolerantly: unknown fields are ignored, and the project block may sit
// at the top level.
type manifest struct {
	Project struct {
		Title      string `json:"title"`
		Subtitle   string `json:"subtitle"`
		Kind       string `json:"kind"`
		Author     string `json:"author"`
		AuthorName string `json:"author_name"`
		Language   string `json:"language"`
	} `json:"project"`
	Title      string `json:"title"`
	Subtitle   string `json:"subtitle"`
	Kind       string `json:"kind"`
	Author     string `json:"author"`
	AuthorName string `json:"author_name"`
	Language   string `json:"language"`
	Documents  []struct {
		Path   string `json:"path"`
		Kind   string `json:"kind"`
		Title  string `json:"title"`
		Status string `json:"status"`
		// Outline metadata, written by the mirror (project.json version 2). A
		// manifest without them (an export bundle, an older mirror) reads as
		// no synopsis, no scene metadata, no target, and compiled.
		SynopsisShort    flexString `json:"synopsis_short"`
		SynopsisLong     flexString `json:"synopsis_long"`
		Pov              flexString `json:"pov"`
		PovEntity        flexRef    `json:"pov_entity"`
		Location         flexString `json:"location"`
		LocationEntity   flexRef    `json:"location_entity"`
		SourcePath       flexString `json:"source_path"`
		StoryDate        flexString `json:"story_date"`
		StoryDateEnd     flexString `json:"story_date_end"`
		TargetWords      flexInt    `json:"target_words"`
		IncludeInCompile flexBool   `json:"include_in_compile"`
	} `json:"documents"`
}

// The outline fields are decoded leniently: a value of the wrong type is
// treated as absent, so one bad field loses that field and not the manifest
// (titles, kinds and the tree come from the rest of it).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = flexString(s)
	}
	return nil
}

// flexRef is the id of the entity a mirror manifest (version 3) says a scene's
// point of view or location was. It is an untrusted id: the importer uses it
// only if that entity is in the project's scope and the importing user may
// read it.
type flexRef string

func (f *flexRef) UnmarshalJSON(b []byte) error {
	var v struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(b, &v) == nil && len(v.ID) <= 64 {
		*f = flexRef(v.ID)
	}
	return nil
}

type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	var n float64
	if json.Unmarshal(b, &n) == nil && n == float64(int(n)) {
		*f = flexInt(int(n))
	}
	return nil
}

// flexBool is "not set" until the JSON says true or false.
type flexBool struct{ set, v bool }

func (f *flexBool) UnmarshalJSON(b []byte) error {
	var v bool
	if json.Unmarshal(b, &v) == nil {
		f.set, f.v = true, v
	}
	return nil
}

// docMeta is a document's outline metadata as the importer will store it,
// cleaned to the limits doc.update enforces. The manifest is untrusted.
type docMeta struct {
	synopsis, synopsisLong, pov, location, storyDate, storyEnd string
	povEntity, locationEntity                                  string // untrusted ids from a version 3 manifest
	target                                                     int
	exclude                                                    bool
}

// Limits on imported outline metadata: those of doc.update, and a cap on the
// long synopsis (which doc.update leaves to the request size).
const (
	maxSynopsisShort = 300
	maxSceneField    = 200
	maxStoryDate     = 64
	maxSynopsisLong  = 20_000
	maxTargetWords   = 10_000_000
)

func cleanMeta(d docInfo) (m docMeta, cut bool) {
	one := func(s string, max int) string {
		v, c := cleanLine(s, max)
		cut = cut || c
		return v
	}
	m = docMeta{
		synopsis: one(d.synopsisShort, maxSynopsisShort), pov: one(d.pov, maxSceneField), location: one(d.location, maxSceneField),
		storyDate: one(d.storyDate, maxStoryDate), storyEnd: one(d.storyEnd, maxStoryDate),
		exclude: d.includeInCompile.set && !d.includeInCompile.v, povEntity: d.povEntity, locationEntity: d.locationEntity,
	}
	if d.targetWords > 0 && d.targetWords <= maxTargetWords {
		m.target = d.targetWords
	}
	// The long synopsis is kept as written, line breaks and spacing included,
	// as doc.update keeps it; only control characters other than newline,
	// carriage return and tab are dropped, and it is cut at the cap.
	l := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, d.synopsisLong)
	if r := []rune(l); len(r) > maxSynopsisLong {
		l, cut = string(r[:maxSynopsisLong]), true
	}
	m.synopsisLong = l
	return m, cut
}

// ProjectInfo is what the plan proposes for a new project.
type ProjectInfo struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Kind     string `json:"kind"`
	Author   string `json:"author,omitempty"`
	Language string `json:"language,omitempty"`
}

// plan is the proposed import.
type plan struct {
	layout       string
	manifestFile string
	project      ProjectInfo
	roots        []*pnode
	warnings     []string
	skipped      []Skipped
	byKey        map[string]*pnode
	// research notes of a converted import (Scrivener's Research folder)
	rRoots []*rplan
	rByKey map[string]*rplan
	// merged records the documents the writer merged into another (key -> key of the one it went into), so an
	// edition's chapters can name the document their text ended up in.
	merged map[string]string
}

// Options are the mapping choices that shape the plan itself.
type Options struct {
	// Name is what the folder is called; it becomes the project title when the
	// folder has nothing better.
	Name string
	// SplitScenes is "h2" to turn second-level headings into scenes, "" or "none".
	SplitScenes string
	// SplitFiles overrides SplitScenes for single files, by node key.
	SplitFiles map[string]bool
	// Layout is "obsidian" to read the files as an Obsidian vault; "" reads a
	// Scríobh mirror or export, or a generic Markdown folder. It is never
	// guessed from the files.
	Layout string
	// Sheets is "entities" when character and place sheets are to be made entities.
	Sheets string
	// DocxHeadings is "parts" to make Heading 1 of a DOCX a part; "" or "chapters".
	DocxHeadings string
}

func (o Options) splits(key string) bool {
	if v, ok := o.SplitFiles[key]; ok {
		return v
	}
	return o.SplitScenes == "h2"
}

type docInfo struct {
	kind, title, status                                             string
	synopsisShort, synopsisLong, pov, location, storyDate, storyEnd string
	povEntity, locationEntity                                       string
	targetWords                                                     int
	includeInCompile                                                flexBool
	sourcePath                                                      string
}

func buildPlan(in *intake, opt Options) *plan {
	p := &plan{byKey: map[string]*pnode{}}
	p.project = ProjectInfo{Title: strings.TrimSpace(prettify(stem(opt.Name))), Kind: "blank", Language: "en"}
	if p.project.Title == "" {
		p.project.Title = "Imported project"
	}
	if t, _ := cleanLine(p.project.Title, 200); t != "" {
		p.project.Title = t
	}
	var m *manifest
	for _, name := range []string{"scriobh.json", "project.json"} {
		raw, ok := in.manifest[name]
		if !ok {
			continue
		}
		var mm manifest
		if err := json.Unmarshal(raw, &mm); err != nil {
			p.warnings = append(p.warnings, name+" is not valid JSON and was ignored.")
			continue
		}
		if m == nil || (len(m.Documents) == 0 && len(mm.Documents) > 0) {
			m, p.manifestFile = &mm, name
		}
	}
	b := &builder{p: p, opt: opt, info: map[string]docInfo{}}
	if in.layout != "" && !in.keepManifest { // a DOCX or Obsidian conversion: a plain folder, whatever manifest it holds
		m = nil
	}
	b.conv = in.layout != "" && in.keepManifest
	if in.project != nil {
		if in.project.Title != "" {
			p.project.Title = in.project.Title
		}
		p.project.Subtitle = in.project.Subtitle
		p.project.Author = in.project.Author
		if in.project.Language != "" {
			p.project.Language = in.project.Language
		}
	}
	p.warnings = append(p.warnings, in.warnings...)
	if m != nil {
		p.layout = LayoutScriobh
		p.applyManifestProject(m)
		for _, d := range m.Documents {
			if cp, why := cleanPath(d.Path); why == "" {
				b.info[cp] = docInfo{kind: d.Kind, title: d.Title, status: d.Status,
					synopsisShort: string(d.SynopsisShort), synopsisLong: string(d.SynopsisLong), pov: string(d.Pov), location: string(d.Location),
					storyDate: string(d.StoryDate), storyEnd: string(d.StoryDateEnd), targetWords: int(d.TargetWords), includeInCompile: d.IncludeInCompile,
					povEntity: string(d.PovEntity), locationEntity: string(d.LocationEntity), sourcePath: string(d.SourcePath)}
			}
		}
		b.buildScriobh(in)
	} else {
		p.layout = LayoutMarkdown
		if in.layout != "" {
			p.layout = in.layout
		}
		b.buildGeneric(in.md)
	}
	b.finish(in)
	if b.conv {
		p.layout, p.manifestFile = in.layout, ""
		p.addResearch(in.extras)
	}
	return p
}

func (p *plan) applyManifestProject(m *manifest) {
	pick := func(vals ...string) string {
		for _, v := range vals {
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
		}
		return ""
	}
	if t, _ := cleanLine(pick(m.Project.Title, m.Title), 200); t != "" {
		p.project.Title = t
	}
	if k := pick(m.Project.Kind, m.Kind); k != "" {
		for _, ok := range projects.Kinds {
			if ok == k {
				p.project.Kind = k
			}
		}
	}
	p.project.Subtitle, _ = cleanLine(pick(m.Project.Subtitle, m.Subtitle), 200)
	p.project.Author, _ = cleanLine(pick(m.Project.AuthorName, m.Project.Author, m.AuthorName, m.Author), 200)
	if l := pick(m.Project.Language, m.Language); l != "" && len(l) <= 16 && !strings.ContainsAny(l, " <>\"'") {
		p.project.Language = l
	}
}

// builder turns the file set into nodes.
type builder struct {
	p     *plan
	opt   Options
	info  map[string]docInfo // manifest entries by path
	order int
	conv  bool // a converter of Scrivener, WordPress or Ghost made the manifest
	// counters for warnings that are one line however many files cause them
	unlisted, empty, cut, tooMany, metaCut int
	count                                  int // nodes built so far
}

// dirNode is a folder of the file set.
type dirNode struct {
	name  string
	path  string
	dirs  []*dirNode
	files []*srcFile
}

// buildDirs nests files (paths relative to the same root) into folders. Paths
// keep their full text so a node's key is its path in the file set.
func buildDirs(files []*srcFile, strip string) *dirNode {
	root := &dirNode{}
	for _, f := range files {
		segs := strings.Split(strings.TrimPrefix(f.Path, strip), "/")
		d := root
		for i, s := range segs[:len(segs)-1] {
			var next *dirNode
			for _, c := range d.dirs {
				if c.name == s {
					next = c
					break
				}
			}
			if next == nil {
				next = &dirNode{name: s, path: strip + strings.Join(segs[:i+1], "/") + "/"}
				d.dirs = append(d.dirs, next)
			}
			d = next
		}
		d.files = append(d.files, f)
	}
	return root
}

// entry is a folder or a file being ordered among its siblings.
type entry struct {
	dir  *dirNode
	file *srcFile
	num  int
	has  bool
	rest string
	cls  string
	key  sortKey
}

func entriesOf(d *dirNode, skip *srcFile) []entry {
	var out []entry
	add := func(name string, e entry) {
		e.num, e.has, e.rest = splitName(name)
		e.cls = classify(e.rest)
		e.key = keyFor(e.num, e.has, e.cls, name)
		out = append(out, e)
	}
	for _, c := range d.dirs {
		add(c.name, entry{dir: c})
	}
	for _, f := range d.files {
		if f != skip {
			add(stem(path.Base(f.Path)), entry{file: f})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].key.less(out[j].key) {
			return true
		}
		if out[j].key.less(out[i].key) {
			return false
		}
		return out[i].dir != nil && out[j].dir == nil // a folder before a file of the same name
	})
	return out
}

func (b *builder) add(parent *pnode, n *pnode) {
	// Keys identify nodes for edits and for the parent links of the insert, so
	// two nodes must never share one (a file a.md and a folder named a.md/ are
	// kept apart by the folder's trailing slash; this is the last line of defence).
	for i := 2; b.p.byKey[n.Key] != nil; i++ {
		n.Key = fmt.Sprintf("%s~%d", strings.SplitN(n.Key, "~", 2)[0], i)
	}
	b.count++
	b.order++
	n.order = b.order
	if parent == nil {
		b.p.roots = append(b.p.roots, n)
	} else {
		parent.kids = append(parent.kids, n)
		n.ParentKey = parent.Key
	}
	b.p.byKey[n.Key] = n
}

func (b *builder) finish(in *intake) {
	p := b.p
	// A copy: the intake may be shared (limiter.go) and is only ever read.
	p.skipped = append(append([]Skipped(nil), in.skipped...), p.skipped...)
	if len(in.md) == 0 {
		p.warnings = append(p.warnings, "No Markdown files were found, so there is nothing to import.")
	}
	if b.empty > 0 {
		p.warnings = append(p.warnings, fmt.Sprintf("%d empty file(s) will be imported as empty documents.", b.empty))
	}
	if b.unlisted > 0 {
		p.warnings = append(p.warnings, fmt.Sprintf("%d Markdown file(s) are not listed in the manifest and were placed by their file names.", b.unlisted))
	}
	if b.tooMany > 0 {
		p.warnings = append(p.warnings, fmt.Sprintf("%d file(s) were kept whole because splitting them at their headings would pass the limit of %d documents.", b.tooMany, MaxDocuments))
	}
	if b.cut > 0 {
		p.warnings = append(p.warnings, fmt.Sprintf("%d title(s) were longer than 300 characters and were shortened.", b.cut))
	}
	if b.metaCut > 0 {
		p.warnings = append(p.warnings, fmt.Sprintf("%d %s had a synopsis, point of view, location or story date longer than allowed, which was shortened.", b.metaCut, map[bool]string{true: "document", false: "documents"}[b.metaCut == 1]))
	}
	for _, name := range in.bigManifest {
		p.warnings = append(p.warnings, fmt.Sprintf("%s is larger than %d MiB and was ignored; the titles, kinds and outline it holds were not read.", name, MaxManifest>>20))
	}
	if p.layout == LayoutScriobh {
		listed := map[string]bool{}
		for _, f := range in.md {
			listed[f.Path] = true
		}
		var missing []string
		for k := range b.info {
			if !listed[k] && isMarkdown(k) {
				missing = append(missing, k)
			}
		}
		sort.Strings(missing)
		if n := len(missing); n > 0 {
			ex := strings.Join(missing[:min(n, 3)], ", ")
			p.warnings = append(p.warnings, fmt.Sprintf("The manifest lists %d file(s) that were not supplied (for example %s).", n, ex))
		}
	}
	sort.SliceStable(p.skipped, func(i, j int) bool { return p.skipped[i].Path < p.skipped[j].Path })
}

// ---- generic folders -------------------------------------------------------------

func (b *builder) buildGeneric(files []*srcFile) {
	b.walkGeneric(buildDirs(files, ""), nil, true)
}

func (b *builder) walkGeneric(d *dirNode, parent *pnode, top bool) {
	for _, e := range entriesOf(d, nil) {
		if e.dir != nil {
			title := prettify(e.rest)
			if title == "" {
				title = fmt.Sprintf("Part %d", e.num)
			}
			title, cut := cleanTitle(title)
			if cut {
				b.cut++
			}
			kind := "part"
			if e.cls != "" {
				kind = e.cls
			}
			n := &pnode{Node: Node{Key: e.dir.path, Kind: kind, Title: title, Status: "draft"}}
			b.add(parent, n)
			b.walkGeneric(e.dir, n, false)
			continue
		}
		kind := "chapter"
		if e.cls != "" {
			kind = e.cls
		}
		if top && strings.EqualFold(e.file.Path, "readme.md") {
			kind = "note"
		}
		b.addFile(parent, e, kind, "", "", true)
	}
}

// addFile adds a file as a document, splitting it into scenes when asked. When
// fromText is set the title comes from front matter or a leading heading, and
// that heading leaves the body (it is the title now); front matter stays in the
// text exactly as written.
func (b *builder) addFile(parent *pnode, e entry, kind, title, status string, fromText bool) *pnode {
	f := e.file
	body := f.Text
	fmEnd := frontMatterEnd(body)
	if fromText && title == "" {
		fmTitle := frontMatterTitle(body[:fmEnd])
		h1, hs, he, ok := leadingH1(body, fmEnd)
		switch {
		case fmTitle != "":
			title = fmTitle
			if ok && strings.EqualFold(strings.Join(strings.Fields(h1), " "), strings.Join(strings.Fields(fmTitle), " ")) {
				body = body[:hs] + body[he:]
			}
		case ok:
			title = h1
			body = body[:hs] + body[he:]
		}
	}
	if title == "" {
		title = prettify(e.rest)
	}
	if title == "" && e.has {
		title = fmt.Sprintf("Chapter %d", e.num)
	}
	if title == "" {
		title = "Untitled"
	}
	title, cut := cleanTitle(title)
	if cut {
		b.cut++
	}
	if status == "" {
		status = "draft"
	}
	n := &pnode{Node: Node{Key: f.Path, Kind: kind, Title: title, Status: status, SourcePath: f.Path}, body: body, hash: f.Hash}
	hs := secondLevelHeadings(body, MaxDocuments+1)
	n.Headings = len(hs)
	if len(hs) > 0 && b.opt.splits(n.Key) && kind != "note" {
		// Splitting is refused before any scene is built when it would take the
		// import past the document limit: a 2 MiB file of headings is 400,000
		// scenes, and the plan must stay small whatever the files hold.
		if b.count+1+len(hs) <= MaxDocuments {
			b.splitInto(parent, n, hs)
			return n
		}
		b.tooMany++
		b.p.skipped = append(b.p.skipped, Skipped{Path: f.Path, Reason: reasonTooMany})
	}
	if strings.TrimSpace(body) == "" && warnEmpty(b) {
		b.empty++
	}
	b.add(parent, n)
	n.Words = text.WordCount(n.body)
	warnLong(n)
	return n
}

// warnEmpty reports whether an empty file is worth a warning. In a Scríobh layout a
// part with no text of its own is exported as an empty file, which is normal.
func warnEmpty(b *builder) bool { return b.p.layout != LayoutScriobh }

// commentWarning says that a file's alternatives comment will be kept as text.
func commentWarning(why string) string {
	return "The alternatives comment at the end of this file was not used (" + strings.TrimRight(why, ".") + "). It is kept as an ordinary comment."
}

func warnLong(n *pnode) {
	switch {
	case n.Words >= docs.ReadOnlyWords:
		n.Warnings = append(n.Warnings, fmt.Sprintf("%d words: this document will open read-only. Split it into scenes or chapters.", n.Words))
	case n.Words >= docs.WarnWords:
		n.Warnings = append(n.Warnings, fmt.Sprintf("%d words: a long document; consider splitting it.", n.Words))
	}
}

// splitInto keeps the text before the first heading in the chapter and turns
// each second-level heading and its section into a scene. The heading line
// becomes the scene's title and is not repeated in its text; everything else is
// kept exactly.
func (b *builder) splitInto(parent, n *pnode, hs []h2) {
	full := n.body
	n.body = tidy(full[:hs[0].start])
	n.Split = true
	b.add(parent, n)
	n.Words = text.WordCount(n.body)
	for i, h := range hs {
		end := len(full)
		if i+1 < len(hs) {
			end = hs[i+1].start
		}
		title, cut := cleanTitle(h.title)
		if cut {
			b.cut++
		}
		c := &pnode{Node: Node{Key: fmt.Sprintf("%s#%d", n.Key, i+1), Kind: "scene", Title: title, Status: n.Status, SourcePath: n.SourcePath},
			body: tidy(strings.TrimLeft(full[h.end:end], "\n")), hash: n.hash}
		b.add(n, c)
		c.Words = text.WordCount(c.body)
		warnLong(c)
	}
}

// tidy ends a split section with exactly one newline, or is empty when the
// section holds nothing.
func tidy(s string) string {
	s = strings.TrimRight(s, " \t\n")
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return s + "\n"
}

// ---- Scríobh mirror and export ------------------------------------------------------

// buildScriobh reads the numbered tree the export bundle, the Git push and the
// mirror write. manuscript/ holds the manuscript and notes/ the notes (flat).
// A document with children is a folder whose own text is the zero-numbered file
// named like the folder (00-chapter-two.md in 02-chapter-two/); everything else
// in the folder is a child. Titles, kinds and statuses come from the manifest,
// because file names are lossy slugs; a file the manifest does not list is
// placed by its name.
func (b *builder) buildScriobh(in *intake) {
	var ms, notes []*srcFile
	for _, f := range in.md {
		switch {
		case strings.HasPrefix(f.Path, "manuscript/"):
			ms = append(ms, f)
		case strings.HasPrefix(f.Path, "notes/"):
			notes = append(notes, f)
		case strings.EqualFold(f.Path, "readme.md"):
			b.p.skipped = append(b.p.skipped, Skipped{Path: f.Path, Reason: reasonReadme})
		default:
			b.p.skipped = append(b.p.skipped, Skipped{Path: f.Path, Reason: reasonNotBook})
		}
	}
	if len(ms) == 0 && len(notes) == 0 {
		// A manifest with no numbered tree: read the folder as a generic one,
		// keeping what the manifest says about the project.
		b.p.warnings = append(b.p.warnings, "There is no manuscript/ folder next to the manifest, so the Markdown was read as a generic folder.")
		b.p.skipped = nil
		b.buildGeneric(in.md)
		return
	}
	b.walkScriobh(buildDirs(ms, "manuscript/"), nil)
	for _, f := range flattenFiles(buildDirs(notes, "notes/")) {
		e := entry{file: f}
		e.num, e.has, e.rest = splitName(stem(path.Base(f.Path)))
		b.addListed(nil, e, "note", "")
	}
}

// flattenFiles lists a folder's files in sibling order, descending into
// subfolders in place.
func flattenFiles(d *dirNode) []*srcFile {
	var out []*srcFile
	for _, e := range entriesOf(d, nil) {
		if e.dir != nil {
			out = append(out, flattenFiles(e.dir)...)
		} else {
			out = append(out, e.file)
		}
	}
	return out
}

// sameName reports whether a file and a folder carry the same name once the
// number is removed.
func sameName(a, b string) bool {
	norm := func(s string) string {
		return strings.ToLower(strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' || r == ' ' }), "-"))
	}
	return norm(a) == norm(b)
}

func (b *builder) walkScriobh(d *dirNode, parent *pnode) {
	for _, e := range entriesOf(d, nil) {
		if e.dir == nil {
			kind := "chapter"
			if e.cls != "" { // a name like 00-front-matter or appendix, when the manifest does not say
				kind = e.cls
			}
			b.addListed(parent, e, kind, "")
			continue
		}
		var own *srcFile
		for _, f := range e.dir.files {
			n, has, rest := splitName(stem(path.Base(f.Path)))
			if has && n == 0 && sameName(rest, e.rest) {
				own = f
				break
			}
		}
		var n *pnode
		if own != nil {
			oe := entry{file: own, has: true, rest: e.rest}
			n = b.addListed(parent, oe, "part", prettify(e.rest))
		} else {
			title := prettify(e.rest)
			if title == "" {
				title = fmt.Sprintf("Part %d", e.num)
			}
			title, _ = cleanTitle(title)
			kind := "part"
			if e.cls != "" {
				kind = e.cls
			}
			n = &pnode{Node: Node{Key: e.dir.path, Kind: kind, Title: title, Status: "draft"}}
			b.add(parent, n)
		}
		// The folder's remaining entries are its children.
		child := *e.dir
		child.files = nil
		for _, f := range e.dir.files {
			if f != own {
				child.files = append(child.files, f)
			}
		}
		b.walkScriobh(&child, n)
	}
}

// addListed adds a file whose kind, title and status the manifest may give.
// The text is kept verbatim: nothing is read out of it and no heading removed.
func (b *builder) addListed(parent *pnode, e entry, defKind, defTitle string) *pnode {
	info, listed := b.info[e.file.Path]
	kind := defKind
	if listed && validKind(info.kind) {
		kind = info.kind
	}
	title := ""
	if listed {
		title = strings.TrimSpace(info.title)
	}
	status := ""
	if listed && validStatus(info.status) {
		status = info.status
	}
	if !listed && len(b.info) > 0 {
		b.unlisted++
	}
	if title == "" {
		title = defTitle
	}
	if title == "" {
		title = prettify(e.rest)
	}
	if title == "" && e.has {
		title = fmt.Sprintf("Chapter %d", e.num)
	}
	if title == "" {
		title = "Untitled"
	}
	n := b.addFile(parent, e, kind, title, status, false)
	if listed && b.conv && info.sourcePath != "" {
		n.SourcePath, _ = cleanLine(info.sourcePath, 300)
	}
	if listed {
		var cut bool
		if n.meta, cut = cleanMeta(info); cut {
			b.metaCut++
		}
	}
	return n
}

func validKind(k string) bool {
	for _, x := range docs.Kinds {
		if x == k {
			return true
		}
	}
	return false
}

func validStatus(s string) bool {
	for _, x := range docs.Statuses {
		if x == s {
			return true
		}
	}
	return false
}

// ---- edits --------------------------------------------------------------------------

// Limits on the writer's mapping edits: generous for a real book, small enough
// that a request cannot make the server do unbounded work.
const (
	MaxEdits       = 10_000
	MaxReorderKeys = 20_000
)

// checkMapping refuses oversized edit lists before any plan is built.
func checkMapping(edits []Edit, reorders []Reorder) error {
	if len(edits) > MaxEdits {
		return ops.Invalid("edits", fmt.Sprintf("At most %d edits.", MaxEdits))
	}
	keys := 0
	for _, r := range reorders {
		keys += len(r.Keys) + 1
	}
	if keys > MaxReorderKeys {
		return ops.Invalid("reorder", fmt.Sprintf("At most %d keys in all.", MaxReorderKeys))
	}
	return nil
}

// Edit changes one node of the previewed mapping, by key.
type Edit struct {
	Key   string  `json:"key" desc:"The node's key from the preview."`
	Title *string `json:"title,omitempty" desc:"Rename the document."`
	Kind  *string `json:"kind,omitempty" enum:"part,chapter,scene,note,front_matter,back_matter" desc:"Change what kind of document it is."`
	Skip  bool    `json:"skip,omitempty" desc:"Leave this node, and everything inside it, out of the import."`
	Split *bool   `json:"split,omitempty" desc:"Split this one file at its second-level headings (true) or keep it whole (false), whatever split_scenes says."`
	// MergeInto and Parent change the shape of the mapping: join a document to another, move it elsewhere.
	MergeInto string  `json:"merge_into,omitempty" desc:"Add this document's text to the end of the document with this key and remove this one; what was inside it goes into the other. Use with nothing else on the same edit. Footnotes are renumbered."`
	Parent    *string `json:"parent,omitempty" desc:"Move this document, and what is inside it, to the end of the document with this key (or to the top level with an empty string). A document cannot be moved into itself or into what is inside it, and the tree stays at most ten levels deep."`
}

// Reorder sets the order of one parent's children.
type Reorder struct {
	Parent string   `json:"parent" desc:"Key of the parent node, or empty for the top level."`
	Keys   []string `json:"keys" desc:"The parent's children in the order wanted. Children you leave out follow, in their proposed order."`
}

// applyEdits applies the writer's changes to a built plan. Split edits are
// applied earlier, because they change the plan's shape; the rest are here.
//
// The result never depends on the order the edits arrive in. What an edit names (a merge target, a new parent) is
// judged against the plan as the whole set leaves it: first every move is made (in the book's reading order), then
// every merge (a chapter that others are merged into waits for them, so a chain a into b into c is the same in any
// order, and a loop is refused), then the titles and kinds, and only then is the set of chapters left out settled and
// the merges and moves checked against it. A chapter is left out when it, or a chapter it is in, is left out, so a
// merge into a chapter that is left out is refused whether the skip or the merge was written first, and so is a move
// into one.
func (p *plan) applyEdits(edits []Edit, reorders []Reorder) (map[string]bool, error) {
	if err := checkMapping(edits, reorders); err != nil {
		return nil, err
	}
	skip := map[string]bool{}
	field := func(i int, f string) string { return fmt.Sprintf("edits[%d].%s", i, f) }
	// the reading order of the book, to make the outcome the book's and not the request's
	pos := map[string]int{}
	var walk func(kids []*pnode)
	walk = func(kids []*pnode) {
		for _, k := range kids {
			pos[k.Key] = len(pos)
			walk(k.kids)
		}
	}
	walk(p.roots)
	// 1. what each edit names, and the set of documents that are merged away (which no other edit may touch)
	mergeOf := map[string]int{} // source key -> the edit that merges it
	for i, e := range edits {
		if e.MergeInto != "" {
			if _, dup := mergeOf[e.Key]; dup {
				return nil, ops.Invalid(field(i, "key"), "That document is merged by two edits.")
			}
			if _, ok := p.byKey[e.Key]; ok {
				mergeOf[e.Key] = i
			}
		}
	}
	var moves, merges, shape []int
	for i, e := range edits {
		if _, ok := p.byKey[e.Key]; !ok {
			if _, isResearch := p.rByKey[e.Key]; isResearch {
				if err := p.editResearch(i, e, skip); err != nil {
					return nil, err
				}
				continue
			}
			return nil, ops.Invalid(field(i, "key"), "That key is not in the preview.")
		}
		if mi, away := mergeOf[e.Key]; away && mi != i {
			return nil, ops.Invalid(field(i, "key"), "That document is merged into another by one of the edits, so it takes no other edit.")
		}
		switch {
		case e.MergeInto != "":
			if e.Parent != nil || e.Skip || e.Title != nil || e.Kind != nil || e.Split != nil {
				return nil, ops.Invalid(field(i, "merge_into"), "A merge is an edit of its own: use no other field with it.")
			}
			merges = append(merges, i)
		default:
			if e.Parent != nil {
				if e.Skip {
					return nil, ops.Invalid(field(i, "parent"), "A document that is left out cannot be moved.")
				}
				moves = append(moves, i)
			}
			shape = append(shape, i)
		}
	}
	was := map[int]string{} // where each moved document was
	for _, i := range edits2(moves, edits, pos) {
		was[i] = p.byKey[edits[i].Key].ParentKey
		if err := p.moveEdit(i, edits[i], p.byKey[edits[i].Key]); err != nil {
			return nil, err
		}
	}
	// 2. merges, a chapter after every chapter that is merged into it
	pending := edits2(merges, edits, pos)
	for len(pending) > 0 {
		next := -1
		for k, i := range pending {
			waits := false
			for _, o := range pending {
				if o != i && edits[o].MergeInto == edits[i].Key {
					waits = true
				}
			}
			if !waits {
				next = k
				break
			}
		}
		if next < 0 {
			return nil, ops.Invalid(field(pending[0], "merge_into"), "Documents cannot be merged into each other in a loop.")
		}
		i := pending[next]
		pending = append(pending[:next:next], pending[next+1:]...)
		n, ok := p.byKey[edits[i].Key]
		if !ok {
			return nil, ops.Invalid(field(i, "key"), "That document is not in the preview any more.")
		}
		if err := p.mergeEdit(i, edits[i], n); err != nil {
			return nil, err
		}
	}
	// 3. titles, kinds and the documents left out
	for _, i := range shape {
		e := edits[i]
		n := p.byKey[e.Key]
		if e.Title != nil {
			t, cut := cleanTitle(*e.Title)
			if t == "" || cut {
				return nil, ops.Invalid(field(i, "title"), "A title is one to 300 characters.")
			}
			n.Title = t
		}
		if e.Kind != nil {
			if !validKind(*e.Kind) {
				return nil, ops.Invalid(field(i, "kind"), "Unknown document kind.")
			}
			n.Kind = *e.Kind
		}
		if e.Skip {
			skip[e.Key] = true
		}
	}
	// 4. the plan is settled: a document is left out when it or a document it is in is, and nothing may be merged or
	// moved into one that is
	out := map[string]bool{}
	var mark func(kids []*pnode, under bool)
	mark = func(kids []*pnode, under bool) {
		for _, k := range kids {
			here := under || skip[k.Key]
			out[k.Key] = here
			mark(k.kids, here)
		}
	}
	mark(p.roots, false)
	for _, i := range merges {
		if t := p.byKey[p.mergedInto(edits[i].MergeInto)]; t == nil || out[t.Key] {
			return nil, ops.Invalid(field(i, "merge_into"), "A document that is left out cannot be merged.")
		}
	}
	for _, i := range moves {
		e := edits[i]
		if n := p.byKey[e.Key]; n != nil && n.ParentKey != "" && out[n.ParentKey] && n.ParentKey != was[i] {
			return nil, ops.Invalid(field(i, "parent"), "That document is left out.")
		}
	}
	for i, r := range reorders {
		var kids *[]*pnode
		if r.Parent == "" {
			kids = &p.roots
		} else {
			pn, ok := p.byKey[r.Parent]
			if !ok {
				return nil, ops.Invalid(fmt.Sprintf("reorder[%d].parent", i), "That key is not in the preview.")
			}
			kids = &pn.kids
		}
		byKey := map[string]*pnode{}
		for _, k := range *kids {
			byKey[k.Key] = k
		}
		var ordered []*pnode
		seen := map[string]bool{}
		for _, k := range r.Keys {
			n, ok := byKey[k]
			if !ok || seen[k] {
				return nil, ops.Invalid(fmt.Sprintf("reorder[%d].keys", i), "Every key must be a distinct child of that parent.")
			}
			seen[k] = true
			ordered = append(ordered, n)
		}
		for _, k := range *kids {
			if !seen[k.Key] {
				ordered = append(ordered, k)
			}
		}
		*kids = ordered
	}
	return skip, nil
}

// flatten lists the nodes that remain in reading order, with the skipped
// subtrees removed. ParentKey is rewritten to the surviving parent.
func (p *plan) flatten(skip map[string]bool) []*pnode {
	var out []*pnode
	var walk func(kids []*pnode)
	walk = func(kids []*pnode) {
		for _, n := range kids {
			if skip[n.Key] {
				continue
			}
			out = append(out, n)
			walk(n.kids)
		}
	}
	walk(p.roots)
	return out
}

// ---- merge and move ---------------------------------------------------------------------

// detach takes a node out of its parent's list.
func (p *plan) detach(n *pnode) {
	list := &p.roots
	if n.ParentKey != "" {
		if par, ok := p.byKey[n.ParentKey]; ok {
			list = &par.kids
		}
	}
	for i, k := range *list {
		if k == n {
			*list = append((*list)[:i:i], (*list)[i+1:]...)
			return
		}
	}
}

// inside reports whether x is n or sits inside it.
func (p *plan) inside(n, x *pnode) bool {
	for cur := x; cur != nil; {
		if cur == n {
			return true
		}
		if cur.ParentKey == "" {
			return false
		}
		cur = p.byKey[cur.ParentKey]
	}
	return false
}

func nodeDepth(p *plan, n *pnode) int {
	d := 1
	for cur := n; cur.ParentKey != ""; d++ {
		cur = p.byKey[cur.ParentKey]
		if cur == nil || d > 1000 {
			break
		}
	}
	return d
}

func subtreeHeight(n *pnode) int {
	h := 0
	for _, k := range n.kids {
		h = max(h, subtreeHeight(k))
	}
	return h + 1
}

// reshape applies merge_into and parent: the mapping edits that change the tree.
func (p *plan) mergeEdit(i int, e Edit, n *pnode) error {
	field := fmt.Sprintf("edits[%d]", i)
	{
		t, ok := p.byKey[e.MergeInto]
		switch {
		case !ok:
			return ops.Invalid(field+".merge_into", "That key is not in the preview.")
		case t == n:
			return ops.Invalid(field+".merge_into", "A document cannot be merged into itself.")
		case p.inside(n, t):
			return ops.Invalid(field+".merge_into", "A document cannot be merged into one that is inside it.")
		}
		t.body = mergeBodies(t.body, n.body)
		t.Words = text.WordCount(t.body)
		t.Headings = len(secondLevelHeadings(t.body, MaxDocuments+1))
		t.Warnings = nil
		warnLong(t)
		if p.merged == nil {
			p.merged = map[string]string{}
		}
		for _, k := range n.kids {
			k.ParentKey = t.Key
		}
		t.kids = append(t.kids, n.kids...)
		n.kids = nil
		p.detach(n)
		delete(p.byKey, n.Key)
		p.merged[n.Key] = t.Key
		return nil
	}
}

// edits2 orders the edits of one kind by where their document is in the book.
func edits2(idx []int, edits []Edit, pos map[string]int) []int {
	out := append([]int(nil), idx...)
	sort.SliceStable(out, func(a, b int) bool { return pos[edits[out[a]].Key] < pos[edits[out[b]].Key] })
	return out
}

func (p *plan) moveEdit(i int, e Edit, n *pnode) error {
	field := fmt.Sprintf("edits[%d]", i)
	np := *e.Parent
	if np == n.ParentKey {
		return nil
	}
	var target *pnode
	if np != "" {
		var ok bool
		if target, ok = p.byKey[np]; !ok {
			return ops.Invalid(field+".parent", "That key is not in the preview.")
		}
		if p.inside(n, target) {
			return ops.Invalid(field+".parent", "A document cannot be moved into itself or into what is inside it.")
		}
		if nodeDepth(p, target)+subtreeHeight(n) > MaxDepth {
			return ops.Invalid(field+".parent", fmt.Sprintf("The tree can be at most %d levels deep.", MaxDepth))
		}
	}
	p.detach(n)
	if target == nil {
		n.ParentKey = ""
		p.roots = append(p.roots, n)
	} else {
		n.ParentKey = target.Key
		target.kids = append(target.kids, n)
	}
	return nil
}

// mergedInto is the key of the node a merged document's text is in now.
func (p *plan) mergedInto(key string) string {
	for i := 0; i < 1000; i++ {
		t, ok := p.merged[key]
		if !ok {
			return key
		}
		key = t
	}
	return key
}

var reNoteRef = regexp.MustCompile(`\[\^(\d+)\]`)

// MergeBodies is mergeBodies for the comparison of a book with the project made from it, which has to put the chapters
// the writer merged at import back together the way the import did.
func MergeBodies(a, b string) string { return mergeBodies(a, b) }

// mergeBodies joins two documents' Markdown. A footnote of the second is numbered
// after those of the first, so the two never share a label.
func mergeBodies(a, b string) string {
	if strings.TrimSpace(b) == "" {
		return a
	}
	if strings.TrimSpace(a) == "" {
		return b
	}
	maxN := 0
	inFence := false
	for _, line := range strings.Split(a, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") || strings.HasPrefix(strings.TrimSpace(line), "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, m := range reNoteRef.FindAllStringSubmatch(line, -1) {
			if v, err := strconv.Atoi(m[1]); err == nil && v > maxN {
				maxN = v
			}
		}
	}
	if maxN > 0 && strings.Contains(b, "[^") {
		var out []string
		inFence := false
		for _, line := range strings.Split(b, "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
				inFence = !inFence
			} else if !inFence {
				line = renumberOutsideCode(line, maxN)
			}
			out = append(out, line)
		}
		b = strings.Join(out, "\n")
	}
	return strings.TrimRight(a, "\n") + "\n\n" + strings.TrimLeft(b, "\n")
}

// renumberOutsideCode adds n to every footnote label ([^3] is [^3+n]) in a line, except inside code spans: a run
// of backticks opens a span that the next run of the same length closes, and what is between them is code, where a
// literal [^1] is text and stays. A run with no closing run is literal backticks.
func renumberOutsideCode(line string, n int) string {
	if !strings.Contains(line, "[^") {
		return line
	}
	var sb strings.Builder
	for i := 0; i < len(line); {
		if line[i] == '\\' && i+1 < len(line) { // an escaped character is text: the sink writes a literal backtick as \` and a literal bracket as \[
			sb.WriteString(line[i : i+2])
			i += 2
			continue
		}
		if line[i] == '`' {
			j := i
			for j < len(line) && line[j] == '`' {
				j++
			}
			run := line[i:j]
			if k := indexRun(line, j, len(run)); k >= 0 {
				sb.WriteString(line[i : k+len(run)])
				i = k + len(run)
				continue
			}
			sb.WriteString(run)
			i = j
			continue
		}
		if m := reNoteRef.FindStringSubmatchIndex(line[i:]); m != nil && m[0] == 0 {
			v, _ := strconv.Atoi(line[i+m[2] : i+m[3]])
			sb.WriteString("[^" + strconv.Itoa(v+n) + "]")
			i += m[1]
			continue
		}
		sb.WriteByte(line[i])
		i++
	}
	return sb.String()
}

// indexRun finds, from position from, the next run of exactly n backticks.
func indexRun(s string, from, n int) int {
	for i := from; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '`' {
			j++
		}
		if j-i == n {
			return i
		}
		i = j
	}
	return -1
}

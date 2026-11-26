package export

// HTML export: the manuscript as a reading document, one self-contained file or
// a zip of one page per document with an index.
//
// It is built from the stored Markdown by text.RenderHTML, which writes only
// whitelisted elements and shows raw HTML as text, so a hostile manuscript
// cannot put a script, a form or a remote load in the file. The page also
// carries a Content-Security-Policy meta that allows nothing but its own
// style (by hash) and inline images. Fonts are the system's serif; there is no
// external URL of any kind.
//
// Deterministic like the Markdown export: the same project state gives the same
// bytes (anchors come from reading order and the title, never from ids; the
// bundle is a sorted, stored zip with a fixed time; no clock or user reaches
// the file). Each export is recorded with the ids and source hashes of the
// documents it held.
//
// What it leaves out that the Markdown export keeps, because this is a document
// to read and share rather than a copy to restore: front matter, HTML comments
// (the alternatives annotation among them) and %%private notes%% (unless
// private_notes is set). The Markdown export writes all of those verbatim.

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"path"
	"regexp"
	"strings"

	"github.com/crueber/scriobh/internal/cover"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// HTMLIn asks for an HTML export.
type HTMLIn struct {
	ProjectID    string `path:"id" json:"-" desc:"Project id."`
	Mode         string `json:"mode,omitempty" enum:"single,bundle" desc:"single: one self-contained .html file; bundle: a .zip with an index.html (title page and contents) and one page per document. Default single."`
	Notes        bool   `json:"notes,omitempty" desc:"Include the notes documents (the ones you keep for yourself), after the manuscript. Default false."`
	PrivateNotes bool   `json:"private_notes,omitempty" desc:"Keep %%private notes%%, marked as such. Default false: they are left out, with front matter and HTML comments."`
	Ghosts       string `json:"ghosts,omitempty" enum:"keep,omit" desc:"keep (default): ghosted text is in the page, undimmed. omit: it is left out."`
	// IncludeExcluded keeps the documents marked not to compile.
	IncludeExcluded bool `json:"include_excluded,omitempty" desc:"Documents marked 'do not compile' (and everything inside them) are left out by default. true keeps them."`
}

// ProjectRef names the project to export.
func (in HTMLIn) ProjectRef() string { return in.ProjectID }

func registerHTML(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "export.html", Method: "POST", Path: "/api/v1/projects/{id}/export/html", Status: 200,
		Desc: "Export the manuscript as HTML to read or share: one self-contained file, or a zip of pages with an index. Front matter, HTML comments and private notes are left out unless asked for. Recorded with a manifest of what it held.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, exportHTML)
}

// pageCSS is the one stylesheet, written into every page. It is fixed text, so
// its hash goes into the page's Content-Security-Policy and nothing else can
// style the page. System fonts only, flat colours, a print sheet.
const pageCSS = `:root{color-scheme:light dark;--bg:#FBFAF7;--fg:#1C1B18;--muted:#6B665C;--line:#DDD8CC;--accent:#2F5D50;--wash:#EFEBE0}
@media (prefers-color-scheme:dark){:root{--bg:#131311;--fg:#EDEAE3;--muted:#A19B8F;--line:#2E2D28;--accent:#7FB09A;--wash:#232320}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:1.125rem/1.7 Newsreader,"Iowan Old Style","Palatino Linotype",Palatino,Georgia,"Times New Roman",serif;-webkit-text-size-adjust:100%}
main{max-width:38rem;margin:0 auto;padding:2rem 1.25rem 5rem}
a{color:var(--accent)}
h1,h2,h3,h4,h5,h6{line-height:1.25;font-weight:600;margin:2.2em 0 .6em;overflow-wrap:anywhere}
h1{font-size:2.2rem;margin-top:0}h2{font-size:1.6rem}h3{font-size:1.3rem}h4{font-size:1.15rem}h5,h6{font-size:1rem}
p{margin:0 0 1em;overflow-wrap:break-word}
.titlepage{text-align:center;padding:2rem 0 3rem;border-bottom:1px solid var(--line);margin-bottom:2rem}
.cover{margin:0 auto 2rem;width:min(100%,17rem)}.cover svg,.cover img{display:block;width:100%;height:auto}
.title{margin:0;font-size:2.6rem}.subtitle{margin:.4rem 0 0;font-style:italic;color:var(--muted)}.author{margin:1.2rem 0 0;letter-spacing:.08em;text-transform:uppercase;font-size:.95rem}
.toc{margin:0 0 3rem}.toc h2{font-size:1rem;letter-spacing:.1em;text-transform:uppercase;color:var(--muted);margin-top:0}
.toc ol{list-style:none;margin:0;padding:0 0 0 1.1rem}.toc>ol{padding-left:0}.toc li{margin:.2rem 0}.toc a{text-decoration:none}.toc a:hover{text-decoration:underline}
.doc{margin:0 0 2rem}.doc>p:first-of-type{margin-top:0}
.doc-chapter>h2,.doc-chapter>h3,.doc-part>h2,.doc-front_matter>h2,.doc-back_matter>h2{margin-top:3rem}
hr.scene{border:0;text-align:center;margin:1.6em 0;height:1em;line-height:1}hr.scene::after{content:"* * *";color:var(--muted);letter-spacing:.4em}
blockquote{margin:1em 0;padding:0 0 0 1rem;border-left:2px solid var(--line);color:var(--muted)}
ul,ol{padding-left:1.5rem}li{margin:.2em 0}
pre{margin:1em 0;padding:.8rem 1rem;background:var(--wash);border:1px solid var(--line);border-radius:2px;overflow-x:auto;font:.9rem/1.5 ui-monospace,Menlo,Consolas,monospace;white-space:pre-wrap;overflow-wrap:anywhere}
code{font:.9em ui-monospace,Menlo,Consolas,monospace}pre code{font-size:inherit}
code.raw,pre.source{color:var(--muted)}
table{border-collapse:collapse;margin:1em 0;width:100%;font-size:.95rem}th,td{border:1px solid var(--line);padding:.35rem .6rem;text-align:left;vertical-align:top}th{background:var(--wash)}.ar{text-align:right}.ac{text-align:center}
img{max-width:100%;height:auto}.omitted{color:var(--muted);font-size:.9em}
.wiki{border-bottom:1px dotted var(--muted);text-decoration:none}a.wiki{color:inherit}
.private-note{background:var(--wash);border-bottom:1px dotted var(--muted)}
.footnotes{margin-top:2rem;padding-top:1rem;border-top:1px solid var(--line);font-size:.95rem}.footnotes p{margin:0 0 .5em}
sup.fnref a{text-decoration:none}.fnback{text-decoration:none}
.pager{display:flex;justify-content:space-between;gap:1rem;margin:3rem 0 0;padding-top:1rem;border-top:1px solid var(--line);font-size:.95rem}
.home{display:block;margin-bottom:2rem;color:var(--muted);font-size:.95rem}
@media print{body{background:#fff;color:#000;font-size:11pt}main{max-width:none;padding:0}a{color:inherit;text-decoration:none}.pager,.home{display:none}.titlepage{break-after:page;border:0}.doc-chapter>h2,.doc-chapter>h3,.doc-part>h2{break-before:page;margin-top:0}h1,h2,h3,h4{break-after:avoid}pre,blockquote,table{break-inside:avoid}.private-note{background:none}@page{margin:2cm}}
`

// pageCSP allows the page's own style (by hash) and inline images, and nothing
// else: no script, no remote load, no form, no frame, no base.
var pageCSP = func() string {
	sum := sha256.Sum256([]byte(pageCSS))
	return "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; img-src data:; base-uri 'none'; form-action 'none'"
}()

var reLang = regexp.MustCompile(`^[A-Za-z0-9-]{1,16}$`)

func esc(s string) string { return html.EscapeString(s) }

// page wraps a body fragment in the document shell.
func page(p projectRow, title, bodyHTML string) string {
	lang := "en"
	if reLang.MatchString(p.Language) {
		lang = p.Language
	}
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"" + esc(lang) + "\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta http-equiv=\"Content-Security-Policy\" content=\"" + esc(pageCSP) + "\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<meta name=\"color-scheme\" content=\"light dark\">\n")
	b.WriteString("<meta name=\"referrer\" content=\"no-referrer\">\n<meta name=\"generator\" content=\"Scríobh\">\n")
	b.WriteString("<title>" + esc(title) + "</title>\n<style>" + pageCSS + "</style>\n</head>\n<body>\n<main>\n")
	b.WriteString(bodyHTML)
	b.WriteString("</main>\n</body>\n</html>\n")
	return b.String()
}

// htmlCover is the project's own cover for the title page: the front as a JPEG, its alt text and size, and the hash of
// what the front is made of. The page holds it as a data URI, so every page keeps the one policy (img-src data: only); a
// bundle also holds the picture as a file, images/cover.jpg, for whoever wants it.
type htmlCover struct {
	data []byte
	alt  string
	w, h int
	hash string
}

// bundleCoverPath is where a bundle keeps the cover picture.
const bundleCoverPath = "images/cover.jpg"

// titlePage is the project's cover, title, subtitle and author.
func titlePage(p projectRow) string {
	var b strings.Builder
	b.WriteString("<header class=\"titlepage\">\n")
	if p.cover != nil {
		src := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(p.cover.data)
		fmt.Fprintf(&b, "<figure class=\"cover\"><img src=\"%s\" alt=\"%s\" width=\"%d\" height=\"%d\"></figure>\n", esc(src), esc(p.cover.alt), p.cover.w, p.cover.h)
	} else {
		_, svg := cover.Cached(cover.Spec{Title: p.Title, Subtitle: p.Subtitle, Author: p.Author, Kind: p.Kind, Color: p.CoverColor})
		b.WriteString("<figure class=\"cover\">" + strings.TrimSpace(string(svg)) + "</figure>\n")
	}
	b.WriteString("<h1 class=\"title\">" + esc(strings.Join(strings.Fields(p.Title), " ")) + "</h1>\n")
	if s := strings.Join(strings.Fields(p.Subtitle), " "); s != "" {
		b.WriteString("<p class=\"subtitle\">" + esc(s) + "</p>\n")
	}
	if a := strings.Join(strings.Fields(p.Author), " "); a != "" {
		b.WriteString("<p class=\"author\">" + esc(a) + "</p>\n")
	}
	b.WriteString("</header>\n")
	return b.String()
}

// ---- planning --------------------------------------------------------------------------

// item is one document as the HTML export lays it out.
type item struct {
	d      *doc
	depth  int    // nesting under other documents of the manuscript (notes are flat)
	anchor string // d<reading order>-<slug of the title>
	title  string
	empty  bool // no title and no text: nothing to show
	notes  bool
	path   string // bundle only: the page's path
}

func plan(manuscript, notes []*doc) []item {
	inMS := map[string]*doc{}
	for _, x := range manuscript {
		inMS[x.ID] = x
	}
	depthOf := map[string]int{}
	var depth func(x *doc) int
	depth = func(x *doc) int {
		if d, ok := depthOf[x.ID]; ok {
			return d
		}
		depthOf[x.ID] = 0 // a cycle cannot recurse
		d := 0
		if p, ok := inMS[x.ParentID]; ok && p != x {
			d = depth(p) + 1
		}
		depthOf[x.ID] = d
		return d
	}
	var items []item
	add := func(x *doc, d int, isNote bool) {
		title := strings.Join(strings.Fields(x.Title), " ")
		items = append(items, item{d: x, depth: d, title: title, notes: isNote,
			anchor: fmt.Sprintf("d%d-%s", len(items)+1, slug(x.Title)), empty: title == "" && strings.TrimSpace(x.Content) == ""})
	}
	for _, x := range manuscript {
		add(x, depth(x), false)
	}
	for _, x := range notes {
		add(x, 0, true)
	}
	return items
}

func kindClass(k string) string {
	if _, ok := headingLevel[k]; ok {
		return "doc-" + k
	}
	return "doc"
}

// toc is the contents list: a nested list of the documents that have a title,
// then the notes under their own heading.
func toc(items []item, href func(item) string) string {
	var b strings.Builder
	b.WriteString("<nav class=\"toc\" aria-label=\"Contents\">\n<h2>Contents</h2>\n")
	var open []bool // per open <ol>: is its last <li> still open
	closeAll := func(to int) {
		for len(open) > to {
			top := len(open) - 1
			if open[top] {
				b.WriteString("</li>\n")
			}
			b.WriteString("</ol>\n")
			open = open[:top]
		}
	}
	notes := false
	for _, it := range items {
		if it.title == "" {
			continue
		}
		if it.notes && !notes {
			notes = true
			closeAll(0)
			b.WriteString("<h2 class=\"toc-notes\">Notes</h2>\n")
		}
		d := it.depth
		if d > len(open) {
			d = len(open) // a list can only nest one level deeper than the one open
		}
		closeAll(d + 1)
		switch {
		case len(open) == d+1:
			if open[d] {
				b.WriteString("</li>\n")
				open[d] = false
			}
		default:
			b.WriteString("<ol>\n")
			open = append(open, false)
		}
		b.WriteString("<li><a href=\"" + esc(href(it)) + "\">" + esc(it.title) + "</a>")
		open[len(open)-1] = true
	}
	closeAll(0)
	b.WriteString("</nav>\n")
	return b.String()
}

// wikiMap maps the folded title of each document to its first item.
func wikiMap(items []item) map[string]int {
	m := map[string]int{}
	for i, it := range items {
		if it.title == "" {
			continue
		}
		if _, ok := m[text.FoldTitle(it.title)]; !ok {
			m[text.FoldTitle(it.title)] = i
		}
	}
	return m
}

func headingTag(level int) string {
	if level > 6 {
		level = 6
	}
	return fmt.Sprintf("h%d", level)
}

// section renders one document: its heading (when it has a title) and its text.
func section(it item, level, shift int, private bool, wiki func(string) (string, bool)) string {
	var b strings.Builder
	b.WriteString("<section class=\"doc " + kindClass(it.d.Kind) + "\" id=\"" + it.anchor + "\">\n")
	if it.title != "" {
		b.WriteString("<" + headingTag(level) + ">" + esc(it.title) + "</" + headingTag(level) + ">\n")
	}
	b.WriteString(text.RenderHTML(it.d.Content, text.HTMLOptions{IDPrefix: it.anchor + "-", HeadingShift: shift, Private: private, WikiHref: wiki}))
	b.WriteString("</section>\n")
	return b.String()
}

// ---- the op --------------------------------------------------------------------------------

func exportHTML(c *ops.Ctx, in HTMLIn) (File, error) {
	mode := in.Mode
	if mode == "" {
		mode = "single"
	}
	if mode != "single" && mode != "bundle" {
		return File{}, ops.Invalid("mode", "The mode must be single or bundle.")
	}
	if in.Ghosts != "" && in.Ghosts != "keep" && in.Ghosts != "omit" {
		return File{}, ops.Invalid("ghosts", "ghosts is keep or omit.")
	}
	p, all, err := load(c, c.DB(), in.ProjectID)
	if err != nil {
		return File{}, err
	}
	// The project's own cover takes the place of the generated one on the title page; its layout's hash is recorded
	// with the export, so a changed cover is a changed export.
	d, lay, err := coverlayout.Make(c, c.Env, in.ProjectID, coverlayout.NameFront, false)
	switch {
	case errors.Is(err, coverlayout.ErrNoPicture):
		// the layout names a picture that is gone: the generated cover stands in
	case err != nil:
		return File{}, err
	case d != nil:
		fh, _, _ := coverlayout.FrontHashOf(c, c.DB().Read(), in.ProjectID, "")
		p.cover = &htmlCover{data: d.Data, alt: lay.Alt, w: d.Width, h: d.Height, hash: fh}
	}
	all, skipped := dropNotCompiled(all, in.IncludeExcluded)
	applyGhosts(all, in.Ghosts, "", true)
	manuscript, notes := tree(all)
	if !in.Notes {
		notes = nil
	}
	var (
		body        []byte
		name        string
		contentType string
		included    []*doc
	)
	slugName := projects.Slugify(p.Title)
	switch mode {
	case "single":
		body = []byte(singleHTML(p, manuscript, notes, in.PrivateNotes))
		name, contentType = slugName+".html", "text/html; charset=utf-8"
		included = append(append(included, manuscript...), notes...)
	default:
		entries, inc := layout(manuscript, notes)
		var berr error
		body, berr = bundleHTML(p, manuscript, notes, entries, in)
		if berr != nil {
			return File{}, ops.Internal(berr)
		}
		name, contentType, included = slugName+"-html.zip", "application/zip", inc
	}
	file := newFile(name, contentType, body, len(included))
	file.Skipped = len(skipped)
	manifest := withSkipped(map[string]any{"includes_notes": in.Notes, "private_notes": in.PrivateNotes, "ghosts": ghostMode(in.Ghosts)}, skipped, true)
	if p.cover != nil {
		manifest["cover_layout"] = p.cover.hash
	}
	if err := record(c, in.ProjectID, mode, "html", file, included, manifest,
		withSkipped(map[string]any{"notes": in.Notes, "private_notes": in.PrivateNotes, "ghosts": ghostMode(in.Ghosts)}, skipped, false)); err != nil {
		return File{}, err
	}
	return file, nil
}

func singleHTML(p projectRow, manuscript, notes []*doc, private bool) string {
	items := plan(manuscript, notes)
	wm := wikiMap(items)
	wiki := func(t string) (string, bool) {
		if i, ok := wm[text.FoldTitle(t)]; ok {
			return "#" + items[i].anchor, true
		}
		return "", false
	}
	var b strings.Builder
	b.WriteString(titlePage(p))
	b.WriteString(toc(items, func(it item) string { return "#" + it.anchor }))
	b.WriteString("<article>\n")
	notesOpen := false
	for _, it := range items {
		if it.empty {
			continue
		}
		if it.notes && !notesOpen {
			notesOpen = true
			b.WriteString("<section class=\"doc doc-notes\" id=\"notes\"><h2>Notes</h2></section>\n")
		}
		level := 2 + it.depth
		if it.notes {
			level = 3
		}
		// The text sits below the chapter's heading: its H2 is one level under the chapter's, as the paged HTML has it.
		b.WriteString(section(it, level, level-1, private, wiki))
	}
	b.WriteString("</article>\n")
	return page(p, strings.Join(strings.Fields(p.Title), " "), b.String())
}

// relPath is the address of target as seen from the page at from (both slash
// paths from the bundle root).
func relPath(from, target string) string {
	var f []string
	if d := path.Dir(from); d != "." {
		f = strings.Split(d, "/")
	}
	t := strings.Split(target, "/")
	i := 0
	for i < len(f) && i < len(t)-1 && f[i] == t[i] {
		i++
	}
	return strings.Repeat("../", len(f)-i) + strings.Join(t[i:], "/")
}

func bundleHTML(p projectRow, manuscript, notes []*doc, entries []entry, in HTMLIn) ([]byte, error) {
	items := plan(manuscript, notes)
	byID := map[string]int{}
	for i, it := range items {
		byID[it.d.ID] = i
	}
	pathOf := make([]string, len(items))
	for _, e := range entries {
		pathOf[byID[e.doc.ID]] = strings.TrimSuffix(e.path, ".md") + ".html"
	}
	wm := wikiMap(items)
	home := strings.Join(strings.Fields(p.Title), " ")
	var out []entry
	for n, e := range entries {
		i := byID[e.doc.ID]
		it := items[i]
		from := pathOf[i]
		wiki := func(t string) (string, bool) {
			if j, ok := wm[text.FoldTitle(t)]; ok {
				return relPath(from, pathOf[j]), true
			}
			return "", false
		}
		var b strings.Builder
		b.WriteString("<a class=\"home\" href=\"" + esc(relPath(from, "index.html")) + "\">" + esc(home) + "</a>\n")
		b.WriteString(section(it, 1, 1, in.PrivateNotes, wiki))
		b.WriteString("<nav class=\"pager\" aria-label=\"Pages\">")
		if n > 0 {
			b.WriteString("<a rel=\"prev\" href=\"" + esc(relPath(from, pathOf[byID[entries[n-1].doc.ID]])) + "\">&larr; " + esc(labelOf(items[byID[entries[n-1].doc.ID]])) + "</a>")
		} else {
			b.WriteString("<span></span>")
		}
		if n+1 < len(entries) {
			b.WriteString("<a rel=\"next\" href=\"" + esc(relPath(from, pathOf[byID[entries[n+1].doc.ID]])) + "\">" + esc(labelOf(items[byID[entries[n+1].doc.ID]])) + " &rarr;</a>")
		}
		b.WriteString("</nav>\n")
		title := it.title
		if title == "" {
			title = home
		} else {
			title += " · " + home
		}
		out = append(out, entry{path: pathOf[i], data: []byte(page(p, title, b.String())), doc: e.doc})
	}
	var idx strings.Builder
	idx.WriteString(titlePage(p))
	idx.WriteString(toc(items, func(it item) string { return pathOf[byID[it.d.ID]] }))
	index := entry{path: "index.html", data: []byte(page(p, home, idx.String()))}
	extra := []entry{index}
	if p.cover != nil {
		extra = append(extra, entry{path: bundleCoverPath, data: p.cover.data})
	}
	return bundle("scriobh-html-bundle", p, in.Notes, out, nil, nil, extra...)
}

// labelOf is what a pager link says for a document.
func labelOf(it item) string {
	if it.title != "" {
		return it.title
	}
	return "Next"
}

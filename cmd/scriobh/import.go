package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
)

const importUsage = `usage: scriobh import <folder> [flags]

Preview importing a folder of Markdown as a new project, or with --apply create
it. The folder is read as a Scríobh mirror or export when it has a scriobh.json
or project.json, and otherwise as a generic Markdown folder: folders become
parts, NN-title.md files become chapters. Nothing is written without --apply.

  --apply          create the project (or add the documents) after the preview
  --owner EMAIL    the person who will own the project; needed when more than
                   one person has signed in
  --into ID        add to an existing project, after what is already there
  --parent ID      with --into: a folder of that project to add into
  --title TEXT     project title (default: from the files, or the folder name)
  --kind KIND      project kind (default: from the files, or blank)
  --split-h2       turn each file's second-level headings into scenes
  --layout NAME    obsidian reads the folder as an Obsidian vault (never guessed
                   from the files); scrivener, wordpress and ghost name the
                   source when it is not recognised by its own markers
  --docx-parts     for a .docx: the lowest heading level is a part and the next
                   a chapter (default: the headings, styles and "Chapter 1" lines
                   are told apart for you, parts included)
  --docx-split auto|heading1|heading2|style|label|page_breaks|section_breaks|numerals|size|none
                   for a .docx: how it is cut into chapters. auto (default) uses
                   strong boundaries only: headings, a style named for chapters,
                   "Chapter 3" lines that look like titles. Page breaks, section
                   breaks and lone numerals are only listed (with their ordinals)
                   and cut when chosen. size makes pieces of --docx-split-words;
                   none leaves one document
  --docx-split-style NAME   with --docx-split style: the paragraph style's name
  --docx-split-words N      with --docx-split size: about N words a piece
                   (500 to 20000; default 5000)
  --docx-break N   start a chapter at the paragraph with ordinal N (the preview
                   lists them); may be given more than once
  --docx-changes suggest|accept|reject   for a .docx with tracked changes: suggest
                   (default) imports the text as its author left it and proposes
                   each change as a pending suggestion by its Word author; accept
                   imports the final view; reject the original. Word comments
                   become comments by their Word authors in every mode
  --scrivener-research notes|skip   a Scrivener project's Research folder: notes
                   (default) become research notes, skip leaves them out
  --scrivener-notes private|skip    its documents' notes: private notes after the
                   text (default), or left out
  --wp-status published|all         a WordPress export: published (default), or
                   drafts, pending, scheduled and private posts too (marked do
                   not compile)
  --wp-types posts|pages|both       a WordPress export: posts (default), pages, both
  --ghost-status published|all, --ghost-types posts|pages|both   the same for a Ghost export
  --pictures import|skip            pictures that come with the source (a
                   Scrivener project's files, the images folder in a zip of a
                   Ghost or WordPress export): import (default) stores them,
                   cleaned, as attachments; skip marks them [image]
  --scrivener-sheets notes|entities a Scrivener project's Characters and Places:
                   research notes (default), or entities of the project's
                   primary Compendium (a new project gets one)
  --epub-sections auto|toc|files|headings   an EPUB: how its text is cut into
                   chapters: by its table of contents, by file, or by heading
                   (default auto: the contents, else the files, else headings)
  --epub-pages skip|keep            an EPUB's title page: left out (default; the
                   project's own title and author replace it) or kept as front
                   matter
  --edition-label TEXT              an EPUB: what the edition is called (default:
                   the book's title and year)

The first argument may also be a Word document (notes.docx): its headings become
chapters, text before the first heading becomes front matter, and what cannot be
imported (comments, images, tracked changes) is reported.

It may be an EPUB (book.epub, version 2 or 3): the reading order, metadata,
cover, pictures and footnotes are read, the chapters are proposed from its
table of contents, and the book's file is kept, with its SHA-256, as an
edition of the project. A book with DRM, or a fixed layout, is refused.

It may be a PDF with a text layer (book.pdf, up to 32 MiB): chapters come from
its outline or from headings set larger than the text, running heads, page
numbers and a contents page are removed, and the preview says what could not be
recovered, with a confidence for each page. A scanned or encrypted PDF is refused. A PDF with no outline and no headings is
cut by the same detector as a Word document and a text file.

It may be a plain text file (book.txt, UTF-8, up to 16 MiB): blank lines are
paragraph breaks, a line of * * * is a scene break, and chapters are found by
the same detector as a Word document's, reading only the words. A "Chapter 3"
line in capitals or bold is a chapter; a plain one is listed for you to take with
--docx-split label, or --docx-break N for one of them.

It may be a Scrivener project (Name.scriv, a folder or a .zip of it: binder to
manuscript tree, synopses, statuses, notes and annotations as private notes,
Research as research notes, pictures as attachments), a WordPress export (export.xml, the WXR file) or a
Ghost export (export.json). Each is recognised by its own markers; what cannot
be imported is reported. Nothing in them is ever fetched.

Limits: 2,000 Markdown files, 2 MiB each, 32 MiB together. Hidden folders,
non-Markdown files and links are skipped and listed. Stop the server first; this
command opens the database directly.
`

// intList is a flag that may be given more than once, each a whole number.
type intList []int

func (l *intList) String() string { return fmt.Sprint([]int(*l)) }
func (l *intList) Set(v string) error {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 {
		return fmt.Errorf("%q is not a paragraph ordinal (1 or more)", v)
	}
	*l = append(*l, n)
	return nil
}

// importCmd runs `scriobh import`.
func importCmd(args []string, stdout, stderr io.Writer) int {
	var folder string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		folder, args = args[0], args[1:]
	}
	fl := flag.NewFlagSet("import", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	apply := fl.Bool("apply", false, "")
	owner := fl.String("owner", "", "")
	into := fl.String("into", "", "")
	parent := fl.String("parent", "", "")
	title := fl.String("title", "", "")
	kind := fl.String("kind", "", "")
	splitH2 := fl.Bool("split-h2", false, "")
	layout := fl.String("layout", "", "")
	docxParts := fl.Bool("docx-parts", false, "")
	docxChanges := fl.String("docx-changes", "", "")
	docxSplit := fl.String("docx-split", "", "")
	docxStyle := fl.String("docx-split-style", "", "")
	docxWords := fl.Int("docx-split-words", 0, "")
	var breaks intList
	fl.Var(&breaks, "docx-break", "")
	scrivResearch := fl.String("scrivener-research", "", "")
	scrivNotes := fl.String("scrivener-notes", "", "")
	wpStatus := fl.String("wp-status", "", "")
	wpTypes := fl.String("wp-types", "", "")
	ghostStatus := fl.String("ghost-status", "", "")
	ghostTypes := fl.String("ghost-types", "", "")
	pictures := fl.String("pictures", "", "")
	sheets := fl.String("scrivener-sheets", "", "")
	epubSections := fl.String("epub-sections", "", "")
	epubPages := fl.String("epub-pages", "", "")
	editionLabel := fl.String("edition-label", "", "")
	if err := fl.Parse(args); err != nil || folder == "" || fl.NArg() > 0 {
		fmt.Fprint(stderr, importUsage)
		return 2
	}
	if *parent != "" && *into == "" {
		fmt.Fprintln(stderr, "scriobh: --parent needs --into")
		return 2
	}
	switch *layout {
	case "", "auto", "obsidian", "scrivener", "wordpress", "ghost":
	default:
		fmt.Fprintln(stderr, "scriobh: --layout is obsidian, scrivener, wordpress or ghost")
		return 2
	}
	var files []imports.File
	var docx, pdf, text, epub, zipData []byte
	var name string
	var hiddenDirs int
	var err error
	lower := strings.ToLower(folder)
	if st, serr := os.Stat(folder); serr == nil && st.Mode().IsRegular() {
		limit := int64(imports.MaxBody / 2)
		switch {
		case strings.HasSuffix(lower, ".docx"), strings.HasSuffix(lower, ".zip"):
		case strings.HasSuffix(lower, ".epub"):
			limit = editions.MaxFileBytes
		case strings.HasSuffix(lower, ".pdf"):
			limit = imports.MaxPDF
		case strings.HasSuffix(lower, ".txt"), strings.HasSuffix(lower, ".text"):
			limit = imports.MaxText
		case strings.HasSuffix(lower, ".xml"), strings.HasSuffix(lower, ".json"):
			limit = imports.ExportFileLimit
		default:
			fmt.Fprintln(stderr, "scriobh: a file to import is a .docx, a .pdf, a .txt, an .epub, a .zip, or a WordPress (.xml) or Ghost (.json) export; for Markdown, name the folder")
			return 2
		}
		if st.Size() > limit {
			fmt.Fprintln(stderr, "scriobh: that file is larger than the import limit")
			return 1
		}
		b, rerr := os.ReadFile(folder)
		if rerr != nil {
			fmt.Fprintln(stderr, "scriobh:", rerr)
			return 1
		}
		name = filepath.Base(folder)
		switch {
		case strings.HasSuffix(lower, ".docx"):
			docx = b
		case strings.HasSuffix(lower, ".epub"):
			epub = b
		case strings.HasSuffix(lower, ".pdf"):
			pdf = b
		case strings.HasSuffix(lower, ".txt"), strings.HasSuffix(lower, ".text"):
			text = b
		case strings.HasSuffix(lower, ".zip"):
			zipData = b
		default:
			files = []imports.File{{Path: name, Data: b, Size: int64(len(b))}}
		}
	} else {
		want := ""
		switch {
		case *layout == "scrivener" || *layout == "wordpress" || *layout == "ghost":
			want = *layout
		case (*layout == "" || *layout == "auto") && hasScrivx(folder):
			want = "scrivener"
		}
		files, name, hiddenDirs, err = readFolder(folder, want)
		if err != nil {
			fmt.Fprintln(stderr, "scriobh:", err)
			return 1
		}
	}
	headings := ""
	if *docxParts {
		headings = imports.DocxParts
	}
	split := ""
	if *splitH2 {
		split = "h2"
	}

	restrictUmask()
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	log := logger(cfg, stderr)
	release, err := db.Lock(cfg.DataDir)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	defer release()
	d, err := openDB(cfg, log)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	defer d.Close()
	who, err := principalFor(d, *owner)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	reg := registry.Build(d, events.NewStore(d, log), cfg.DataDir, log)
	ctx := context.Background()

	var pv imports.PreviewOut
	if *into == "" {
		out, err := reg.Call(ctx, who, "import.preview", imports.PreviewIn{Name: name, Files: files, Zip: zipData, Docx: docx, Pdf: pdf, Text: text, Epub: epub, EpubSections: *epubSections, EpubPages: *epubPages, Layout: *layout, DocxHeadings: headings, DocxChanges: *docxChanges, DocxSplit: *docxSplit, DocxSplitStyle: *docxStyle, DocxSplitWords: *docxWords, DocxBreaks: breaks, SplitScenes: split,
			ScrivResearch: *scrivResearch, ScrivNotes: *scrivNotes, WPStatus: *wpStatus, WPTypes: *wpTypes, GhostStatus: *ghostStatus, GhostTypes: *ghostTypes, Pictures: *pictures, ScrivSheets: *sheets})
		if err != nil {
			fmt.Fprintln(stderr, "scriobh:", err)
			return 1
		}
		pv = out.(imports.PreviewOut)
	} else {
		out, err := reg.Call(ctx, who, "import.append_preview", imports.AppendPreviewIn{ProjectID: *into, Name: name, Files: files, Zip: zipData, Docx: docx, Pdf: pdf, Text: text, Epub: epub, EpubSections: *epubSections, EpubPages: *epubPages, Layout: *layout, DocxHeadings: headings, DocxChanges: *docxChanges, DocxSplit: *docxSplit, DocxSplitStyle: *docxStyle, DocxSplitWords: *docxWords, DocxBreaks: breaks, SplitScenes: split,
			ScrivResearch: *scrivResearch, ScrivNotes: *scrivNotes, WPStatus: *wpStatus, WPTypes: *wpTypes, GhostStatus: *ghostStatus, GhostTypes: *ghostTypes, Pictures: *pictures, ScrivSheets: *sheets})
		if err != nil {
			fmt.Fprintln(stderr, "scriobh:", err)
			return 1
		}
		pv = out.(imports.PreviewOut)
	}
	printPreview(stdout, pv, name, hiddenDirs, *into != "")
	if pv.Documents == 0 {
		return 1
	}
	if !*apply {
		fmt.Fprintln(stdout, "\nNothing was written. Run again with --apply to import.")
		return 0
	}
	var res imports.ApplyOut
	if *into == "" {
		out, err := reg.Call(ctx, who, "import.apply", imports.ApplyIn{Token: pv.Token, EditionLabel: *editionLabel, Name: name, Files: files, Zip: zipData, Docx: docx, Pdf: pdf, Text: text, Epub: epub, EpubSections: *epubSections, EpubPages: *epubPages, Layout: *layout, DocxHeadings: headings, DocxChanges: *docxChanges, DocxSplit: *docxSplit, DocxSplitStyle: *docxStyle, DocxSplitWords: *docxWords, DocxBreaks: breaks, SplitScenes: split, Title: *title, Kind: *kind,
			ScrivResearch: *scrivResearch, ScrivNotes: *scrivNotes, WPStatus: *wpStatus, WPTypes: *wpTypes, GhostStatus: *ghostStatus, GhostTypes: *ghostTypes, Pictures: *pictures, ScrivSheets: *sheets})
		if err != nil {
			fmt.Fprintln(stderr, "scriobh: nothing was imported:", err)
			return 1
		}
		res = out.(imports.ApplyOut)
		fmt.Fprintf(stdout, "\nCreated project %q (%s): %d documents, %d words.\n", res.Title, res.ProjectID, res.Documents, res.Words)
		if res.ResearchNotes > 0 {
			fmt.Fprintf(stdout, "Research: %d notes and folders.\n", res.ResearchNotes)
		}
		if res.Pictures > 0 || res.Entities > 0 {
			fmt.Fprintf(stdout, "Pictures: %d. Entities: %d.\n", res.Pictures, res.Entities)
		}
		if res.EditionID != "" {
			fmt.Fprintf(stdout, "The book is kept, as it was received, as edition %s.\n", res.EditionID)
		}
	} else {
		out, err := reg.Call(ctx, who, "import.append", imports.AppendIn{ProjectID: *into, ParentID: *parent, Token: pv.Token, EditionLabel: *editionLabel, Name: name, Files: files, Zip: zipData, Docx: docx, Pdf: pdf, Text: text, Epub: epub, EpubSections: *epubSections, EpubPages: *epubPages, Layout: *layout, DocxHeadings: headings, DocxChanges: *docxChanges, DocxSplit: *docxSplit, DocxSplitStyle: *docxStyle, DocxSplitWords: *docxWords, DocxBreaks: breaks, SplitScenes: split,
			ScrivResearch: *scrivResearch, ScrivNotes: *scrivNotes, WPStatus: *wpStatus, WPTypes: *wpTypes, GhostStatus: *ghostStatus, GhostTypes: *ghostTypes, Pictures: *pictures, ScrivSheets: *sheets})
		if err != nil {
			fmt.Fprintln(stderr, "scriobh: nothing was imported:", err)
			return 1
		}
		res = out.(imports.ApplyOut)
		fmt.Fprintf(stdout, "\nAdded %d documents (%d words) to project %s.\n", res.Documents, res.Words, res.ProjectID)
		if res.ResearchNotes > 0 {
			fmt.Fprintf(stdout, "Research: %d notes and folders.\n", res.ResearchNotes)
		}
		if res.Pictures > 0 || res.Entities > 0 {
			fmt.Fprintf(stdout, "Pictures: %d. Entities: %d.\n", res.Pictures, res.Entities)
		}
		if res.EditionID != "" {
			fmt.Fprintf(stdout, "The book is kept, as it was received, as edition %s.\n", res.EditionID)
		}
	}
	return 0
}

// readFolder lists a local folder for the importer. It does not follow
// symbolic links, skips hidden folders (and says how many), sends the bytes of
// Markdown and manifests under the size cap, and lists everything else by size
// only so the importer can report it.
func readFolder(root, layout string) (files []imports.File, name string, hiddenDirs int, err error) {
	maxFiles := imports.MaxListed
	if layout == "scrivener" {
		maxFiles = imports.MaxScrivenerEntries
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, "", 0, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, "", 0, err
	}
	if !st.IsDir() {
		return nil, "", 0, fmt.Errorf("%s is not a folder", root)
	}
	name = filepath.Base(abs)
	err = filepath.WalkDir(abs, func(p string, e fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if p == abs {
			return nil
		}
		rel, rerr := filepath.Rel(abs, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if e.IsDir() {
			if strings.HasPrefix(e.Name(), ".") {
				hiddenDirs++
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= maxFiles {
			return fmt.Errorf("the folder holds more than %d files; send only the Markdown", maxFiles)
		}
		if e.Type()&fs.ModeSymlink != 0 || !e.Type().IsRegular() {
			files = append(files, imports.File{Path: rel, Size: -1})
			return nil
		}
		info, ierr := e.Info()
		if ierr != nil {
			return ierr
		}
		lower := strings.ToLower(rel)
		wanted := strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown") || lower == "scriobh.json" || lower == "project.json"
		limit := int64(imports.MaxFileBytes)
		if layout != "" {
			limit, wanted = imports.WantFile(layout, lower)
		}
		if !wanted || info.Size() > limit {
			files = append(files, imports.File{Path: rel, Size: info.Size()})
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		files = append(files, imports.File{Path: rel, Data: b, Size: info.Size()})
		return nil
	})
	return files, name, hiddenDirs, err
}

// hasScrivx says a folder is a Scrivener project: a .scrivx file in it.
func hasScrivx(dir string) bool {
	es, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range es {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".scrivx") {
			return true
		}
	}
	return false
}

// principalFor resolves the person an offline command acts as: --owner by
// email, or the only person who has signed in.
func principalFor(d *db.DB, email string) (ops.Principal, error) {
	var id, role string
	var err error
	if email != "" {
		err = d.Read().QueryRow(`SELECT id, role FROM users WHERE email = ? COLLATE NOCASE AND deleted_at IS NULL`, email).Scan(&id, &role)
		if errors.Is(err, sql.ErrNoRows) {
			return ops.Principal{}, fmt.Errorf("no one with the email %s has signed in", email)
		}
	} else {
		var n int
		if err = d.Read().QueryRow(`SELECT count(*) FROM users WHERE deleted_at IS NULL`).Scan(&n); err != nil {
			return ops.Principal{}, err
		}
		if n != 1 {
			return ops.Principal{}, fmt.Errorf("%d people have signed in; say who owns this with --owner EMAIL", n)
		}
		err = d.Read().QueryRow(`SELECT id, role FROM users WHERE deleted_at IS NULL`).Scan(&id, &role)
	}
	if err != nil {
		return ops.Principal{}, err
	}
	return ops.Principal{UserID: id, Role: role, Via: "cli"}, nil
}

// printBreaks says how a Word document was cut and what else could be, with the ordinals --docx-break takes.
func printBreaks(w io.Writer, pv imports.PreviewOut) {
	b := pv.Breaks
	if b == nil {
		return
	}
	switch b.ChosenBy {
	case "none_found":
		fmt.Fprintf(w, "No chapter breaks were found by heading, style or \"Chapter\" label: one document of %d words.\n", b.Words)
	case "none":
		fmt.Fprintln(w, "Left as one document (--docx-split none).")
	case "explicit":
		fmt.Fprintf(w, "Cut by --docx-split %s: %d chapters.\n", b.Rule, b.Chapters)
	default:
		fmt.Fprintf(w, "Cut by %s (auto): %d chapters", b.ChosenBy, b.Chapters)
		if b.Parts > 0 {
			fmt.Fprintf(w, ", %d of them parts", b.Parts)
		}
		fmt.Fprintln(w, ".")
	}
	for _, o := range b.Options {
		if o.Rule == "style" {
			fmt.Fprintf(w, "  --docx-split style --docx-split-style %q: %d\n", o.Style, o.Count)
			continue
		}
		fmt.Fprintf(w, "  --docx-split %s: %d\n", o.Rule, o.Count)
	}
	for _, l := range pv.LeftOut {
		fmt.Fprintf(w, "Left out: %s (%s, %d words)\n", l.Title, l.Why, l.Words)
	}
	if len(b.Suggestions) > 0 {
		fmt.Fprintf(w, "Possible chapter starts (%d), by --docx-break ORDINAL:\n", b.SuggestionsTotal)
		for i, s := range b.Suggestions {
			if i >= 40 {
				fmt.Fprintf(w, "  ... and %d more\n", b.SuggestionsTotal-40)
				break
			}
			fmt.Fprintf(w, "  %d  %s  %q\n", s.Ordinal, s.Rule, s.Line)
		}
	}
	fmt.Fprint(w, "\n")
}

func printPreview(w io.Writer, pv imports.PreviewOut, name string, hiddenDirs int, into bool) {
	layout := "generic Markdown folder"
	switch pv.Layout {
	case imports.LayoutScriobh:
		layout = "Scríobh layout (" + pv.ManifestFile + ")"
	case imports.LayoutObsidian:
		layout = "Obsidian vault"
	case imports.LayoutDocx:
		layout = "Word document"
	case imports.LayoutPdf:
		layout = "PDF with a text layer"
	case imports.LayoutScrivener:
		layout = "Scrivener project"
	case imports.LayoutWordPress:
		layout = "WordPress export"
	case imports.LayoutGhost:
		layout = "Ghost export"
	case imports.LayoutEpub:
		layout = "EPUB"
	case imports.LayoutText:
		layout = "plain text file"
	}
	fmt.Fprintf(w, "%s, read as a %s.\n", name, layout)
	if !into {
		fmt.Fprintf(w, "Project: %s (%s)\n", pv.Project.Title, pv.Project.Kind)
	}
	fmt.Fprintf(w, "%d documents, %d words", pv.Documents, pv.Words)
	if pv.ResearchTotal > 0 {
		cands := 0
		for _, r := range pv.Research {
			if r.Candidate != "" {
				cands++
			}
		}
		fmt.Fprintf(w, ", %d research notes and folders", pv.ResearchTotal)
		if cands > 0 {
			fmt.Fprintf(w, " (%d look like character or place sheets: kept as research notes)", cands)
		}
	}
	fmt.Fprint(w, "\n")
	if a := pv.Already; a != nil {
		fmt.Fprintf(w, "Already imported: this same file went into the project on %s, and %d of its documents are still there. Importing it again adds the text a second time. Take the earlier one back with: scriobh-cli import undo PROJECT %s\n", a.ImportedAt, a.Documents, a.SourceID)
	}
	printBreaks(w, pv)
	if e := pv.Epub; e != nil {
		fmt.Fprintf(w, "EPUB %s, cut by %s", e.Version, e.Strategy)
		if e.Contents != "" && e.Contents != "none" {
			fmt.Fprintf(w, " (contents: %s)", e.Contents)
		}
		fmt.Fprintf(w, "; %d footnotes", e.Footnotes)
		if e.Identifier != "" {
			fmt.Fprintf(w, "; identifier %s", e.Identifier)
		}
		fmt.Fprintf(w, "\nEdition label: %s\n", e.Label)
	}
	fmt.Fprint(w, "\n")
	depth := map[string]int{"": 0}
	shown := 0
	for _, n := range pv.Nodes {
		d := depth[n.ParentKey]
		depth[n.Key] = d + 1
		if shown++; shown > 200 {
			fmt.Fprintf(w, "... and %d more\n", len(pv.Nodes)-200)
			break
		}
		note := ""
		if n.Headings > 0 && !n.Split {
			note = fmt.Sprintf("  [%d H2 headings: --split-h2 makes them scenes]", n.Headings)
		}
		fmt.Fprintf(w, "%s%-13s %s (%d words)%s\n", strings.Repeat("  ", d), n.Kind, n.Title, n.Words, note)
	}
	if pv.SkippedTotal > 0 || hiddenDirs > 0 {
		fmt.Fprintf(w, "\nSkipped: %d files", pv.SkippedTotal)
		if hiddenDirs > 0 {
			fmt.Fprintf(w, ", and %d hidden folder(s) were not read", hiddenDirs)
		}
		fmt.Fprintln(w)
		reasons := make([]string, 0, len(pv.SkippedByReason))
		for r := range pv.SkippedByReason {
			reasons = append(reasons, r)
		}
		sort.Strings(reasons)
		for _, r := range reasons {
			fmt.Fprintf(w, "  %d  %s\n", pv.SkippedByReason[r], r)
		}
	}
	for _, m := range pv.Warnings {
		fmt.Fprintln(w, "warning:", m)
	}
	for _, n := range pv.Nodes {
		for _, m := range n.Warnings {
			fmt.Fprintf(w, "warning: %s: %s\n", n.Title, m)
		}
	}
}

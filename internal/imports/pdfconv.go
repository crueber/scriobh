package imports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/pdfread"
)

// PDF import: a PDF with a text layer becomes the folder of Markdown files the
// generic importer reads (as a .docx does), so the preview, the edits, the
// token, the caps and the provenance are the folder import's own. The PDF is
// read by internal/pdfread with limits on everything a file can cost; this file
// makes the Markdown and the words of the fidelity report.
//
// What a PDF becomes:
//
//	the outline (bookmarks), when it has at least two entries that resolve,
//	else headings by size    chapters (and parts, when the top level owns chapters)
//	paragraphs               Markdown paragraphs; a paragraph that runs across a
//	                         page or column break is one paragraph; a word split by
//	                         a hyphen at a line's end is joined only when safe
//	bold, italic             kept (from the font's name and flags)
//	scene ornaments, a lone  the canonical scene break (* * *); a gap of more than
//	#, * * *                 2.4 lines mid-page is also read as one and counted
//	foot notes               Markdown footnotes when the marker in the text is found
//	table-like rows          flattened to one paragraph per row (and reported)
//	running heads, feet,     removed by the rule the report states
//	page numbers, contents
//	pictures                 not imported (counted and reported)
//
// Never kept: scanned pages (the file is refused when most pages are pictures),
// encrypted files (refused whatever the password), JavaScript, launch actions
// and attachments (counted, never run, opened or extracted).

// MaxPDF is the largest PDF read.
const MaxPDF = 32 << 20

func pdfToFiles(ctx context.Context, data []byte, name string) (files []File, pre *prepass, err error) {
	// The HTTP binding recovers from a panic; the command line does not. A file
	// that breaks this side is refused in both.
	defer func() {
		if r := recover(); r != nil {
			files, pre, err = nil, nil, errors.New("the PDF could not be read")
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	// Everything from the bytes to the chapters is done by a supervised child process (pdfchild.go).
	co, err := readPDF(ctx, data)
	if err != nil {
		return nil, nil, pdfErr(err)
	}
	res, info := co.Result, co.Info
	files, noteN, err := pdfMarkdown(res)
	if err != nil {
		return nil, nil, err
	}
	pi := &ProjectInfo{Title: res.Title, Subtitle: res.Subtitle, Author: res.Author, Language: info.Lang}
	pi.Title, _ = cleanLine(pi.Title, 200)
	pi.Subtitle, _ = cleanLine(pi.Subtitle, 200)
	pi.Author, _ = cleanLine(pi.Author, 200)
	if l := pi.Language; l == "" || len(l) > 16 || strings.ContainsAny(l, " <>\"'") {
		pi.Language = ""
	}
	sum := sha256.Sum256(data)
	rep := &res.Report
	var repMap map[string]any
	if b, e := json.Marshal(rep); e == nil {
		_ = json.Unmarshal(b, &repMap)
	}
	pdfMeta := map[string]any{"report": repMap, "pdf_version": info.Version, "producer": info.Producer, "creator": info.Creator, "tagged": info.Tagged,
		"xref_rebuilt": info.RebuiltXref, "javascript_actions_ignored": info.JavaScript, "other_actions_ignored": info.Launch, "embedded_files_not_read": info.EmbeddedFiles,
		"annotations_ignored": info.Annotations, "damaged_streams": info.DamagedStreams, "footnotes_written": noteN, "bytes": len(data), "name": folderName(name)}
	pre = &prepass{layout: LayoutPdf, srcKind: "pdf", srcHash: hex.EncodeToString(sum[:]), project: pi, meta: map[string]any{"pdf": pdfMeta}}
	pre.warnings = pdfWarnings(res, info)
	return files, pre, nil
}

// pdfWarnings says in words what the file could not give.
func pdfWarnings(res *pdfread.Result, info pdfread.Info) []string {
	r := &res.Report
	var w []string
	add := func(n int, format string, args ...any) {
		if n > 0 {
			w = append(w, fmt.Sprintf(format, args...))
		}
	}
	w = append(w, fmt.Sprintf("Read %d page(s) of a PDF (%d with text). Overall confidence %d%%; %d page(s) are under 70%%. Only the text layer is read: no character recognition is done.", r.Pages, r.TextPages, r.Confidence, r.LowConfidence))
	switch r.ChapterSource {
	case "outline":
		w = append(w, fmt.Sprintf("Chapters were taken from the PDF's outline (%d of %d bookmarks).", r.OutlineUsed, r.OutlineItems))
	case "headings":
		w = append(w, "The PDF has no usable outline: chapters were proposed from headings set larger than the body text. Check them before creating the project.")
	default:
		w = append(w, "No chapters could be told apart (no outline, no headings set larger than the text): the text is one document. Split it by hand after import.")
	}
	add(r.PictureOnlyTotal, "%d page(s) have no text layer (pictures only) and were left out.", r.PictureOnlyTotal)
	add(r.Images, "%d picture(s) on the pages were not imported; the text around them was.", r.Images)
	add(r.HeadLines+r.PageNumbers, "%d running head or foot line(s) and %d page number(s) were removed. The rule: %s", r.HeadLines, r.PageNumbers, r.HeadRule)
	add(r.TOCLines, "%d line(s) of a table of contents on page(s) %s were removed.", r.TOCLines, pageList(r.TOCPages))
	add(r.TwoColumnPages, "%d page(s) were read as two columns, left then right.", r.TwoColumnPages)
	add(len(r.AmbiguousColumns), "%d page(s) may be set in columns or a table and were read as one column: page(s) %s.", len(r.AmbiguousColumns), pageList(r.AmbiguousColumns))
	add(r.TableRows, "%d table row(s) were flattened to one paragraph each (cells separated by |) on page(s) %s; Scríobh does not rebuild tables from a PDF.", r.TableRows, pageList(r.TablePages))
	add(r.Notes, "%d footnote(s) were found: %d linked to their marker in the text, %d kept as a paragraph after the text they sit under (the marker was not found).", r.Notes, r.NotesLinked, r.NotesUnlinked)
	add(r.NoteMarkersLeft, "%d raised number(s) or symbol(s) that were not footnote markers became ordinary text.", r.NoteMarkersLeft)
	add(r.HyphenJoined, "%d word(s) split by a hyphen at a line's end were joined; %d hyphen(s) were kept because the word may be a real compound.", r.HyphenJoined, r.HyphenKept)
	add(r.GapSceneBreaks, "%d gap(s) of more than two lines between paragraphs were read as scene breaks.", r.GapSceneBreaks)
	add(len(r.RTLPages), "Right-to-left text on page(s) %s is kept in the order the file stores it, which is not always the reading order.", pageList(r.RTLPages))
	add(r.RotatedChars+r.VerticalChars, "%d character(s) set sideways, upside down or in vertical writing were left out.", r.RotatedChars+r.VerticalChars)
	add(r.UnmappedChars, "%d character(s) had no known meaning (a font without a character map) and were left out.", r.UnmappedChars)
	add(len(r.OCRPages), "Page(s) %s carry an invisible OCR text layer over a picture: the text is as accurate as the recogniser was.", pageList(r.OCRPages))
	add(len(r.TruncatedPages), "A limit was reached on page(s) %s; part of them was not read.", pageList(r.TruncatedPages))
	add(info.JavaScript+info.Launch, "%d JavaScript or launch action(s) in the PDF were ignored. Nothing in a PDF is run.", info.JavaScript+info.Launch)
	add(info.EmbeddedFiles, "%d file(s) attached to the PDF were not extracted.", info.EmbeddedFiles)
	add(info.DamagedStreams, "%d stream(s) of the PDF are damaged and were read as far as they went.", info.DamagedStreams)
	if info.RebuiltXref {
		w = append(w, "The PDF's cross-reference table was missing or wrong and was rebuilt by scanning the file; text may be missing.")
	}
	if info.PageTreeProblem != "" {
		w = append(w, "The PDF: "+info.PageTreeProblem+".")
	}
	if r.BoldRuns+r.ItalicRuns > 0 {
		w = append(w, fmt.Sprintf("Bold (%d run(s)) and italic (%d) were kept; underline, colour and size are not.", r.BoldRuns, r.ItalicRuns))
	} else {
		w = append(w, "No bold or italic was found in the text (or the fonts do not say which they are).")
	}
	return w
}

func pageList(p []int) string {
	if len(p) == 0 {
		return "none"
	}
	var b strings.Builder
	for i, n := range p {
		if i >= 12 {
			fmt.Fprintf(&b, " and %d more", len(p)-12)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

// pdfMarkdown makes the Markdown files from the recovered chapters, through the
// same blocks the Word importer composes files from.
func pdfMarkdown(res *pdfread.Result) ([]File, int, error) {
	c := newDocxConv()
	c.mode = DocxAccept
	parts := false
	for _, ch := range res.Chapters {
		if ch.Part {
			parts = true
		}
	}
	noteN := 0
	// register the notes first: a reference renders only when its text is known
	ids := map[string]string{}
	register := func(bs []pdfread.Block) {
		for _, b := range bs {
			for _, n := range b.Notes {
				if _, ok := ids[n.Label]; ok {
					continue
				}
				noteN++
				id := strconv.Itoa(noteN)
				ids[n.Label] = id
				t := strings.TrimSpace(c.render(pdfSegs(n.Runs, nil, false)))
				if t == "" {
					t = "(empty)"
				}
				c.noteText[id] = []string{escLine(strings.ReplaceAll(t, "\n", " "))}
				c.noteSize += len(t)
			}
		}
	}
	register(res.Front)
	for _, ch := range res.Chapters {
		register(ch.Blocks)
	}
	listID, prevItem, prevOrdered := 0, false, false
	add := func(bs []pdfread.Block, headingBase int) {
		for _, b := range bs {
			switch b.Kind {
			case pdfread.BlockScene:
				c.add(mdBlock{kind: bScene}, 0)
			case pdfread.BlockHeading:
				t := strings.Join(strings.Fields(b.Text()), " ")
				if t != "" {
					c.add(mdBlock{kind: bHeading, level: min(6, b.Level+headingBase), text: t}, 0)
				}
			case pdfread.BlockCode:
				for _, ln := range strings.Split(b.Text(), "\n") {
					c.add(mdBlock{kind: bCode, text: strings.TrimRight(ln, " ")}, 0)
				}
			case pdfread.BlockItem:
				c.noteSeen = nil
				text := strings.TrimSpace(c.render(pdfSegs(b.Runs, ids, !res.BodyMono)))
				notes := c.noteSeen
				c.noteSeen = nil
				if text != "" {
					if !(prevItem && prevOrdered == b.Ordered) {
						listID++
					}
					c.add(mdBlock{kind: bList, text: escLines(text), ordered: b.Ordered, listNum: "pdf" + strconv.Itoa(listID), start: b.Num, notes: notes}, 0)
				}
			default:
				c.noteSeen = nil
				segs := pdfSegs(b.Runs, ids, !res.BodyMono)
				text := strings.TrimSpace(c.render(segs))
				notes := c.noteSeen
				c.noteSeen = nil
				if text != "" {
					kind := bPara
					if b.Kind == pdfread.BlockQuote {
						kind = bQuote
					}
					c.add(mdBlock{kind: kind, text: escLines(text), notes: notes}, 0)
				}
			}
			prevItem, prevOrdered = b.Kind == pdfread.BlockItem, b.Ordered
			if c.overflow {
				return
			}
		}
	}
	add(res.Front, 0)
	for _, ch := range res.Chapters {
		lv := 1
		if parts && !ch.Part {
			lv = 2
		}
		c.add(mdBlock{kind: bHeading, level: lv, text: strings.Join(strings.Fields(ch.Title), " ")}, 0)
		base := 0
		if parts {
			base = 1
		}
		add(ch.Blocks, base)
	}
	if c.overflow {
		return nil, 0, fmt.Errorf("the PDF holds more text than the %d MB import limit", MaxTotalBytes>>20)
	}
	mode := DocxChapters
	if parts {
		mode = DocxParts
	}
	files, _, err := c.files("", mode)
	if err != nil {
		return nil, 0, err
	}
	return files, noteN, nil
}

// pdfSegs turns runs into the segments the Word converter renders.
func pdfSegs(runs []pdfread.Run, ids map[string]string, code bool) []seg {
	var out []seg
	for _, r := range runs {
		if r.Note != "" {
			if id, ok := ids[r.Note]; ok {
				out = append(out, seg{note: id})
			}
			continue
		}
		if r.Text == "" {
			continue
		}
		out = append(out, seg{text: r.Text, b: r.Bold, i: r.Italic, code: code && r.Mono && strings.TrimSpace(r.Text) != ""})
	}
	return out
}

// pdfErr turns a cancelled or timed-out read into words.
func pdfErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("reading the PDF took longer than the import allows")
	}
	return err
}

package imports

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/ops"
)

// The edition an EPUB import records (F-372): the book's file, kept intact, and the
// chapters it was read as, with the Markdown the converter made for each before the
// writer's edits. The file is staged before the transaction (the quota is checked
// then, and again inside it), placed inside it, and removed if it does not commit.

// stageEdition stages the book's file for the edition record. It does nothing for
// an import that is not an EPUB.
func stageEdition(c *ops.Ctx, in *intake, projectID string, data []byte) (*editions.Staged, error) {
	if in.epub == nil || len(data) == 0 {
		return nil, nil
	}
	return editions.Stage(c, c.Env, attachments.LimitsOf(c.Env), c.P.UserID, projectID, data, "epub", c.DryRun())
}

// recordEdition writes the edition and its chapters in the import's transaction.
// ids maps the plan's node keys to the documents made.
func recordEdition(c *ops.Ctx, tx *sql.Tx, s source, in *intake, pl *plan, projectID, sourceID string, ids map[string]string, now string, out *ApplyOut) error {
	ed := in.epub
	if ed == nil || s.staged == nil {
		return nil
	}
	if s.staged.SHA256 != ed.sha {
		return ops.Internal(errors.New("the staged file is not the book that was read"))
	}
	if err := s.staged.Place(c, tx, attachments.LimitsOf(c.Env)); err != nil {
		return err
	}
	pics := map[int]string{} // picture number -> the hash of its cleaned bytes
	if in.extras != nil {
		for _, p := range in.extras.pictures {
			pics[p.n] = p.pic.SHA256
		}
	}
	chs := make([]editions.NewChapter, 0, len(ed.chapters))
	for i, ch := range ed.chapters {
		doc := ""
		if ch.key != "" {
			doc = ids[pl.mergedInto(ch.key)]
		}
		parent := 0
		if ch.parentIdx >= 0 && ch.parentIdx < i {
			parent = ch.parentIdx + 1
		}
		chs = append(chs, editions.NewChapter{ParentOrd: parent, DocumentID: doc, SourcePath: ch.source, Kind: ch.kind, Title: ch.title, Words: ch.words,
			Baseline: baselineText(ch.baseline, pics)})
	}
	meta := map[string]any{}
	for k, v := range ed.meta {
		meta[k] = v
	}
	// The name the file came with (the browser's file name without its extension), so a list of editions can say which file it was.
	if n, _ := cleanLine(s.name, 200); n != "" {
		meta["source_name"] = n
	}
	if ed.coverN > 0 && s.pics != nil {
		if u, ok := s.pics.urls[ed.coverN]; ok {
			meta["cover_url"] = u
		}
	}
	label := pick(cleanText(s.label, 200), ed.label) // a label typed by a person holds no direction override or control
	e, err := editions.Insert(tx, editions.New{ProjectID: projectID, SourceID: sourceID, Kind: editions.KindEPUB, Label: label, Identifier: ed.ident, SHA256: ed.sha,
		Size: ed.size, Metadata: meta, By: c.P.UserID, Now: now, Chapters: chs}, true)
	if err != nil {
		return err
	}
	out.EditionID = e.ID
	c.Emit(ops.Event{Type: "edition.created", ProjectID: projectID, Payload: map[string]any{"edition_id": e.ID, "kind": "epub", "chapters": len(chs)}})
	c.Audit(ops.AuditEntry{Action: "edition.create", TargetType: "project", TargetID: projectID,
		Detail: map[string]any{"edition_id": e.ID, "sha256": ed.sha, "bytes": ed.size, "chapters": len(chs)}})
	return nil
}

// baselineText names the pictures of a chapter by the hash of their cleaned bytes
// (scriobh-picture:<sha256>), so a baseline reads the same in every project; a
// picture that was not kept is the [image] mark.
func baselineText(body string, pics map[int]string) string {
	if !strings.Contains(body, pictureScheme) {
		return body
	}
	return picRe.ReplaceAllStringFunc(body, func(m string) string {
		sub := picRe.FindStringSubmatch(m)
		n, _ := strconv.Atoi(sub[2])
		if h, ok := pics[n]; ok {
			return "![" + sub[1] + "](" + pictureScheme + h + ")"
		}
		if sub[1] == "" {
			return `*\[image\]*`
		}
		return `*\[image: ` + sub[1] + `\]*`
	})
}

// EpubBook is a book read on its own, with no project: what a comparison of two
// editions reads. Its chapters are the ones an edition records (editions.Chapters
// and editions.Baseline) for the same file, text for text.
type EpubBook struct {
	Info     EpubInfo       `json:"info"`
	Metadata map[string]any `json:"metadata"`
	SHA256   string         `json:"sha256"`
	Size     int64          `json:"size"`
	Chapters []EpubChapterText
}

// EpubChapterText is one chapter of a book read with ReadEpub.
type EpubChapterText struct {
	Ord        int    // 1-based, reading order
	ParentOrd  int    // 0 at the top
	Kind       string // part, chapter, scene, front_matter, back_matter
	Title      string
	SourcePath string
	Words      int
	// Markdown is the text as the importer makes it before any edit, with a picture as
	// scriobh-picture:<sha256 of its cleaned bytes>.
	Markdown string
	SHA256   string // of Markdown (editions.HashOf)
}

// ReadEpub reads a book and returns its chapters as the import would propose them,
// without writing anything: the cut (sections: "", auto, toc, files or headings) and
// whether title pages are kept (pages: "", skip or keep) are the import's options.
// It takes a place among the conversions the process runs at once, and gives up when
// ctx is done. The same limits as an import apply, and a book that import refuses
// (DRM, a fixed layout, a zip bomb) is refused here with the same error.
func ReadEpub(ctx context.Context, data []byte, sections, pages string) (*EpubBook, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := checkEpubOptions(sections, pages); err != nil {
		return nil, err
	}
	release, err := acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	// The same deadline as a request that reads a book: the caller's, and a ceiling of its own for a caller that has none.
	ctx, cancel := context.WithTimeout(ctx, epubReadTimeout)
	defer cancel()
	res, ed, err := epubConvert(data, "", convOpts{ctx: ctx, pictures: PicturesImport}, epubOpts{sections: sections, pages: pages})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var oe *ops.Error
		if errors.As(err, &oe) {
			return nil, oe
		}
		return nil, ops.Invalid("epub", capSentence(err.Error()))
	}
	res.toFiles() // gives the documents their paths
	finalizeEpub(ed)
	pics := map[int]string{}
	if res.pics != nil {
		for _, p := range res.pics.list {
			pics[p.n] = p.pic.SHA256
		}
	}
	out := &EpubBook{Info: ed.info, Metadata: ed.meta, SHA256: ed.sha, Size: ed.size}
	for i, ch := range ed.chapters {
		md := baselineText(ch.baseline, pics)
		parent := 0
		if ch.parentIdx >= 0 && ch.parentIdx < i {
			parent = ch.parentIdx + 1
		}
		out.Chapters = append(out.Chapters, EpubChapterText{Ord: i + 1, ParentOrd: parent, Kind: ch.kind, Title: ch.title, SourcePath: ch.source, Words: ch.words,
			Markdown: md, SHA256: editions.HashOf(md)})
	}
	return out, nil
}

// epubReadTimeout is the longest ReadEpub reads one book.
const epubReadTimeout = 5 * time.Minute

func checkEpubOptions(sections, pages string) error {
	switch sections {
	case "", EpubSectionsAuto, EpubSectionsTOC, EpubSectionsFiles, EpubSectionsHeadings:
	default:
		return ops.Invalid("epub_sections", "epub_sections is auto, toc, files or headings.")
	}
	switch pages {
	case "", EpubPagesSkip, EpubPagesKeep:
	default:
		return ops.Invalid("epub_pages", "epub_pages is skip or keep.")
	}
	return nil
}

func capSentence(msg string) string {
	if msg != "" {
		msg = strings.ToUpper(msg[:1]) + msg[1:]
	}
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}

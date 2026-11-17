package imports

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pandoc"
)

// LayoutPandoc is a document the Pandoc bridge converted. It is chosen by the
// writer (a file and its format), never detected.
const LayoutPandoc = "pandoc"

// Importing through Pandoc.
//
// Pandoc converts the file to GitHub-flavoured Markdown (internal/pandoc runs it
// sandboxed, with nothing but an allow-listed format name from the request). What
// comes back is as foreign as an export from Ghost: it is read exactly as the
// Markdown cards of a Ghost export are, so nothing in it can be a Scríobh
// construct it was not written as:
//
//	boundMarkdown     breaks up what makes a Markdown parser slow
//	goldmark          parses it, with no raw HTML (it is left out and counted)
//	htmlToBlocks      walks the result with the whitelist of elements the other
//	                  importers use; scripts, forms, embeds and the like are dropped
//	the sink          writes Markdown, escaping every character that could be read as
//	                  Markdown or as a private note, a ghost, an alternatives comment, a wiki link
//	docxConv.files    cuts the blocks into chapters at the first heading level, as for a .docx
//
// Control and bidirectional characters are removed by the same paths as for every
// other importer (cleanText, the sink's escaping, and the document write seam).
// Pictures Pandoc extracted are admitted only by the importer's picture box, which
// hands them to internal/imagesafe; a picture the text names that Pandoc did not
// extract is marked [image]. The provenance is a project_sources row of kind
// markdown, with layout pandoc and the format, the Pandoc version and the SHA-256
// of the file as sent, in its record.

// pandocOf finds the bridge, when there is one.
func pandocOf(c *ops.Ctx) *pandoc.Service {
	if c == nil {
		return nil
	}
	svc, _ := pandoc.ServiceOf(c.Env)
	return svc
}

func (s source) pandocVersion() string {
	if s.pandocSvc == nil {
		return ""
	}
	return s.pandocSvc.Version(s.ctx)
}

const (
	// maxPandocMarkdown is the Markdown Pandoc may return for one document: the 2 MiB the other importers allow a
	// Markdown file. A parser's memory follows the number of inline tokens and not the bytes: valid Markdown with a
	// formatted word every few characters cost 100 to 240 times its size (31 MiB reached 3.8 to 7.6 GB), so the
	// bound has to be small, and the parse is done a chunk at a time so the tree and the HTML of one chunk are
	// let go before the next (see convertPandoc). The peak measured at this size is in docs/p7-notes/pandoc.md.
	maxPandocMarkdown = MaxFileBytes
	// maxPandocParse is how long the Markdown parse of one document may take: it is checked between chunks, while
	// the parse runs, and a chunk is small and bounded by boundMarkdown, so a parse cannot outlast it by much.
	maxPandocParse = 20 * time.Second
	// pandocChunk is the size a chunk of Markdown is cut at (at a blank line, never inside a list or a quote).
	pandocChunk = 64 << 10
)

// chunkMarkdown cuts Markdown into pieces of about pandocChunk bytes at blank lines that end a block: the next line
// is not indented and does not continue a list, a quote or a definition. A piece is never cut where a guess about
// the parser's rules would matter for safety (the caps of boundMarkdown have been applied to every line already);
// a code block with a blank line in it may be split in two, which costs fidelity, never safety.
func chunkMarkdown(md string) []string {
	var out []string
	start, lastCut := 0, -1
	pos := 0
	for pos < len(md) {
		end := strings.IndexByte(md[pos:], '\n')
		next := len(md)
		if end >= 0 {
			next = pos + end + 1
		}
		line := md[pos:next]
		if strings.TrimSpace(line) == "" && next < len(md) {
			// look at the first non-blank line after this blank run
			q := next
			for q < len(md) {
				e := strings.IndexByte(md[q:], '\n')
				l := md[q:]
				if e >= 0 {
					l = md[q : q+e]
				}
				if strings.TrimSpace(l) != "" {
					if l[0] != ' ' && l[0] != '\t' && !strings.HasPrefix(l, ">") && !strings.HasPrefix(l, "- ") && !strings.HasPrefix(l, "* ") &&
						!strings.HasPrefix(l, "+ ") && !strings.HasPrefix(l, "[") && !(l[0] >= '0' && l[0] <= '9') {
						lastCut = next
					}
					break
				}
				if e < 0 {
					break
				}
				q += e + 1
			}
		}
		pos = next
		if lastCut > start && pos-start >= pandocChunk {
			out = append(out, md[start:lastCut])
			start = lastCut
		}
	}
	if start < len(md) {
		out = append(out, md[start:])
	}
	return out
}

// pandocError turns a failure of the bridge into the error a caller sees.
func pandocError(err error) error {
	e, ok := pandoc.AsError(err)
	if !ok {
		return ops.Invalid("pandoc", "The file could not be converted.")
	}
	switch e.Kind {
	case pandoc.KindDisabled:
		return ops.ConflictWith(e.Message(), map[string]any{"reason": "pandoc_off"})
	case pandoc.KindBusy:
		x := ops.RateLimited(5)
		x.Message = e.Message()
		return x
	case pandoc.KindNotFound, pandoc.KindUnsafe, pandoc.KindVersion, pandoc.KindUnsupported:
		return ops.ConflictWith("The Pandoc bridge is not usable: ask an administrator to check the Pandoc settings.", map[string]any{"reason": "pandoc_unusable"})
	case pandoc.KindCancelled:
		return errors.New("the request was cancelled")
	}
	return ops.Invalid("pandoc", e.Message())
}

func (s source) pandocIntake() (in *intake, err error) {
	defer func() {
		if r := recover(); r != nil {
			in, err = nil, ops.Invalid("pandoc", "The file could not be read.")
		}
	}()
	if s.pandocSvc == nil {
		return nil, ops.Conflict("Importing through Pandoc is not available on this server.")
	}
	if !pandoc.ValidImport(s.pandocFmt) {
		return nil, ops.Invalid("pandoc_format", "Say which format the file is: "+strings.Join(pandoc.ImportFormats(), ", ")+".")
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if oc, ok := s.ctx.(*ops.Ctx); ok && oc != nil {
		if p, has := ops.PrincipalFrom(oc); has {
			ctx = pandoc.WithWho(ctx, p.UserID)
		}
	}
	// Pictures are extracted only when the person asked for them (pictures: import). Extraction is the one write
	// Pandoc makes besides standard output, and a document's pictures are rarely why it is imported.
	res, err := s.pandocSvc.ToMarkdown(ctx, s.pandocFmt, s.pandoc, pandoc.ImportOptions{ExtractMedia: s.conv.pictures == "import"})
	if err != nil {
		return nil, pandocError(err)
	}
	o := s.conv
	o.ctx = s.ctx
	return convertPandoc(res, s.pandoc, s.name, s.headings, o)
}

// convertPandoc turns what Pandoc returned into the intake the plan is built from.
func convertPandoc(res *pandoc.Imported, original []byte, name, headings string, o convOpts) (*intake, error) {
	md := res.Markdown
	if len(md) > maxPandocMarkdown {
		return nil, ops.Invalid("pandoc", "The converted text is larger than the import limit.")
	}
	if !utf8.Valid(md) {
		md = bytes.ToValidUTF8(md, []byte("\uFFFD"))
	}
	text, bounded := boundMarkdown(cleanForeign(string(md)))
	dctx := o.ctx
	if dctx == nil {
		dctx = context.Background()
	}
	dctx, cancelParse := context.WithTimeout(dctx, maxPandocParse)
	defer cancelParse()
	chunks := chunkMarkdown(text)
	text = ""
	rawHTML := 0

	pics := newPicBox(o)
	hs := &htmlStats{}
	index := map[string]pandoc.Media{}
	for _, m := range res.Media {
		index[strings.ToLower(m.Path)] = m
	}
	hs.pic = func(src, alt string) (seg, bool) {
		m, ok := lookupMedia(index, src)
		if !ok {
			pics.missing++
			return seg{}, false
		}
		p, _ := pics.add(m.Path, m.Data, path.Base(m.Path))
		if p == nil {
			return seg{}, false
		}
		if alt == "" {
			alt = strings.TrimSuffix(path.Base(m.Path), path.Ext(m.Path))
		}
		return p.seg(alt), true
	}
	sk := newSink()
	md2 := ghostMarkdown()
	for _, ch := range chunks {
		if dctx.Err() != nil {
			if o.ctx != nil && o.ctx.Err() != nil {
				return nil, o.ctx.Err()
			}
			return nil, ops.Invalid("pandoc", "The converted text took too long to read.")
		}
		var out bytes.Buffer
		if err := md2.Convert([]byte(ch), &out); err != nil {
			return nil, ops.Invalid("pandoc", "The converted text could not be read.")
		}
		html := out.String()
		rawHTML += strings.Count(html, "<!-- raw HTML omitted -->")
		if err := htmlToBlocks(sk, html, htmlOpts{}, hs); err != nil {
			if strings.Contains(err.Error(), "limit") {
				return nil, ops.Invalid("pandoc", "The converted text is larger than the import limit.")
			}
			return nil, ops.Invalid("pandoc", "The converted text could not be read.")
		}
	}
	if sk.overflow() {
		return nil, ops.Invalid("pandoc", "The converted text is larger than the import limit.")
	}
	files, parts, err := sk.c.files(name, headings)
	if err != nil {
		return nil, ops.Invalid("pandoc", err.Error())
	}
	if len(files) == 0 {
		return nil, ops.Invalid("pandoc", "Pandoc returned no text from that file.")
	}
	in, err := intakeFiles(files, false)
	if err != nil {
		return nil, ops.Invalid("pandoc", err.Error())
	}
	sum := sha256.Sum256(original)
	outSum := sha256.Sum256(res.Markdown)
	rep := &cresult{meta: map[string]any{}}
	rep.pics = pics
	pics.report(rep)
	rep.meta["pandoc"] = map[string]any{
		"format": res.Format, "version": res.Version, "headings": headings, "markdown_sha256": hex.EncodeToString(outSum[:]), "markdown_bytes": len(res.Markdown),
		"media_extracted": len(res.Media), "media_skipped": res.MediaSkipped, "images": hs.images, "image_addresses": hs.imageSrc, "pictures_placed": hs.placed,
		"raw_html_dropped": rawHTML, "markdown_broken_up": bounded, "links": hs.links, "links_not_kept": hs.badLinks, "tables": sk.c.tables,
		"scripts_dropped": hs.scripts, "iframes_dropped": hs.iframes, "forms_dropped": hs.forms, "footnotes": hs.notes,
	}
	rep.warnN(1, "Converted by Pandoc %s from %s. Pandoc's own reading of the format decides what became headings, lists and tables; check the result.", res.Version, res.Format)
	rep.warnN(hs.images, "%d image(s) were not imported; each is marked in the text.", hs.images)
	rep.warnN(rawHTML, "%d piece(s) of raw HTML in the converted text were dropped.", rawHTML)
	rep.warnN(bounded, "%d place(s) in the converted text were broken up: a paragraph of more than 8 KB, more than 16 quote or list markers on a line, or more than 64 columns of indent.", bounded)
	rep.warnN(hs.scripts+hs.iframes+hs.forms+hs.embeds+hs.media, "Left out: %d script(s), %d iframe(s), %d form(s), %d embed(s), %d audio or video element(s).", hs.scripts, hs.iframes, hs.forms, hs.embeds, hs.media)
	rep.warnN(hs.badLinks, "%d link(s) to something other than a web or e-mail address kept their text only.", hs.badLinks)
	rep.warnN(res.MediaSkipped, "%d extracted file(s) were not read (links, special files, or over the limits).", res.MediaSkipped)
	if parts {
		rep.warn("Text under a part heading, before its first chapter, became a chapter of its own.")
	}
	in.apply(&prepass{layout: LayoutPandoc, srcKind: "markdown", srcHash: hex.EncodeToString(sum[:]), project: &ProjectInfo{}, warnings: rep.warnings, meta: rep.meta, extras: newExtras(nil, pics)})
	return in, nil
}

// cleanForeign removes from converted text what no manuscript needs and an attack on
// the reader can use: control characters (a new line and a tab stay), the line and
// paragraph separators, the bidirectional embeddings, overrides and isolates that
// make text read in an order other than the one it is stored in, the byte order mark
// and the noncharacters. Joiners and the left-to-right and right-to-left marks stay:
// scripts need them.
func cleanForeign(s string) string {
	clean := true
	for _, r := range s {
		if foreign(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	return strings.Map(func(r rune) rune {
		if foreign(r) {
			return -1
		}
		return r
	}, s)
}

func foreign(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
		return true
	case r == 0x2028 || r == 0x2029 || r == 0xfeff || r == 0xfffe || r == 0xffff:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// lookupMedia finds the extracted file a Markdown address names. Only a path
// inside what Pandoc extracted counts: no scheme, no host, no way out.
func lookupMedia(index map[string]pandoc.Media, src string) (pandoc.Media, bool) {
	s := strings.TrimSpace(src)
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if u, err := url.PathUnescape(s); err == nil {
		s = u
	}
	s = strings.ToLower(strings.TrimPrefix(strings.ReplaceAll(s, "\\", "/"), "./"))
	if s == "" || strings.HasPrefix(s, "/") || strings.Contains(s, "://") || strings.Contains(s, "..") || strings.HasPrefix(s, "data:") {
		return pandoc.Media{}, false
	}
	if m, ok := index[s]; ok {
		return m, true
	}
	if m, ok := index["media/"+s]; ok {
		return m, true
	}
	return pandoc.Media{}, false
}

package imports

import (
	"regexp"
	"strings"
)

// The detector for sources with no layout of their own: a PDF, a document Pandoc converted, a plain text file (F-1244, F-344).
//
// A Word file records, for each paragraph, how it looks (centred, a page break before it, its style); the detector reads those. A PDF has
// no such record that can be trusted (where a page ends says nothing of where a chapter does), and Pandoc drops the layout, so these
// sources give the detector only what the words themselves say: the text of the block, whether it is all bold or all in capitals, how
// long it is, whether it ends like a sentence, and the level of a heading. No layout signal is invented: a page start is not a break, so
// a label with no other mark is an unformatted label, which is a suggestion and never cuts by itself (the plan's D6).

var (
	reMdNote    = regexp.MustCompile(`\[\^[^\]\s]{1,40}\]`)
	reMdLink    = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reMdEscape  = regexp.MustCompile(`\\([\\` + "`" + `*_{}\[\]()#+\-.!|~<>=:"'])`)
	reAllBold   = regexp.MustCompile(`^\*\*[^*].*[^*]\*\*$|^\*\*[^*]\*\*$`)
	reBoldInner = regexp.MustCompile(`\*\*.+\*\*.+\*\*`)
)

// plainOf is the text of a block as a reader sees it: the Markdown taken out.
func mdPlain(md string) string {
	t := reMdNote.ReplaceAllString(md, "")
	t = reMdLink.ReplaceAllString(t, "$1")
	t = reMdEscape.ReplaceAllString(t, "$1")
	t = strings.NewReplacer("**", "", "~~", "", "`", "", "*", "").Replace(t)
	return strings.Join(strings.Fields(noMarks(t)), " ")
}

// allBold says the whole text of a block is bold: it begins and ends with the bold marker and has no other bold passage in it.
func allBold(md string) bool {
	t := strings.TrimSpace(md)
	return strings.HasPrefix(t, "**") && strings.HasSuffix(t, "**") && len(t) > 4 && !reBoldInner.MatchString(t) && reAllBold.MatchString(t)
}

// synthSigs gives every paragraph and heading of c the signals the detector reads, from the words alone. The ordinal is the block's place
// among the paragraphs and headings, in reading order, whatever the options: it is the stable part of a chapter's key, as a Word
// paragraph's index is. It is clamped to the paragraph limit; a source with more paragraphs than that is refused by the caller.
func (c *docxConv) synthSigs() {
	ord := 0
	for i := range c.blocks {
		b := &c.blocks[i]
		if b.kind != bPara && b.kind != bHeading {
			continue
		}
		ord++
		if ord > maxParagraphs {
			ord = maxParagraphs
		}
		plain := mdPlain(b.text)
		outline := 0
		if b.kind == bHeading {
			outline = max(1, min(9, b.level))
		}
		sg := c.sigOf(look{bold: b.kind == bPara && allBold(b.text)}, plain, "", outline, "", ord)
		b.sig = sg
	}
}

// defaultCut says the options ask for nothing but what auto does: no rule, no pattern being kept or tried, no breaks of the writer's own.
func (o docxCutOpts) defaultCut() bool {
	return (o.split == "" || o.split == SplitAuto) && o.pattern == "" && o.try == "" && len(o.add) == 0
}

// hasHeadings says the converter's blocks hold at least one heading.
func (c *docxConv) hasHeadings() bool {
	for _, b := range c.blocks {
		if b.kind == bHeading {
			return true
		}
	}
	return false
}

// cutBlocks makes the documents of a converter whose blocks have no layout (a PDF, a Pandoc document, plain text). It is files() with the
// detector behind it:
//
//   - A source that already has headings, cut with no rule asked for, is cut as it always was, at its lowest heading level (files). The
//     PDF's outline and the headings Pandoc kept are the source's own statement, and the keys and text of what was imported before this
//     do not change.
//   - Anything else goes to the detector: a source with no headings (the book that used to be one "Front Matter" lump), and any rule the
//     writer chose, any pattern and any chapter started by hand. The detector reads only the words (see synthSigs), so under auto a
//     label line cuts only when it is also bold or in capitals, and a plain "Chapter 3" is a suggestion for the writer to take.
//
// info is nil when files cut it.
func (c *docxConv) cutBlocks(name, headings string, opt docxCutOpts, pi *ProjectInfo) (files []File, parts bool, info *BreakInfo, notes map[string]*docNote, err error) {
	if opt.defaultCut() && c.hasHeadings() {
		files, parts, err = c.files(name, headings)
		return files, parts, nil, nil, err
	}
	c.synthSigs()
	opt.headings, opt.name = strings.TrimPrefix(headings, "auto"), name
	if pi != nil {
		opt.projectTitle, opt.author = pi.Title, pi.Author
	}
	files, notes, info, err = c.cutDocx(opt, pi)
	if err != nil {
		return nil, false, nil, nil, err
	}
	return files, info.intro, info, notes, nil
}

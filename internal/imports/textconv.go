package imports

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Plain text import (F-344). A text file becomes the folder of Markdown files the generic importer reads, as a Word file or a PDF does, so
// the preview, the edits, the token, the caps and the provenance are the folder import's own. The chapters are found by the shared detector
// (blocksig.go), which can read only what the words say: a "Chapter 3" line is a suggestion until the writer chooses it, a chosen pattern
// cuts where it matches, and nothing is cut that the writer did not choose.
//
// What a text file becomes:
//
//	blank lines              paragraph breaks; the lines of a paragraph are joined with a space, as a viewer would show wrapped text
//	a file with no blank     one paragraph a line
//	line at all
//	a line of * * *, ***,    the canonical scene break (* * *); a lone #, ~~~ or ornament is
//	--- or ___              kept as text, and counted
//	everything else          text, with the characters Markdown would take for markup escaped, so what was typed is what is shown
//
// Never kept: anything that is not text (a file with NUL bytes, or saved as UTF-16, is refused in words), control characters and
// direction overrides (removed, as for any converted text), bytes that are not UTF-8 (replaced, and counted).

// reTextScene is what a text file's scene break is: a line that Markdown itself reads as a thematic break (three or more of * - or _, with
// or without spaces between). It is the one convention a plain text file has that means the same thing there as in Markdown, so it becomes
// the canonical * * *. A lone #, an ornament (⁂ ❦ •••) or ~~~ is a mark a writer chose, and what they typed is kept: it is text, and the
// preview says how many there were.
var reTextScene = regexp.MustCompile(`^(?:\*\s*){3,}$|^(?:-\s*){3,}$|^(?:_\s*){3,}$`)

// MaxText is the largest plain text file read: well over any book, well under what the importer holds.
const MaxText = 16 << 20

// textToConv reads a text file into a converter that holds its blocks, with the facts of the file. It makes no files: the cut does.
func textToConv(data []byte) (c *docxConv, meta map[string]any, warns []string, err error) {
	if len(data) > MaxText {
		return nil, nil, nil, &cutErr{reason: "text_big", msg: fmt.Sprintf("that text file is larger than the %d MiB an import reads", MaxText>>20), n: MaxText >> 20}
	}
	if len(data) >= 2 && (data[0] == 0xFF && data[1] == 0xFE || data[0] == 0xFE && data[1] == 0xFF) {
		return nil, nil, nil, &cutErr{reason: "text_utf16", msg: "that text file is saved as UTF-16; save it as UTF-8 (plain text) and choose it again"}
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, nil, nil, &cutErr{reason: "not_text", msg: "that is not a plain text file: it holds bytes no text has"}
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	invalid := 0
	if !utf8.Valid(data) {
		for i := 0; i < len(data); {
			r, n := utf8.DecodeRune(data[i:])
			if r == utf8.RuneError && n == 1 {
				invalid++
			}
			i += n
		}
		data = bytes.ToValidUTF8(data, []byte("�"))
	}
	s := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
	s = strings.ReplaceAll(s, "\f", "\n\n") // a form feed is a page, which is nothing the detector trusts: a paragraph break at most
	s = cleanForeign(s)
	lines := strings.Split(strings.TrimSpace(s), "\n") // the file's own last newline is not a blank line
	blank := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blank++
		}
	}
	// A file with no blank line holds a paragraph on each line; with them, a paragraph is the lines between two.
	byLine := blank == 0
	c = newDocxConv()
	paras, joined, scenes, ornaments := 0, 0, 0, 0
	var cur []string
	flush := func() error {
		if len(cur) == 0 {
			return nil
		}
		if len(cur) > 1 {
			joined++
		}
		t := strings.Join(cur, " ")
		cur = cur[:0]
		if reTextScene.MatchString(t) {
			c.add(mdBlock{kind: bScene}, 0)
			scenes++
			return nil
		}
		if reSceneText.MatchString(t) { // a lone # or an ornament: a Word writer's convention, which a text file does not share; kept as typed
			ornaments++
		}
		paras++
		if paras > maxParagraphs {
			return &cutErr{reason: "text_paragraphs", msg: fmt.Sprintf("that text file holds more than %d paragraphs", maxParagraphs), n: maxParagraphs}
		}
		c.add(mdBlock{kind: bPara, text: escLines(c.render([]seg{{text: t}}))}, 0)
		if c.overflow {
			return &cutErr{reason: "text_overflow", msg: fmt.Sprintf("that text file holds more text than the %d MB import limit", MaxTotalBytes>>20), n: MaxTotalBytes >> 20}
		}
		return nil
	}
	for _, l := range lines {
		t := strings.Join(strings.Fields(l), " ")
		switch {
		case t == "":
			if err := flush(); err != nil {
				return nil, nil, nil, err
			}
		case byLine:
			cur = append(cur[:0], t)
			if err := flush(); err != nil {
				return nil, nil, nil, err
			}
		default:
			cur = append(cur, t)
		}
	}
	if err := flush(); err != nil {
		return nil, nil, nil, err
	}
	if paras == 0 {
		return nil, nil, nil, &cutErr{reason: "text_empty", msg: "the text file has no text to import"}
	}
	meta = map[string]any{"text": map[string]any{"bytes": len(data), "paragraphs": paras, "lines_joined": joined, "scene_breaks": scenes, "ornament_lines_kept": ornaments,
		"invalid_bytes": invalid, "one_paragraph_a_line": byLine}}
	if byLine {
		warns = append(warns, "The text file has no blank line, so each line was read as a paragraph.")
	} else if joined > 0 {
		warns = append(warns, fmt.Sprintf("%d paragraph(s) had line breaks inside; their lines were joined with a space, as wrapped text is shown. If they are verse, put the line breaks back.", joined))
	}
	if ornaments > 0 {
		warns = append(warns, fmt.Sprintf("%d line(s) of a lone # or an ornament were kept as text, as typed. If they mark scene breaks, put * * * in their place.", ornaments))
	}
	if invalid > 0 {
		warns = append(warns, fmt.Sprintf("%d byte(s) were not valid UTF-8 and became the replacement character. If the file is in an older encoding, save it as UTF-8 and import it again.", invalid))
	}
	return c, meta, warns, nil
}

// textToFiles converts a text file into a folder of Markdown files, with what the file says of itself (nothing: a text file has no title
// or author, so the project is named for the file) and what the cut found.
func textToFiles(data []byte, name, headings string, cut docxCutOpts) (files []File, pre *prepass, err error) {
	defer func() {
		if r := recover(); r != nil {
			files, pre, err = nil, nil, errors.New("the text file could not be read")
		}
	}()
	c, meta, warns, err := textToConv(data)
	if err != nil {
		return nil, nil, err
	}
	pi := &ProjectInfo{}
	files, parts, info, notes, err := c.cutBlocks(name, headings, cut, pi)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(data)
	pre = &prepass{layout: LayoutText, srcKind: "markdown", srcHash: hex.EncodeToString(sum[:]), project: pi, meta: meta, warnings: warns, notes: notes, breaks: info}
	if parts {
		pre.warnings = append(pre.warnings, "Text under a part heading, before its first chapter, became a chapter of its own.")
	}
	if info != nil && info.Hint == "pages" {
		pre.warnings = append(pre.warnings, "These chapters look like pages, not chapters: there are very many of them, or most are short. Choose another way to cut, or join them.")
	}
	return files, pre, nil
}

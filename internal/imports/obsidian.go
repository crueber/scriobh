package imports

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Obsidian vault import. A vault is a folder of Markdown with a few Obsidian
// conventions on top. The vault is chosen, never detected: the writer picks the
// layout "obsidian" in the preview, and the files are rewritten into the plain
// Markdown the generic importer reads (folders become parts, files become
// chapters). Nothing is guessed about Hugo, Jekyll or any other layout.
//
//	[[Page]], [[Page|alias]]         kept: Scríobh reads the same wiki links. A link to a
//	                                 heading or a block ([[Page#H]], [[Page^id]]) points at the
//	                                 page, and a folder path in the link is dropped.
//	![[note]]                        a link to the note (the embed is not expanded), counted.
//	![[picture.png]], ![[file.pdf]]  a bracketed note in the text; the file is not imported.
//	> [!note] Title                  a block quote that begins with a bold label.
//	%%a comment%%                    the same syntax as a Scríobh private note, so it stays
//	                                 private; a comment across paragraphs is closed and
//	                                 reopened at each paragraph, since a private note ends
//	                                 with its paragraph.
//	```dataview ... ```, <% ... %>   dropped, and counted: they are queries and templates, not text.
//	#tag, tags: in the front matter  kept as written; the tags found are listed in the preview.
//	^block-id at a line's end        removed (nothing can point at it any more).
//	.obsidian/, *.canvas, drawings   skipped and listed.
//
// Front matter stays in the text exactly as written, so its title and its
// properties survive.

// Obsidian is the layout name to pass in Options.Layout.
const Obsidian = "obsidian"

var (
	reObsEmbed   = regexp.MustCompile(`!\[\[([^\[\]\n]+?)\]\]`)
	reObsLink    = regexp.MustCompile(`\[\[([^\[\]\n]+?)\]\]`)
	reObsCallout = regexp.MustCompile(`^(\s*(?:>\s*)+)\[!([A-Za-z0-9_-]+)\][+-]?(?:[ \t]+(.*))?$`)
	reObsBlockID = regexp.MustCompile(` \^[A-Za-z0-9-]{3,}$`)
	reObsTag     = regexp.MustCompile(`(?:^|[\s(])#([\p{L}_][\p{L}\p{N}_/\-]*)`)
	reObsFence   = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")
	reObsLocalMD = regexp.MustCompile(`!\[[^\]]*\]\((?:\./|\.\./)?[^):]+\.(?:png|jpe?g|gif|webp|svg|bmp)\)`)
	reTemplater  = regexp.MustCompile(`<%[*+_-]?[\s\S]*?-?%>`)
)

var obsImageExt = map[string]bool{"png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true, "svg": true, "bmp": true, "avif": true, "heic": true}
var obsAttachExt = map[string]bool{"pdf": true, "mp3": true, "mp4": true, "wav": true, "ogg": true, "m4a": true, "webm": true, "mov": true, "zip": true, "mkv": true, "flac": true}

type obsStats struct {
	noteEmbeds, imageEmbeds, attachEmbeds, localImages int
	comments, dataview, templater, callouts, blockIDs  int
	unclosedTpl                                        int
	tags                                               map[string]bool
	excalidraw                                         int
}

// obsidianConvert rewrites a vault's files for the generic importer. It reports
// what it changed or dropped. Files it cannot read as text are passed on so the
// intake lists them with its own reason.
func obsidianConvert(files []File) (out []File, pre *prepass) {
	// A note that breaks the converter must not take the command down, and must
	// not be passed on unconverted (its comments would not be private).
	defer func() {
		if r := recover(); r != nil {
			out = nil
			pre = &prepass{layout: Obsidian, warnings: []string{"The vault could not be converted and nothing was imported from it."}}
		}
	}()
	pre = &prepass{layout: Obsidian, meta: map[string]any{}}
	st := &obsStats{tags: map[string]bool{}}
	attachments, canvases := 0, 0
	for _, f := range files {
		lower := strings.ToLower(f.Path)
		ext := strings.TrimPrefix(path.Ext(lower), ".")
		switch {
		case f.Size < 0:
			out = append(out, f)
		case ext == "canvas":
			canvases++
			pre.skipped = append(pre.skipped, Skipped{Path: shown(f.Path), Reason: "Obsidian canvas (not imported)"})
		case ext == "md" || ext == "markdown":
			b := f.bytesOf()
			if len(b) == 0 || len(b) > MaxFileBytes || !utf8.Valid(b) {
				out = append(out, f) // the intake says why it is skipped (or that it is empty)
				continue
			}
			text := string(b)
			if strings.Contains(text[:min(len(text), 600)], "excalidraw-plugin") {
				st.excalidraw++
				pre.skipped = append(pre.skipped, Skipped{Path: shown(f.Path), Reason: "Excalidraw drawing (not imported)"})
				continue
			}
			f.Data, f.Content = []byte(obsidianText(text, st)), ""
			f.Size = int64(len(f.Data))
			out = append(out, f)
		case obsImageExt[ext] || obsAttachExt[ext]:
			attachments++
			pre.skipped = append(pre.skipped, Skipped{Path: shown(f.Path), Reason: "attachment (files are not imported yet)"})
		default:
			out = append(out, f)
		}
	}
	w := func(n int, format string, args ...any) {
		if n > 0 {
			pre.warnings = append(pre.warnings, fmt.Sprintf(format, args...))
		}
	}
	w(canvases, "%d Obsidian canvas file(s) were skipped: a canvas is a drawing, not text.", canvases)
	w(st.excalidraw, "%d Excalidraw drawing(s) were skipped.", st.excalidraw)
	w(attachments, "%d attached file(s) (images, PDFs, audio) were not imported; embeds of them became bracketed notes in the text.", attachments)
	w(st.imageEmbeds+st.attachEmbeds+st.localImages, "%d embedded image(s) or file(s) became notes like [image: name].", st.imageEmbeds+st.attachEmbeds+st.localImages)
	w(st.noteEmbeds, "%d embedded note(s) (![[Note]]) became links to the note; the embedded text was not copied in.", st.noteEmbeds)
	w(st.dataview, "%d dataview or query block(s) were dropped.", st.dataview)
	w(st.templater, "%d Templater command(s) were dropped.", st.templater)
	w(st.unclosedTpl, "%d Templater command(s) never closed; only the lines up to the next blank line were dropped.", st.unclosedTpl)
	w(st.comments, "%d Obsidian comment(s) (%%%%...%%%%) were kept as private notes, which are never in an export.", st.comments)
	w(st.callouts, "%d callout(s) became block quotes with a bold label.", st.callouts)
	w(st.blockIDs, "%d block id(s) (^id) were removed.", st.blockIDs)
	if n := len(st.tags); n > 0 {
		tags := make([]string, 0, n)
		for t := range st.tags {
			tags = append(tags, "#"+t)
		}
		sort.Strings(tags)
		shownTags := tags
		more := ""
		if len(tags) > 20 {
			shownTags, more = tags[:20], fmt.Sprintf(" and %d more", len(tags)-20)
		}
		pre.warnings = append(pre.warnings, fmt.Sprintf("%d tag(s) were found (%s%s). Tags stay in the text as written; they are not turned into tags.", n, strings.Join(shownTags, ", "), more))
	}
	pre.meta["obsidian"] = map[string]int{"canvases": canvases, "attachments": attachments, "note_embeds": st.noteEmbeds, "comments": st.comments, "dataview": st.dataview, "tags": len(st.tags)}
	return out, pre
}

// obsidianText rewrites one note.
//
// An Obsidian comment may run across lines and across Markdown blocks; a
// Scríobh private note ends with its text block (a paragraph, a heading, a
// list item, a table cell), and a delimiter that does not pair inside one is
// printed. So no comment is ever left open across a line. The line that opens
// one closes it at its end; every line inside it becomes a private paragraph of
// its own, with whatever block marker it had (a heading mark, a bullet, a
// quote mark, a fence, a rule, an indent) dropped with its structure, since
// what is inside a comment is private and needs none; the line that closes it
// carries on as ordinary text. A comment in a table row is cut out of the row.
func obsidianText(src string, st *obsStats) string {
	src = strings.ReplaceAll(strings.ReplaceAll(src, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(src, "\n")
	var out []string
	i := 0
	// Front matter: kept exactly; its tags are read.
	if len(lines) > 0 && strings.TrimRight(lines[0], " \t") == "---" {
		end := -1
		for j := 1; j < len(lines) && j < 200; j++ {
			if t := strings.TrimRight(lines[j], " \t"); t == "---" || t == "..." {
				end = j
				break
			}
		}
		if end > 0 {
			collectFrontTags(lines[1:end], st)
			out = append(out, lines[:end+1]...)
			i = end + 1
		}
	}
	var fence, dropFence string // the open fence's marker; a dataview fence being dropped
	open := false               // inside an Obsidian comment
	inTemplater := false
	tplLines := 0
	ensureBlank := func() {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
	}
	for ; i < len(lines); i++ {
		line := lines[i]
		if fence != "" || dropFence != "" {
			m := reObsFence.FindStringSubmatch(line)
			closing := m != nil && strings.TrimSpace(m[2]) == "" && strings.HasPrefix(m[1], string([]byte{(fence + dropFence)[0]})) && len(m[1]) >= len(fence+dropFence)
			if dropFence != "" {
				if closing {
					dropFence = ""
				}
				continue
			}
			out = append(out, line)
			if closing {
				fence = ""
			}
			continue
		}
		if open {
			// Inside a comment: private text, never structure.
			inner, rest, closed := line, "", false
			if j := strings.Index(line, "%%"); j >= 0 {
				inner, rest, closed = line[:j], line[j+2:], true
			}
			inner = strings.TrimSpace(strings.ReplaceAll(inner, "%%", ""))
			if closed && strings.TrimSpace(rest) != "" {
				// The comment ends here and the line goes on as text.
				open = false
				if inner == "" {
					st.comments++
					line = rest
				} else {
					ensureBlank()
					line = "%%" + inner + "%%" + rest // read as a whole below: a pair, then the rest
				}
			} else {
				if inner != "" {
					ensureBlank()
					out = append(out, "%%"+inner+"%%")
				} else {
					ensureBlank()
				}
				if closed {
					open = false
					st.comments++
				}
				continue
			}
		}
		if m := reObsFence.FindStringSubmatch(line); m != nil {
			info := strings.ToLower(strings.Fields(m[2] + " x")[0])
			if info == "dataview" || info == "dataviewjs" || info == "query" || info == "tasks" {
				st.dataview++
				dropFence = m[1]
				continue
			}
			fence = m[1]
			out = append(out, line)
			continue
		}
		if inTemplater {
			if j := strings.Index(line, "%>"); j >= 0 {
				inTemplater = false
				line = strings.TrimSpace(line[j+2:])
				if line == "" {
					continue
				}
			} else if strings.TrimSpace(line) == "" || tplLines >= maxTemplaterLines {
				// A template command that never closes ends at the blank line (or after
				// a few lines): the rest of the note is text, not template.
				inTemplater = false
				st.unclosedTpl++
			} else {
				tplLines++
				continue
			}
		}
		if strings.TrimSpace(line) == "" {
			out = append(out, line)
			continue
		}
		wasTpl := inTemplater
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			var tail string
			line, tail, open = cutTableComments(line, st)
			if strings.TrimSpace(line) != "" {
				out = append(out, rewriteLine(line, st, new(bool), &inTemplater))
			}
			if open && strings.TrimSpace(tail) != "" {
				ensureBlank()
				out = append(out, "%%"+strings.ReplaceAll(strings.TrimSpace(tail), "%%", "")+"%%")
			}
			if !wasTpl && inTemplater {
				tplLines = 0
			}
			continue
		}
		line = rewriteLine(line, st, &open, &inTemplater)
		if !wasTpl && inTemplater {
			tplLines = 0
		}
		if open {
			// The comment goes on into the next lines, which are private text of
			// their own: close it here. One that opened at the very end of the line
			// has nothing in it yet, and the opener goes.
			if strings.HasSuffix(line, "%%") && opensAt(line) {
				line = strings.TrimSuffix(line, "%%")
			} else {
				line += "%%"
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// maxTemplaterLines bounds how far an unclosed Templater command reaches.
const maxTemplaterLines = 50

// opensAt reports whether the trailing %% of a line is a comment opening that
// nothing follows (the comment began at the very end of the line).
func opensAt(s string) bool {
	segs := splitCode(s)
	n := 0
	for _, sg := range segs {
		if !sg.code {
			n += strings.Count(sg.text, "%%")
		}
	}
	return n%2 == 1
}

// cutTableComments takes the comments out of a table row: a comment that
// crosses a cell would be cut in two by the cells, and a private note cannot
// span them. A comment that opens and does not close in the row is cut off
// there, and its text so far is returned.
func cutTableComments(line string, st *obsStats) (row, tail string, open bool) {
	var b strings.Builder
	for _, sg := range splitCode(line) {
		if sg.code {
			b.WriteString(sg.text)
			continue
		}
		t := sg.text
		for t != "" {
			j := strings.Index(t, "%%")
			if j < 0 {
				b.WriteString(t)
				break
			}
			b.WriteString(t[:j])
			rest := t[j+2:]
			k := strings.Index(rest, "%%")
			if k < 0 {
				return b.String(), rest, true
			}
			st.comments++
			t = rest[k+2:]
		}
	}
	return b.String(), "", false
}

func collectFrontTags(fm []string, st *obsStats) {
	inList := false
	for _, l := range fm {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "tags:") || strings.HasPrefix(t, "tag:"):
			v := strings.TrimSpace(t[strings.Index(t, ":")+1:])
			inList = v == ""
			v = strings.Trim(v, "[]")
			for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
				addTag(st, p)
			}
		case inList && strings.HasPrefix(t, "- "):
			addTag(st, strings.TrimSpace(t[2:]))
		case inList && t != "" && !strings.HasPrefix(t, "-"):
			inList = false
		}
	}
}

func addTag(st *obsStats, t string) {
	t = strings.Trim(strings.TrimSpace(t), `"'#`)
	if t != "" && utf8.RuneCountInString(t) <= 60 {
		st.tags[t] = true
	}
}

type codeSeg struct {
	text string
	code bool
}

// splitCode cuts a line into code spans and the text between them.
func splitCode(s string) []codeSeg {
	var out []codeSeg
	start, i := 0, 0
	for i < len(s) {
		if s[i] != '`' {
			i++
			continue
		}
		n := 0
		for i+n < len(s) && s[i+n] == '`' {
			n++
		}
		// the closing run must be exactly n backticks
		j := i + n
		found := -1
		for j < len(s) {
			if s[j] != '`' {
				j++
				continue
			}
			m := 0
			for j+m < len(s) && s[j+m] == '`' {
				m++
			}
			if m == n {
				found = j
				break
			}
			j += m
		}
		if found < 0 {
			i += n
			continue
		}
		if i > start {
			out = append(out, codeSeg{s[start:i], false})
		}
		out = append(out, codeSeg{s[i : found+n], true})
		i = found + n
		start = i
	}
	if start < len(s) {
		out = append(out, codeSeg{s[start:], false})
	}
	return out
}

// rewriteLine applies the line-level and inline rules to one line outside a code fence.
func rewriteLine(line string, st *obsStats, open *bool, inTemplater *bool) string {
	if m := reObsCallout.FindStringSubmatch(line); m != nil {
		st.callouts++
		label := capitalize(strings.ToLower(m[2]))
		if t := strings.TrimSpace(m[3]); t != "" {
			label += ": " + t
		}
		return m[1] + "**" + label + "**"
	}
	segs := splitCode(line)
	var b strings.Builder
	for _, sg := range segs {
		if sg.code {
			b.WriteString(sg.text)
			continue
		}
		t := sg.text
		// comments first: what is inside one is not rewritten
		var parts []string
		rest := t
		for rest != "" {
			j := strings.Index(rest, "%%")
			if j < 0 {
				parts = append(parts, plain(rest, *open, st, inTemplater))
				break
			}
			parts = append(parts, plain(rest[:j], *open, st, inTemplater), "%%")
			*open = !*open
			if !*open {
				st.comments++
			}
			rest = rest[j+2:]
		}
		b.WriteString(strings.Join(parts, ""))
	}
	return b.String()
}

// plain rewrites text that holds no comment delimiter. Inside a comment it is
// left alone.
func plain(t string, inComment bool, st *obsStats, inTemplater *bool) string {
	if inComment || t == "" {
		return t
	}
	// Templater commands.
	if strings.Contains(t, "<%") {
		t = reTemplater.ReplaceAllStringFunc(t, func(string) string { st.templater++; return "" })
		if j := strings.Index(t, "<%"); j >= 0 { // opened and not closed: drop to the end, and on until it closes
			st.templater++
			t = t[:j]
			*inTemplater = true
		}
	}
	t = reObsEmbed.ReplaceAllStringFunc(t, func(m string) string {
		inner := reObsEmbed.FindStringSubmatch(m)[1]
		target := inner
		if bar := strings.IndexByte(inner, '|'); bar >= 0 {
			target = inner[:bar]
		}
		name := path.Base(strings.TrimSpace(target))
		page := cutFragment(target)
		ext := strings.ToLower(strings.TrimPrefix(path.Ext(page), "."))
		switch {
		case obsImageExt[ext]:
			st.imageEmbeds++
			return "\\[image: " + escBracket(name) + "\\]" // plain: an italic mark beside a word leaves asterisks in the text
		case obsAttachExt[ext]:
			st.attachEmbeds++
			return "*\\[attachment: " + escBracket(name) + "\\]*"
		}
		st.noteEmbeds++
		return "[[" + path.Base(page) + "]]"
	})
	t = reObsLocalMD.ReplaceAllStringFunc(t, func(m string) string {
		st.localImages++
		return "\\[image\\]" // plain, as above
	})
	t = reObsLink.ReplaceAllStringFunc(t, func(m string) string {
		inner := reObsLink.FindStringSubmatch(m)[1]
		target, alias := inner, ""
		if bar := strings.IndexByte(inner, '|'); bar >= 0 {
			target, alias = inner[:bar], strings.TrimSpace(inner[bar+1:])
		}
		target = strings.TrimSuffix(target, `\`)
		frag := fragmentOf(target)
		page := strings.TrimSpace(path.Base(cutFragment(target)))
		if cutFragment(target) == "" { // [[#Heading]]: a link inside the note
			if alias != "" {
				return alias
			}
			return strings.TrimLeft(frag, "#^")
		}
		switch {
		case alias != "":
			return "[[" + page + "|" + alias + "]]"
		case strings.HasPrefix(frag, "#"):
			return "[[" + page + "|" + page + " > " + strings.TrimSpace(frag[1:]) + "]]"
		}
		return "[[" + page + "]]"
	})
	if reObsBlockID.MatchString(t) {
		t = reObsBlockID.ReplaceAllString(t, "")
		st.blockIDs++
	}
	for _, m := range reObsTag.FindAllStringSubmatch(t, -1) {
		addTag(st, m[1])
	}
	return t
}

func cutFragment(s string) string {
	if i := strings.IndexAny(s, "#^"); i >= 0 {
		return s[:i]
	}
	return s
}

func fragmentOf(s string) string {
	if i := strings.IndexAny(s, "#^"); i >= 0 {
		return s[i:]
	}
	return ""
}

func escBracket(s string) string {
	return strings.NewReplacer("[", "(", "]", ")", "*", "", "_", " ", "`", "'").Replace(s)
}

func capitalize(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

package imports

import "strings"

// Markdown that comes from someone else's export is parsed before it is trusted,
// and a Markdown parser can be made to work in quadratic time by a few kinds of
// text: block quotes and list markers piled up on one line, a paragraph that is
// one long unfinished link or image. boundMarkdown breaks those up before the
// parser sees them, and leaves everything a person writes as it is: no real
// paragraph is 16 KB, no real quote is sixteen levels deep.

const (
	mdMaxMarkers = 16      // quote and list markers that open one line
	mdMaxIndent  = 64      // columns of white space that open a line
	mdMaxPara    = 8 << 10 // bytes of one paragraph before it is cut at a blank line
	mdCutAt      = 4 << 10 // where a very long line is cut (at a space)
)

// boundMarkdown returns md with the pathological parts broken up, and how many
// places it changed. The caps apply to every line, whatever the parser will make
// of it: a line that looks like a fence to this function may not be one to the
// parser (a backtick in the info string, a tab counted as four columns, a closing
// fence with trailing spaces, a fence inside a list item), and a bound that is
// switched off by a guess is a bound an attacker chooses. Code in a foreign
// export loses nothing a reader needs: a line of code is not 16 quote markers
// deep, and a paragraph of code is not 8 KB without a blank line.
func boundMarkdown(md string) (string, int) {
	var b strings.Builder
	b.Grow(len(md) + len(md)/64)
	changed := 0
	para := 0
	for len(md) > 0 {
		line := md
		if i := strings.IndexByte(md, '\n'); i >= 0 {
			line, md = md[:i], md[i+1:]
		} else {
			md = ""
		}
		trim := strings.TrimLeft(line, " \t")
		if trim == "" {
			para = 0
			b.WriteString(line)
			b.WriteByte('\n')
			continue
		}
		line, c1 := capIndent(line)
		line, c2 := capMarkers(line)
		if c1 || c2 {
			changed++
		}
		if para+len(line) > mdMaxPara && para > 0 {
			b.WriteByte('\n') // a paragraph this long becomes two
			para = 0
			changed++
		}
		for len(line) > mdMaxPara {
			cut := strings.LastIndexByte(line[:mdCutAt], ' ')
			if cut <= 0 {
				cut = mdCutAt
			}
			b.WriteString(line[:cut])
			b.WriteString("\n\n")
			line = strings.TrimLeft(line[cut:], " ")
			changed++
		}
		para += len(line) + 1
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), changed
}

// fenceOf returns the run of three or more ` or ~ a line opens a fence with.
func fenceOf(trim string) string {
	if len(trim) < 3 || (trim[0] != '`' && trim[0] != '~') {
		return ""
	}
	n := 0
	for n < len(trim) && trim[n] == trim[0] {
		n++
	}
	if n < 3 {
		return ""
	}
	return trim[:n]
}

// capIndent limits the white space that opens a line.
func capIndent(line string) (string, bool) {
	w, i := 0, 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		if line[i] == '\t' {
			w += 4
		} else {
			w++
		}
		i++
		if w > mdMaxIndent {
			rest := strings.TrimLeft(line[i:], " \t")
			return strings.Repeat(" ", mdMaxIndent) + rest, true
		}
	}
	return line, false
}

// capMarkers keeps the first mdMaxMarkers quote and list markers of a line.
func capMarkers(line string) (string, bool) {
	i, n, keepEnd := 0, 0, 0
	for {
		j := i
		for j < len(line) && (line[j] == ' ' || line[j] == '\t') && j-i < 4 {
			j++
		}
		switch {
		case j < len(line) && line[j] == '>':
			j++
			if j < len(line) && (line[j] == ' ' || line[j] == '\t') {
				j++
			}
		case j+1 < len(line) && (line[j] == '-' || line[j] == '*' || line[j] == '+') && (line[j+1] == ' ' || line[j+1] == '\t'):
			j += 2
		case j < len(line) && line[j] >= '0' && line[j] <= '9':
			k := j
			for k < len(line) && line[k] >= '0' && line[k] <= '9' && k-j < 9 {
				k++
			}
			if k+1 < len(line) && (line[k] == '.' || line[k] == ')') && (line[k+1] == ' ' || line[k+1] == '\t') {
				j = k + 2
			} else {
				return line, false
			}
		default:
			if n > mdMaxMarkers {
				return line[:keepEnd] + line[i:], true
			}
			return line, false
		}
		n++
		i = j
		if n == mdMaxMarkers {
			keepEnd = i
		}
	}
}

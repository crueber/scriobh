package imports

import (
	"regexp"
	"strings"
)

// What a book's stylesheets say that matters to text: which classes are italic,
// bold or struck through. Books made by Calibre and by word processors carry
// their emphasis in classes (<span class="calibre3">), and a converter that reads
// only <em> would lose every italic. Only a rule of one class (.x, span.x, p.x),
// with font-style, font-weight or text-decoration, is read; nothing else in the
// stylesheet is interpreted, and the file is never fetched or run.

// cssFmt is the emphasis a class gives.
type cssFmt struct{ i, b, s bool }

func (f cssFmt) any() bool { return f.i || f.b || f.s }

const maxCSSRules = 20_000

var (
	cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	cssRule    = regexp.MustCompile(`([^{}@]+)\{([^{}]*)\}`)
	cssSel     = regexp.MustCompile(`^(?:[a-zA-Z][a-zA-Z0-9]*)?\.([a-zA-Z_][\w-]*)$`)
)

// parseCSSFormats reads the emphasis classes out of stylesheets.
func parseCSSFormats(sheets []string) map[string]cssFmt {
	out := map[string]cssFmt{}
	n := 0
	for _, css := range sheets {
		css = cssComment.ReplaceAllString(css, "")
		for _, m := range cssRule.FindAllStringSubmatch(css, -1) {
			if n++; n > maxCSSRules {
				return out
			}
			var f cssFmt
			for _, d := range strings.Split(m[2], ";") {
				k, v, ok := strings.Cut(d, ":")
				if !ok {
					continue
				}
				k, v = strings.ToLower(strings.TrimSpace(k)), strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "!important")))
				switch k {
				case "font-style":
					f.i = v == "italic" || v == "oblique"
				case "font-weight":
					f.b = v == "bold" || v == "bolder" || v == "600" || v == "700" || v == "800" || v == "900"
				case "text-decoration", "text-decoration-line":
					f.s = strings.Contains(v, "line-through")
				}
			}
			if !f.any() {
				continue
			}
			for _, sel := range strings.Split(m[1], ",") {
				if sm := cssSel.FindStringSubmatch(strings.TrimSpace(sel)); sm != nil {
					c := out[sm[1]]
					c.i, c.b, c.s = c.i || f.i, c.b || f.b, c.s || f.s
					out[sm[1]] = c
				}
			}
		}
	}
	return out
}

// classFormat is the emphasis an element's classes and style attribute give.
func (w *hwalk) classFormat(attrs map[string]string) cssFmt {
	var f cssFmt
	if len(attrs) == 0 {
		return f
	}
	if cls := attrs["class"]; cls != "" && len(w.o.classFmt) > 0 {
		for _, c := range strings.Fields(cls) {
			if g, ok := w.o.classFmt[c]; ok {
				f.i, f.b, f.s = f.i || g.i, f.b || g.b, f.s || g.s
			}
		}
	}
	if st := attrs["style"]; st != "" && len(st) < 400 {
		l := strings.ToLower(st)
		f.i = f.i || strings.Contains(l, "font-style:italic") || strings.Contains(l, "font-style: italic")
		f.b = f.b || strings.Contains(l, "font-weight:bold") || strings.Contains(l, "font-weight: bold") || strings.Contains(l, "font-weight:700") || strings.Contains(l, "font-weight: 700")
		f.s = f.s || strings.Contains(l, "line-through")
	}
	return f
}

func classIn(class string, set map[string]bool) bool {
	if class == "" {
		return false
	}
	for _, c := range strings.Fields(class) {
		if set[c] {
			return true
		}
	}
	return false
}

// A paragraph is a scene break by its class, or by its text.
var sceneClass = regexp.MustCompile(`(?i)^(?:scene[-_]?break|scenebreak|section[-_]?break|sectionbreak|scene[-_]?sep(?:arator)?|asterism|dinkus|fleuron|ornament|ornamental[-_]?break|sbreak|sb|sep|separator|divider|chapter[-_]?break[-_]?ornament)$`)

// sceneBlock says the paragraph just ended (segs, its class) is a scene break.
func sceneBlock(segs []seg, class string) bool {
	text := 0
	var sb strings.Builder
	for _, s := range segs {
		if s.note != "" || s.priv {
			return false
		}
		text += len(s.text)
		sb.WriteString(s.text)
	}
	if class != "" && text <= 40 {
		for _, c := range strings.Fields(class) {
			if sceneClass.MatchString(c) {
				return true
			}
		}
	}
	t := strings.TrimSpace(sb.String())
	return t != "" && len(t) <= 24 && reSceneText.MatchString(t)
}

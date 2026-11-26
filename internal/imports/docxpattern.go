package imports

import (
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/ops"
)

// Cutting a Word document at the paragraphs that match a pattern the writer types.
//
// The pattern is the writer's, and a book is large, so it is held to limits that do not depend on what the pattern or the book says:
// at most 200 characters; tried only against paragraphs of 200 characters or fewer (a long paragraph is never a chapter start); a
// regular expression is RE2 (no backtracking, so no input makes one run away), with a program of at most 400 instructions, and the work
// (the characters to try times the program; measured at about 3.5 ns for each, so at most about two seconds) is checked before anything is matched, so a book with a great deal of short text and a big
// expression is refused in words instead of being slow. The matches are bounded by the document cap, which refuses in the usual words.

// How the pattern is read.
const (
	SplitPattern = "pattern"
	MatchStarts  = "starts"
	MatchRegex   = "regex"
)

// Limits of the pattern rule.
const (
	MaxPatternChars = 200
	maxPatternLine  = 200         // a paragraph longer than this (in characters) is never tried
	maxPatternProg  = 400         // instructions in a regular expression's program
	maxPatternWork  = 500_000_000 // characters to try times instructions
	maxPatternHits  = 5           // matches shown by a test
)

// PatternHit is one paragraph a pattern matches.
type PatternHit struct {
	Ordinal int    `json:"ordinal"`
	Line    string `json:"line" desc:"The start of the paragraph, 80 characters at most. It is the manuscript's text: show it as text."`
}

// PatternInfo says what the pattern in the options matches in this document.
type PatternInfo struct {
	Count   int          `json:"count" desc:"How many paragraphs it matches (of those of 200 characters or fewer that are not in a contents page)."`
	First   []PatternHit `json:"first,omitempty" desc:"The first five."`
	Refused string       `json:"refused,omitempty" enum:"too_much" desc:"Why it was not tried: too_much is a book with more short text than a pattern of that size can be tried on."`
}

// patternFunc is a compiled pattern: it says whether a paragraph's text matches, and what trying it costs per character.
type patternFunc struct {
	match func(string) bool
	cost  int // instructions a character may take
}

// patternError is a pattern that cannot be used, with a fixed reason for the page and the words for everyone else.
type patternError struct{ reason, msg string }

func (e *patternError) Error() string { return e.msg }

func (e *patternError) asOps() *ops.Error { return invalidWhy("docx_split_pattern", e.msg, e.reason) }

// compilePattern reads a pattern the way the options say. It does all its checking before it compiles anything large.
func compilePattern(p, mode string) (*patternFunc, *patternError) {
	if strings.TrimSpace(p) == "" { // the spaces of a pattern are its own, but nothing but spaces is not one
		return nil, &patternError{"pattern_empty", "Type the text a chapter begins with, or an expression."}
	}
	if utf8.RuneCountInString(p) > MaxPatternChars {
		return nil, &patternError{"pattern_long", fmt.Sprintf("A pattern is at most %d characters.", MaxPatternChars)}
	}
	if mode == MatchRegex {
		re, err := syntax.Parse(p, syntax.Perl)
		if err != nil {
			return nil, &patternError{"pattern_invalid", "That is not an expression this program reads (RE2): " + syntaxWhy(err)}
		}
		prog, err := syntax.Compile(re.Simplify())
		if err != nil {
			return nil, &patternError{"pattern_invalid", "That is not an expression this program reads (RE2): " + syntaxWhy(err)}
		}
		if len(prog.Inst) > maxPatternProg {
			return nil, &patternError{"pattern_big", "That expression is too large: it makes a program of more than 400 steps. Make it shorter."}
		}
		rx, err := regexp.Compile(p)
		if err != nil {
			return nil, &patternError{"pattern_invalid", "That is not an expression this program reads (RE2): " + syntaxWhy(err)}
		}
		return &patternFunc{match: rx.MatchString, cost: len(prog.Inst)}, nil
	}
	low := strings.ToLower(p)
	return &patternFunc{match: func(s string) bool { return strings.HasPrefix(strings.ToLower(s), low) }, cost: 1}, nil
}

// syntaxWhy is a parse error in a line: the kind of mistake, which names no more of the pattern than the writer typed.
func syntaxWhy(err error) string {
	if se, ok := err.(*syntax.Error); ok {
		return string(se.Code) + "."
	}
	return "it cannot be compiled."
}

// patternBoundaries finds the paragraphs the pattern matches. A paragraph is tried when it is text (not a contents page, not a title page
// repeated) and has 200 characters or fewer.
func (d *detector) patternBoundaries(opt docxCutOpts) ([]boundary, *PatternInfo, *patternError) {
	pf, perr := compilePattern(opt.pattern, opt.match)
	if perr != nil {
		return nil, nil, perr
	}
	info := &PatternInfo{}
	var tried []int
	chars := 0
	for i, b := range d.b {
		if (b.kind != bPara && b.kind != bHeading) || !d.ok(i) {
			continue
		}
		sg := d.sigs[i]
		if sg.plain == "" {
			continue
		}
		n := utf8.RuneCountInString(sg.plain)
		if n > maxPatternLine {
			continue
		}
		chars += n
		tried = append(tried, i)
	}
	if pf.cost > 1 && chars*pf.cost > maxPatternWork {
		info.Refused = "too_much"
		return nil, info, nil
	}
	var out []boundary
	for _, i := range tried {
		sg := d.sigs[i]
		if !pf.match(sg.plain) {
			continue
		}
		info.Count++
		if len(info.First) < maxPatternHits {
			line, _ := cleanLine(sg.plain, 80)
			info.First = append(info.First, PatternHit{Ordinal: sg.ord, Line: line})
		}
		if len(out) <= MaxMarkdown { // the document cap refuses a cut that makes more; no more are kept than that
			bd := d.boundaryAt(i, "pattern")
			bd.why = []string{"pattern"}
			bd.score = scoreWeights.sure
			out = append(out, bd)
		}
	}
	return out, info, nil
}

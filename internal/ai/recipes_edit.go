package ai

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/alts"
)

// The selection recipes (policy action "selection"). Each turns a selected
// passage into one suggested replacement. Wording is short and plain, and every
// change to a prompt is a new Version, so a run record says which wording it
// was sent under. The passage, the style guide, the brief and the entity sheets
// arrive as data blocks (the core's framing); the recipe's own sentence is the
// task.

// EditData is the parsed answer of a selection recipe: one replacement.
type EditData struct {
	Text string `json:"text"`
	// Op is "replace" (the passage is replaced) or "insert_after" (the text
	// follows the passage).
	Op string `json:"op" enum:"replace,insert_after"`
}

const editSystem = "You revise a passage of the author's manuscript as the task asks. Keep the author's voice, tense, point of view and meaning unless the task says otherwise. Keep names, facts and Markdown marks (emphasis, links) as they are. Follow the style guide block when there is one."

const editOutput = "Only the revised passage as plain Markdown text: no quotation marks around it, no heading, no list, no comment before or after."

type editSpec struct {
	id, name, desc string
	params         []ParamSpec
	task           func(p map[string]string) string
	op             string
	maxOut         int
	ratio          int
	class          string
	// system, when set, replaces editSystem
	system, output string
}

var selectionChips = []string{ChipSelection, ChipStyleGuide, ChipBrief, ChipEntities}

func registerEdit(sp editSpec) {
	sys, out := sp.system, sp.output
	if sys == "" {
		sys = editSystem
	}
	if out == "" {
		out = editOutput
	}
	if sp.class == "" {
		sp.class = "strong"
	}
	if sp.ratio == 0 {
		sp.ratio = 100
	}
	if sp.op == "" {
		sp.op = "replace"
	}
	RegisterRecipe(Recipe{
		ID: sp.id, Version: 1, Name: sp.name, Desc: sp.desc + " The result is a suggestion with a diff to review; nothing is inserted unless you accept it.",
		Action: ActionSelection, Result: ResultText, Scope: ScopeSelection,
		DefaultChips: selectionChips, Params: sp.params, MaxOutputTokens: sp.maxOut, ExpectedOutputRatio: sp.ratio, Class: sp.class,
		Build: func(b BuildContext) (Prompt, error) {
			if _, ok := b.Chip(ChipSelection); !ok {
				return Prompt{}, fmt.Errorf("the selected text is required: this action works on a selection")
			}
			return Prompt{System: sys, Output: out, Task: sp.task(b.Params), Blocks: b.Blocks()}, nil
		},
		Parse: func(c ParseContext) (Parsed, error) {
			orig := ""
			if ch, ok := firstChip(c.Chips, ChipSelection); ok {
				orig = ch.Text
			}
			txt, warn := CleanReplacement(c.Raw, orig)
			if txt == "" {
				return Parsed{Warnings: warn}, nil
			}
			return Parsed{Text: txt, Data: mustJSON(EditData{Text: txt, Op: sp.op}), Warnings: warn}, nil
		},
	})
}

func pct(p map[string]string, def int) int {
	n, err := strconv.Atoi(p["percent"])
	if err != nil || n < 5 || n > 80 {
		return def
	}
	return n
}

func init() {
	registerEdit(editSpec{id: "rewrite", name: "Rewrite", maxOut: 1400,
		desc:   "Rewrite the selected text following your instruction.",
		params: []ParamSpec{{Name: "instruction", Desc: "How to rewrite it.", Required: true, MaxLen: 1000}},
		task: func(p map[string]string) string {
			return "Rewrite the selected text. The author's instruction: " + p["instruction"]
		}})
	registerEdit(editSpec{id: "tighten", name: "Tighten", maxOut: 1000, ratio: 80,
		desc:   "Cut words from the selected text without losing meaning or voice.",
		params: []ParamSpec{{Name: "percent", Desc: "About how much to cut, 5 to 80.", Default: "20", MaxLen: 3}},
		task: func(p map[string]string) string {
			return fmt.Sprintf("Tighten the selected text: cut about %d%% of its words (filler, repetition, over-explaining) and keep what carries meaning and voice.", pct(p, 20))
		}})
	registerEdit(editSpec{id: "expand", name: "Expand", maxOut: 1600, ratio: 140,
		desc:   "Expand the selected text with detail that fits what is already there.",
		params: []ParamSpec{{Name: "percent", Desc: "About how much longer, 5 to 80.", Default: "30", MaxLen: 3}},
		task: func(p map[string]string) string {
			return fmt.Sprintf("Expand the selected text by about %d%%: add concrete detail that fits the material. Do not add events or facts that contradict it.", pct(p, 30))
		}})
	registerEdit(editSpec{id: "simplify", name: "Simplify", maxOut: 1000, ratio: 85,
		desc: "Plainer words and shorter sentences, same meaning.",
		task: func(map[string]string) string {
			return "Simplify the selected text: plainer words, shorter sentences, the same meaning and events."
		}})
	registerEdit(editSpec{id: "tone", name: "Change tone", maxOut: 1200,
		desc:   "Change the tone or register of the selected text.",
		params: []ParamSpec{{Name: "tone", Desc: "The tone or register to move to (for example: warmer, formal, wry).", Required: true, MaxLen: 200}},
		task: func(p map[string]string) string {
			return "Change the tone or register of the selected text to: " + p["tone"] + ". Keep what happens and who says what."
		}})
	registerEdit(editSpec{id: "grammar", name: "Fix grammar and punctuation", maxOut: 1000, ratio: 100, class: "cheap",
		desc: "Fix grammar, spelling and punctuation only.",
		task: func(map[string]string) string {
			return "Fix grammar, spelling and punctuation in the selected text. Change nothing else: not the wording, not the style, not the voice."
		}})
	registerEdit(editSpec{id: "continue", name: "Continue", op: "insert_after", maxOut: 900, ratio: 100,
		desc:   "Continue from the end of the selected text (the text before your cursor).",
		params: []ParamSpec{{Name: "length", Desc: "About how much to write.", Enum: []string{"short", "medium", "long"}, Default: "medium", MaxLen: 10}},
		system: "You continue the author's manuscript from where the selected text ends. Match the author's voice, tense, point of view and pacing. Do not repeat the selected text. Follow the style guide block when there is one.",
		output: "Only the new text that follows the selected passage, as plain Markdown: no comment, no heading, no repetition of the passage.",
		task: func(p map[string]string) string {
			n := map[string]int{"short": 60, "medium": 150, "long": 300}[p["length"]]
			if n == 0 {
				n = 150
			}
			return fmt.Sprintf("Continue the selected text from where it ends, in about %d words.", n)
		}})
	registerEdit(editSpec{id: "describe", name: "Add sensory detail", maxOut: 1400, ratio: 140,
		desc:   "Add sensory detail to the selected passage.",
		params: []ParamSpec{{Name: "sense", Desc: "Which sense to draw on.", Enum: []string{"any", "sight", "sound", "smell", "taste", "touch"}, Default: "any", MaxLen: 10}},
		task: func(p map[string]string) string {
			s := p["sense"]
			if s == "" || s == "any" {
				return "Add sensory detail to the selected text (whichever senses suit it). Keep the events and the voice; show, do not list."
			}
			return "Add detail of " + s + " to the selected text. Keep the events and the voice; show, do not list."
		}})
	registerEdit(editSpec{id: "dialogue", name: "Polish dialogue", maxOut: 1200,
		desc: "Polish the dialogue of the selected passage: rhythm and distinct voices.",
		task: func(map[string]string) string {
			return "Polish the dialogue in the selected text: natural rhythm, distinct voices, no speech that only repeats what the action shows. Keep what is said unless it is clumsy, and keep the quotation and tag conventions the author uses."
		}})
}

var (
	labelLine   = regexp.MustCompile(`(?i)^(here( is|'s| are)|sure|certainly|revised|rewritten|the (revised|rewritten))[^\n]{0,80}:\s*$`)
	wrapQuotes  = [][2]string{{"\"", "\""}, {"“", "”"}, {"«", "»"}}
	replaceTags = regexp.MustCompile("(?is)```(?:replacement|markdown|md|text)?[ \t]*\n(.*?)```")
)

// CleanReplacement reads a model's answer to a selection recipe as one
// replacement: it takes the text out of a single code fence, drops a leading
// "Here is the revised text:" line, and refuses anything that may not be
// inserted. It returns "" and a plain warning when there is nothing usable. It
// never edits the words themselves.
func CleanReplacement(raw, original string) (string, []string) {
	s := strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
	if m := replaceTags.FindStringSubmatch(s); m != nil {
		if rest := strings.TrimSpace(replaceTags.ReplaceAllString(s, "")); (rest == "" || labelLine.MatchString(rest)) && !strings.Contains(original, "```") {
			s = strings.TrimSpace(m[1])
		}
	}
	if i := strings.Index(s, "\n"); i > 0 && labelLine.MatchString(s[:i]) {
		s = strings.TrimSpace(s[i:])
	}
	orig := strings.TrimSpace(original)
	for _, q := range wrapQuotes {
		if len(s) > 2 && strings.HasPrefix(s, q[0]) && strings.HasSuffix(s, q[1]) && wrapsOnce(s, q) && !strings.HasPrefix(orig, q[0]) {
			s = strings.TrimSpace(s[len(q[0]) : len(s)-len(q[1])])
		}
	}
	if s == "" {
		return "", []string{"The model returned no text to suggest."}
	}
	if why := InsertableAgainst(s, original); why != "" {
		return "", []string{"The answer is shown as text but was not offered as a suggestion: " + why + "."}
	}
	return s, nil
}

// ---- alternatives ----------------------------------------------------------------

// AlternativesData is the parsed answer of the alternatives recipe: candidates,
// not yet alternatives.
type AlternativesData struct {
	Alternatives []string `json:"alternatives"`
	Dropped      int      `json:"dropped"`
	Truncated    bool     `json:"truncated"`
}

func init() {
	RegisterRecipe(Recipe{
		ID: "alternatives", Version: 1, Name: "Alternative wordings", Action: ActionAlternatives, Result: ResultText, Scope: ScopeSelection,
		Desc:         "Suggest other ways to word the selected text, as candidates you can add to an alternative (marked as AI) one by one. Nothing changes in the text unless you accept and choose one.",
		DefaultChips: selectionChips,
		Params: []ParamSpec{
			{Name: "count", Desc: "How many candidates, 1 to 5.", Default: "3", MaxLen: 1},
			{Name: "scope", Desc: "The size of the thing being reworded.", Enum: []string{"word", "sentence", "paragraph"}, Default: "sentence", MaxLen: 10},
		},
		MaxOutputTokens: 1400, ExpectedOutputRatio: 220, Class: "strong",
		Build: func(b BuildContext) (Prompt, error) {
			if _, ok := b.Chip(ChipSelection); !ok {
				return Prompt{}, fmt.Errorf("the selected text is required: this action works on a selection")
			}
			n := altCount(b.Params)
			unit := map[string]string{"word": "word", "sentence": "sentence", "paragraph": "paragraph"}[b.Params["scope"]]
			if unit == "" {
				unit = "sentence"
			}
			return Prompt{
				System: "You offer the author other ways to word a short passage of their manuscript. Each candidate keeps the meaning, the events and the point of view, fits the author's voice, and differs from the others in rhythm, word choice or emphasis. Follow the style guide block when there is one.",
				Output: fmt.Sprintf(`A JSON object {"alternatives": ["...", "..."]} with exactly %d strings, each a complete replacement for the selected %s, in plain Markdown on one line (a paragraph is one paragraph). No commentary.`, n, unit),
				Task:   fmt.Sprintf("Give %d alternative wordings of the selected text (a %s).", n, unit),
				Blocks: b.Blocks(),
			}, nil
		},
		Parse: parseAlternatives,
	})
}

func altCount(p map[string]string) int {
	n, err := strconv.Atoi(p["count"])
	if err != nil || n < 1 {
		return 3
	}
	return min(n, MaxAlternatives)
}

var numbered = regexp.MustCompile(`^\s*(?:\d+[.)]|[-*•])\s+(.+)$`)

func parseAlternatives(c ParseContext) (Parsed, error) {
	want := altCount(c.Params)
	scope := c.Params["scope"]
	if scope == "" {
		scope = alts.ScopeSentence
	}
	orig := ""
	if ch, ok := firstChip(c.Chips, ChipSelection); ok {
		orig = strings.TrimSpace(ch.Text)
	}
	var cand []string
	data := AlternativesData{Alternatives: []string{}}
	if items, trunc, over, ok := SalvageList(c.Raw, "alternatives", 4*MaxAlternatives); ok {
		data.Truncated = trunc
		data.Dropped += b2i(over)
		for _, it := range items {
			var s string
			if json.Unmarshal(it, &s) != nil {
				var o struct {
					Text        string `json:"text"`
					Alternative string `json:"alternative"`
				}
				if json.Unmarshal(it, &o) != nil || (o.Text == "" && o.Alternative == "") {
					data.Dropped++
					continue
				}
				s = o.Text + o.Alternative
			}
			cand = append(cand, s)
		}
	} else if scope != alts.ScopeParagraph { // a plain numbered or bulleted list
		for _, ln := range strings.Split(strings.ReplaceAll(c.Raw, "\r\n", "\n"), "\n") {
			if m := numbered.FindStringSubmatch(ln); m != nil {
				cand = append(cand, m[1])
			}
		}
	}
	seen := map[string]bool{orig: true}
	var warn []string
	for _, s := range cand {
		s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
		if len(data.Alternatives) >= want {
			data.Dropped++
			continue
		}
		if s == "" || seen[s] {
			data.Dropped++
			continue
		}
		if InsertableAgainst(s, orig) != "" || alts.CheckVariantText(s, scope, alts.Limits{}) != nil {
			data.Dropped++
			continue
		}
		seen[s] = true
		data.Alternatives = append(data.Alternatives, s)
	}
	if len(data.Alternatives) == 0 {
		return Parsed{Warnings: []string{"The model gave no usable alternatives; the text is shown as it came."}}, nil
	}
	if data.Dropped > 0 {
		warn = append(warn, fmt.Sprintf("%d candidate(s) were left out: empty, repeated, too long for the scope, or not text that may be inserted.", data.Dropped))
	}
	if data.Truncated {
		warn = append(warn, "The answer was cut off; the candidates that finished are kept.")
	}
	return Parsed{Text: strings.Join(data.Alternatives, "\n"), Data: mustJSON(data), Warnings: warn}, nil
}

func firstChip(chips []Chip, kind string) (Chip, bool) {
	for _, c := range chips {
		if c.Kind == kind {
			return c, true
		}
	}
	return Chip{}, false
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// wrapsOnce reports whether the quotation marks q open and close the whole of s
// and appear nowhere else (so a passage that has speech inside it is left alone).
func wrapsOnce(s string, q [2]string) bool {
	if q[0] == q[1] {
		return strings.Count(s, q[0]) == 2
	}
	return strings.Count(s, q[0]) == 1 && strings.Count(s, q[1]) == 1
}

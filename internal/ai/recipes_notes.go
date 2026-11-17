package ai

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Critique and the continuity audit (policy actions "critique" and
// "continuity"). Both return NOTES: observations anchored to short quotes of the
// text, with a category and a severity. The prompts say notes only, and the
// parser enforces it: a note is one or two sentences, it may carry one short
// example (at most 160 characters, flagged when it was cut), and any field that
// looks like replacement prose is ignored. A continuity finding must cite the
// facts it rests on, and a citation counts only if the fact was in the request:
// an entity sheet, a timeline event or finding, an earlier chapter's summary.

// NoteCategories are the categories a critique uses. Anything else a model
// writes becomes "other".
var NoteCategories = []string{"pacing", "clarity", "voice", "continuity", "dialogue", "show_dont_tell", "structure", "character", "description", "tension", "style", "other"}

var categoryAlias = map[string]string{
	"show don't tell": "show_dont_tell", "show-dont-tell": "show_dont_tell", "showing": "show_dont_tell", "show_don't_tell": "show_dont_tell", "telling": "show_dont_tell",
	"pace": "pacing", "rhythm": "pacing", "clear": "clarity", "confusing": "clarity", "tone": "voice", "pov": "voice", "point of view": "voice",
	"characterisation": "character", "characterization": "character", "setting": "description", "imagery": "description", "prose": "style",
	"plot": "structure", "consistency": "continuity", "stakes": "tension",
}

func normCategory(s string) string {
	s = strings.ToLower(CleanLine(s, 40))
	s = strings.ReplaceAll(s, "_", " ")
	if a, ok := categoryAlias[s]; ok {
		return a
	}
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	for _, c := range NoteCategories {
		if c == s {
			return c
		}
	}
	return "other"
}

func normSeverity(s string) string {
	switch strings.ToLower(CleanLine(s, 20)) {
	case "high", "major", "serious", "critical", "error":
		return "high"
	case "low", "minor", "nit", "trivial", "info", "suggestion":
		return "low"
	}
	return "medium"
}

func maxNotes(p map[string]string) int {
	n, err := strconv.Atoi(p["max_notes"])
	if err != nil || n < 1 {
		return 12
	}
	return min(n, 30)
}

const notesSystem = "You are a developmental editor giving notes on part of an author's manuscript. You give NOTES ONLY: what works, what does not, and why, each anchored to a short quote of the text. You never rewrite the author's prose and never write replacement paragraphs; one short example of at most a dozen words is the most you may show. Follow the style guide and the brief when there are any."

func notesOutput(n int) string {
	return fmt.Sprintf(`A JSON object {"notes": [...]} with at most %d notes, most important first. Each note: {"quote": "the exact words from the text, at most 25 words", "before": "up to 6 words that come right before the quote", "category": "one of %s", "severity": "low, medium or high", "note": "one or two sentences", "example": "optional, at most 12 words"}. Quotes are copied letter for letter from the text. No other commentary.`,
		n, strings.Join(NoteCategories, ", "))
}

func init() {
	for _, v := range []struct {
		id, name, scope string
		chips           []string
		what            string
	}{
		{"critique.chapter", "Critique a chapter", ScopeDocument, []string{ChipDocument, ChipBrief, ChipStyleGuide, ChipEntities}, "the chapter"},
		{"critique.selection", "Critique a selection", ScopeSelection, []string{ChipSelection, ChipParagraph, ChipBrief, ChipStyleGuide, ChipEntities}, "the selected text"},
	} {
		v := v
		RegisterRecipe(Recipe{
			ID: v.id, Version: 1, Name: v.name, Action: ActionCritique, Result: ResultNotes, Scope: v.scope,
			Desc:         "Notes on " + v.what + ": pacing, clarity, voice, dialogue and more, each anchored to a quote with a severity. Notes only: no rewritten prose. They are kept as a review you can work through, note by note.",
			DefaultChips: v.chips,
			Params: []ParamSpec{
				{Name: "focus", Desc: "What to look at, in your words (for example: pacing and dialogue).", MaxLen: 500},
				{Name: "max_notes", Desc: "The most notes to return, 1 to 30.", Default: "12", MaxLen: 2},
			},
			MaxOutputTokens: 2200, ExpectedOutputRatio: 25, Class: "strong",
			Build: func(b BuildContext) (Prompt, error) {
				task := "Give notes on " + v.what + "."
				if f := strings.TrimSpace(b.Params["focus"]); f != "" {
					task += " The author wants you to look at: " + f + "."
				}
				if v.scope == ScopeSelection {
					if _, ok := b.Chip(ChipSelection); !ok {
						return Prompt{}, fmt.Errorf("the selected text is required: this action works on a selection")
					}
				} else if _, ok := b.Chip(ChipDocument); !ok {
					return Prompt{}, fmt.Errorf("the chapter's text is required")
				}
				return Prompt{System: notesSystem, Output: notesOutput(maxNotes(b.Params)), Task: task, Blocks: b.Blocks()}, nil
			},
			Parse: func(c ParseContext) (Parsed, error) { return parseNotes(c, "notes", false, "") },
		})
	}
	for _, v := range []struct {
		id, name, scope string
		chips           []string
		what            string
	}{
		{"continuity.chapter", "Continuity audit of a chapter", ScopeDocument, []string{ChipDocument, ChipEntities, ChipSummaries, ChipTimeline, ChipBrief}, "the chapter"},
		{"continuity.selection", "Continuity audit of a selection", ScopeSelection, []string{ChipSelection, ChipParagraph, ChipEntities, ChipSummaries, ChipTimeline, ChipBrief}, "the selected text"},
	} {
		v := v
		RegisterRecipe(Recipe{
			ID: v.id, Version: 1, Name: v.name, Action: ActionContinuity, Result: ResultNotes, Scope: v.scope,
			Desc:         "Check " + v.what + " against the Compendium (entity sheets you may read), the timeline and the summaries of earlier chapters. Each finding cites the fact it rests on; a finding that cites something that was not sent is discarded. The timeline's own deterministic check is separate and unchanged.",
			DefaultChips: v.chips,
			Params:       []ParamSpec{{Name: "max_notes", Desc: "The most findings to return, 1 to 30.", Default: "12", MaxLen: 2}},
			// the entities chip is automatic: the sheets of the names the text uses
			MaxOutputTokens: 2200, ExpectedOutputRatio: 25, Class: "strong",
			Build: func(b BuildContext) (Prompt, error) {
				if v.scope == ScopeSelection {
					if _, ok := b.Chip(ChipSelection); !ok {
						return Prompt{}, fmt.Errorf("the selected text is required: this action works on a selection")
					}
				} else if _, ok := b.Chip(ChipDocument); !ok {
					return Prompt{}, fmt.Errorf("the chapter's text is required")
				}
				out := fmt.Sprintf(`A JSON object {"findings": [...]} with at most %d findings. Each: {"quote": "the exact words from the text that contradict a fact, at most 25 words", "before": "up to 6 words right before the quote", "severity": "low, medium or high", "note": "one or two sentences saying what contradicts what", "sources": [{"kind": "entity|event|finding|doc", "id": "the id from the material", "field": "optional: the entity field"}]}. Entity ids are the source= of an entity block; event, finding and doc ids are the ids in square brackets in the material, such as [event:ID]. A finding with no source from the material is not a finding. If nothing contradicts, return {"findings": []}. No other commentary.`, maxNotes(b.Params))
				return Prompt{
					System: "You check an author's manuscript for continuity. Compare " + v.what + " with the facts in the material: entity sheets, the timeline's events and its own findings, and the summaries of earlier chapters. Report only contradictions you can cite to a fact in the material; never use outside knowledge and never invent a fact or an id. You report; you do not rewrite the author's prose.",
					Output: out,
					Task:   "Audit " + v.what + " for continuity against the material.",
					Blocks: b.Blocks(),
				}, nil
			},
			Parse: func(c ParseContext) (Parsed, error) { return parseNotes(c, "findings", true, "continuity") },
		})
	}
}

var citeRe = regexp.MustCompile(`\[(event|finding|doc):([A-Za-z0-9_.:\-]{1,64})\]`)

// Citable is the set of facts a request supplied: what a note may cite.
type Citable struct {
	Entities map[string]string // entity id -> the sheet's text, lower case
	Others   map[string]bool   // "event:ID", "finding:ID", "doc:ID"
}

// CitableOf reads the facts out of the chips of a request. Entity chips name
// their entity by Source; the other kinds are the ids in square brackets in the
// chips that carry them, and the target document itself.
func CitableOf(chips []Chip) Citable {
	c := Citable{Entities: map[string]string{}, Others: map[string]bool{}}
	for _, ch := range chips {
		switch ch.Kind {
		case ChipEntities:
			if ch.Source != "" {
				c.Entities[ch.Source] = strings.ToLower(ch.Text)
			}
		case ChipDocument:
			if ch.Source != "" {
				c.Others["doc:"+ch.Source] = true
			}
		case ChipSummaries, ChipTimeline:
			for _, m := range citeRe.FindAllStringSubmatch(ch.Text, -1) {
				c.Others[m[1]+":"+m[2]] = true
			}
		}
	}
	return c
}

// check returns the source if the request supplied it, with the field kept only
// when the sheet really has such a field.
func (c Citable) check(s Source) (Source, bool) {
	s.Kind = strings.ToLower(CleanLine(s.Kind, 12))
	s.ID = CleanLine(s.ID, 64)
	switch s.Kind {
	case "entity":
		sheet, ok := c.Entities[s.ID]
		if !ok {
			return Source{}, false
		}
		s.Field = CleanLine(s.Field, 40)
		if s.Field != "" && !strings.Contains(sheet, strings.ToLower(s.Field)) {
			s.Field = ""
		}
		return s, true
	case "event", "finding", "doc":
		s.Field = ""
		return s, c.Others[s.Kind+":"+s.ID]
	}
	return Source{}, false
}

// rawNote is a note as models write it: the keys vary, so every plausible one
// is read, and the ones that would carry replacement prose are not.
type rawNote struct {
	Quote    string          `json:"quote"`
	Before   string          `json:"before"`
	After    string          `json:"after"`
	Category string          `json:"category"`
	Severity string          `json:"severity"`
	Note     string          `json:"note"`
	Text     string          `json:"text"`
	Comment  string          `json:"comment"`
	Issue    string          `json:"issue"`
	Example  string          `json:"example"`
	Sources  json.RawMessage `json:"sources"`
	Source   json.RawMessage `json:"source"`
}

func parseNotes(c ParseContext, key string, needSource bool, force string) (Parsed, error) {
	items, trunc, over, ok := SalvageList(c.Raw, key, 4*MaxNotes)
	if !ok {
		return Parsed{Warnings: []string{"The answer was not a list of notes; it is shown as text and nothing was kept."}}, nil
	}
	limit := min(MaxNotes, maxNotes(c.Params))
	cite := CitableOf(c.Chips)
	data := NotesData{Notes: []Note{}, Truncated: trunc, Dropped: b2i(over)}
	flagged := 0
	for _, it := range items {
		var r rawNote
		if json.Unmarshal(it, &r) != nil {
			data.Dropped++
			continue
		}
		if len(data.Notes) >= limit {
			data.Dropped++
			continue
		}
		txt := CleanLine(firstOf(r.Note, r.Text, r.Comment, r.Issue), 4*MaxNoteChars)
		if txt == "" {
			data.Dropped++
			continue
		}
		n := Note{Quote: CleanQuote(r.Quote, MaxQuoteChars), Category: normCategory(r.Category), Severity: normSeverity(r.Severity)}
		n.Before, n.After = tailRunesStr(CleanContext(r.Before, 120), 40), headRunesStr(CleanContext(r.After, 120), 40)
		if force != "" {
			n.Category = force
		}
		if t, cut := cutSentence(txt, MaxNoteChars); cut {
			txt, n.Trimmed = t, true
		}
		n.Text = txt
		if ex := CleanLine(r.Example, 4*MaxExampleChars); ex != "" {
			if t, cut := cutSentence(ex, MaxExampleChars); cut {
				ex, n.Trimmed = t, true
			}
			if Insertable(ex) == "" {
				n.Example = ex
			}
		}
		if needSource {
			raw := r.Sources
			if len(raw) == 0 {
				raw = r.Source
			}
			n.Sources = citedSources(raw, cite)
			if len(n.Sources) == 0 {
				data.Dropped++
				continue
			}
		}
		if n.Trimmed {
			flagged++
		}
		data.Notes = append(data.Notes, n)
	}
	var warn []string
	if data.Dropped > 0 {
		if needSource {
			warn = append(warn, fmt.Sprintf("%d finding(s) were left out: unreadable, over the limit, or citing a source that was not in the request.", data.Dropped))
		} else {
			warn = append(warn, fmt.Sprintf("%d note(s) were left out: unreadable, empty or over the limit.", data.Dropped))
		}
	}
	if flagged > 0 {
		warn = append(warn, fmt.Sprintf("%d note(s) were shortened to the length a note may have; a critique gives notes, not rewritten prose.", flagged))
	}
	if trunc {
		warn = append(warn, "The answer was cut off; the notes that finished are kept.")
	}
	if len(data.Notes) == 0 && !trunc && data.Dropped == 0 {
		warn = append(warn, "The model found nothing to note.")
	}
	return Parsed{Data: mustJSON(data), Warnings: warn}, nil
}

func citedSources(raw json.RawMessage, c Citable) []Source {
	if len(raw) == 0 {
		return nil
	}
	var list []Source
	if json.Unmarshal(raw, &list) != nil {
		var one Source
		if json.Unmarshal(raw, &one) != nil {
			return nil
		}
		list = []Source{one}
	}
	var out []Source
	seen := map[string]bool{}
	for _, s := range list {
		s, ok := c.check(s)
		if !ok || seen[s.Kind+":"+s.ID+":"+s.Field] {
			continue
		}
		seen[s.Kind+":"+s.ID+":"+s.Field] = true
		out = append(out, s)
		if len(out) == 4 {
			break
		}
	}
	return out
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func tailRunesStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

func headRunesStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

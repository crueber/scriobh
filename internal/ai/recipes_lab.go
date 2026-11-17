package ai

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The Lab (policy action "lab") marks, it never rewrites. The recipes here are
// the ones that need a model's judgment: sentences that are hard to follow,
// words that do not fit a stated tone, and the four trim modes, each of which
// marks the exact words that could go. Long sentences, hedge words and
// punctuation are found by local analysers (internal/ai/analyse) with no model.
//
// A mark is a quote with a little context: the server finds it in the text
// itself and keeps only what it can find exactly once. The model's own idea of
// a position is never read.

// Lab mark kinds.
const (
	MarkConvoluted  = "convoluted_sentence"
	MarkTone        = "tone_mismatch"
	MarkPunct       = "punctuation_fix"
	MarkLong        = "long_sentence"
	MarkHedge       = "hedge_words"
	MarkTrimFiller  = "trim_filler"
	MarkTrimAdverbs = "trim_adverbs"
	MarkTrimRepeat  = "trim_repetition"
	MarkTrimTags    = "trim_dialogue_tags"
)

// MarkKinds lists every kind a mark can have.
var MarkKinds = []string{MarkConvoluted, MarkTone, MarkPunct, MarkLong, MarkHedge, MarkTrimFiller, MarkTrimAdverbs, MarkTrimRepeat, MarkTrimTags}

// TrimModes maps a trim recipe's mode to the mark kind and the instruction.
var TrimModes = map[string]struct{ Kind, Task string }{
	"filler":        {MarkTrimFiller, "Mark words and short phrases that can be deleted without changing the meaning or the voice: filler such as just, really, very, quite, in order to, began to, started to, a bit. Mark exactly the words to delete."},
	"adverbs":       {MarkTrimAdverbs, "Mark adverbs that prop up a weak verb or repeat what the verb or the dialogue already shows (for example said quietly, walked slowly). Mark exactly the adverb to delete."},
	"repetition":    {MarkTrimRepeat, "Mark words and phrases that repeat something said a few lines earlier in the text. Mark the later, weaker occurrence: exactly the words to delete."},
	"dialogue_tags": {MarkTrimTags, "Mark dialogue tags and attributions that are not needed because the speaker is already clear (for example she said, he replied). Mark exactly the tag words to delete, not the speech."},
}

const labSystem = "You help an author find places in their manuscript to look at again. You MARK ranges and say why; you never rewrite anything. Follow the style guide and the brief when there are any."

func marksOutput(n int, what string) string {
	return fmt.Sprintf(`A JSON object {"marks": [...]} with at most %d marks, most useful first. Each mark: {"quote": "%s, copied letter for letter from the text", "before": "the 20 characters of the text right before the quote", "after": "the 20 characters right after it", "reason": "one short sentence"}. No other commentary.`, n, what)
}

func maxMarks(p map[string]string) int {
	n, err := strconv.Atoi(p["max_marks"])
	if err != nil || n < 1 {
		return 20
	}
	return min(n, 60)
}

var labParams = ParamSpec{Name: "max_marks", Desc: "The most marks to return, 1 to 60.", Default: "20", MaxLen: 2}

func init() {
	RegisterRecipe(Recipe{
		ID: "lab.convoluted", Version: 1, Name: "Lab: convoluted sentences", Action: ActionLab, Result: ResultMarks, Scope: ScopeDocument,
		Desc:         "Mark sentences that are hard to follow (knotted clauses, buried subjects). Marks only; nothing is rewritten.",
		DefaultChips: []string{ChipDocument, ChipStyleGuide},
		Params:       []ParamSpec{labParams}, MaxOutputTokens: 2000, ExpectedOutputRatio: 25, Class: "strong",
		Build: func(b BuildContext) (Prompt, error) {
			if _, ok := b.Chip(ChipDocument); !ok {
				return Prompt{}, fmt.Errorf("the chapter's text is required")
			}
			return Prompt{System: labSystem, Output: marksOutput(maxMarks(b.Params), "the sentence, or the knotted part of it (at most 40 words)"), Blocks: b.Blocks(),
				Task: "Mark the sentences a reader would have to read twice to follow. Skip dialogue that is meant to sound broken."}, nil
		},
		Parse: func(c ParseContext) (Parsed, error) { return parseMarks(c, MarkConvoluted) },
	})
	RegisterRecipe(Recipe{
		ID: "lab.tone", Version: 1, Name: "Lab: tone mismatch", Action: ActionLab, Result: ResultMarks, Scope: ScopeDocument,
		Desc:         "Mark passages whose tone does not fit the tone you state (or the project's brief). Marks only; nothing is rewritten.",
		DefaultChips: []string{ChipDocument, ChipBrief, ChipStyleGuide},
		Params: []ParamSpec{{Name: "tone", Desc: "The tone the text should have (for example: dry and understated). Empty: judge against the brief.", MaxLen: 300},
			labParams},
		MaxOutputTokens: 2000, ExpectedOutputRatio: 25, Class: "strong",
		Build: func(b BuildContext) (Prompt, error) {
			if _, ok := b.Chip(ChipDocument); !ok {
				return Prompt{}, fmt.Errorf("the chapter's text is required")
			}
			tone := strings.TrimSpace(b.Params["tone"])
			if _, hasBrief := b.Chip(ChipBrief); tone == "" && !hasBrief {
				return Prompt{}, fmt.Errorf("state the tone, or include the project's brief to judge against")
			}
			task := "Mark passages whose tone does not fit"
			if tone != "" {
				task += " the tone the author wants: " + tone + "."
			} else {
				task += " the project's brief."
			}
			return Prompt{System: labSystem, Output: marksOutput(maxMarks(b.Params), "the passage that jars (at most 40 words)"), Blocks: b.Blocks(), Task: task}, nil
		},
		Parse: func(c ParseContext) (Parsed, error) { return parseMarks(c, MarkTone) },
	})
	RegisterRecipe(Recipe{
		ID: "lab.trim", Version: 1, Name: "Lab: trim", Action: ActionLab, Result: ResultMarks, Scope: ScopeDocument,
		Desc:         "Mark words that could be deleted, in one of four modes: filler, adverbs, repetition or dialogue tags. Each mark is a candidate deletion you accept or dismiss on its own.",
		DefaultChips: []string{ChipDocument, ChipStyleGuide},
		Params: []ParamSpec{{Name: "mode", Desc: "What to look for.", Enum: []string{"filler", "adverbs", "repetition", "dialogue_tags"}, Required: true, MaxLen: 20},
			{Name: "max_marks", Desc: "The most marks to return, 1 to 60.", Default: "30", MaxLen: 2}},
		MaxOutputTokens: 2400, ExpectedOutputRatio: 30, Class: "cheap",
		Build: func(b BuildContext) (Prompt, error) {
			if _, ok := b.Chip(ChipDocument); !ok {
				return Prompt{}, fmt.Errorf("the chapter's text is required")
			}
			m, ok := TrimModes[b.Params["mode"]]
			if !ok {
				return Prompt{}, fmt.Errorf("unknown trim mode")
			}
			return Prompt{System: labSystem, Output: marksOutput(maxMarks(b.Params), "the exact words that could be deleted (at most 12 words)"), Blocks: b.Blocks(), Task: m.Task}, nil
		},
		Parse: func(c ParseContext) (Parsed, error) {
			m, ok := TrimModes[c.Params["mode"]]
			if !ok {
				return Parsed{}, fmt.Errorf("unknown trim mode")
			}
			return parseMarks(c, m.Kind)
		},
	})
}

// parseMarks reads a Lab answer. The kind comes from the recipe, never from the
// model; a quote is required and bounded; positions the model may have sent are
// not read at all.
func parseMarks(c ParseContext, kind string) (Parsed, error) {
	items, trunc, over, ok := SalvageList(c.Raw, "marks", 4*MaxMarks)
	if !ok {
		return Parsed{Warnings: []string{"The answer was not a list of marks; it is shown as text and nothing was kept."}}, nil
	}
	limit := min(MaxMarks, maxMarks(c.Params))
	data := MarksData{Marks: []Mark{}, Truncated: trunc, Dropped: b2i(over)}
	seen := map[string]bool{}
	for _, it := range items {
		var r struct {
			Quote  string `json:"quote"`
			Before string `json:"before"`
			After  string `json:"after"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal(it, &r) != nil {
			data.Dropped++
			continue
		}
		q := CleanQuote(r.Quote, MaxQuoteChars)
		if q == "" || len(data.Marks) >= limit {
			data.Dropped++
			continue
		}
		m := Mark{Kind: kind, Quote: q, Before: tailRunesStr(CleanContext(r.Before, 120), 40), After: headRunesStr(CleanContext(r.After, 120), 40), Reason: CleanLine(r.Reason, MaxReasonChars)}
		key := m.Before + "\x00" + m.Quote + "\x00" + m.After
		if seen[key] {
			data.Dropped++
			continue
		}
		seen[key] = true
		data.Marks = append(data.Marks, m)
	}
	var warn []string
	if data.Dropped > 0 {
		warn = append(warn, fmt.Sprintf("%d mark(s) were left out: unreadable, repeated or over the limit.", data.Dropped))
	}
	if trunc {
		warn = append(warn, "The answer was cut off; the marks that finished are kept.")
	}
	return Parsed{Data: mustJSON(data), Warnings: warn}, nil
}

package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Summaries, entity extraction and chat. Like every recipe these return
// suggestions: a summary is written into a synopsis, a proposed entity into a
// Compendium and a chat answer into the text only when the writer accepts it.

// SummaryData is the parsed answer of a summary recipe.
type SummaryData struct {
	Short string `json:"short,omitempty" desc:"One line, at most 300 characters: what goes in the short synopsis."`
	Long  string `json:"long,omitempty" desc:"A paragraph: what goes in the long synopsis."`
}

func init() {
	RegisterRecipe(Recipe{
		ID: "summary.chapter", Version: 1, Name: "Summarise a chapter", Action: ActionSummaries, Result: ResultText, Scope: ScopeDocument,
		Desc:         "Summarise a chapter into its synopsis (one line and a paragraph), using the summary of the previous chapter as context. The summary is a suggestion; accepting it writes the synopsis.",
		DefaultChips: []string{ChipDocument, ChipPreviousSummary, ChipBrief, ChipStyleGuide},
		Params:       []ParamSpec{{Name: "length", Desc: "Which synopsis to write.", Enum: []string{"short", "long", "both"}, Default: "both", MaxLen: 5}},
		// a summary is a fraction of the chapter
		MaxOutputTokens: 700, ExpectedOutputRatio: 10, Class: "cheap",
		Build: func(b BuildContext) (Prompt, error) {
			if _, ok := b.Chip(ChipDocument); !ok {
				return Prompt{}, errors.New("the chapter's text is required")
			}
			what := `"short" (one sentence, at most 200 characters) and "long" (one paragraph of 80 to 160 words)`
			switch b.Params["length"] {
			case "short":
				what = `"short" (one sentence, at most 200 characters)`
			case "long":
				what = `"long" (one paragraph of 80 to 160 words)`
			}
			return Prompt{
				System: "You summarise a chapter of an author's manuscript for the author's own outline: what happens, to whom, and what changes, in plain past-tense prose with no judgement and no spoilers beyond the chapter. Use the previous chapter's summary only for context; summarise the chapter.",
				Output: "A JSON object with " + what + ". No other commentary.",
				Task:   "Summarise the chapter.",
				Blocks: b.Blocks(),
			}, nil
		},
		Parse: parseSummary,
	})
}

func parseSummary(c ParseContext) (Parsed, error) {
	var d SummaryData
	if v, ok := FirstJSONValue(c.Raw); ok {
		var r struct {
			Short   string `json:"short"`
			Long    string `json:"long"`
			Summary string `json:"summary"`
		}
		if json.Unmarshal(v, &r) == nil {
			d.Short, d.Long = r.Short, firstOf(r.Long, r.Summary)
		}
	}
	if d.Short == "" && d.Long == "" {
		d.Long = Unfence(c.Raw) // a plain-text answer is the long summary
	}
	d.Short = CleanLine(d.Short, 300)
	d.Long = CleanQuote(d.Long, 4000)
	if why := Insertable(d.Long); why != "" {
		return Parsed{Warnings: []string{"The summary was not offered: " + why + "."}}, nil
	}
	if why := Insertable(d.Short); why != "" {
		d.Short = ""
	}
	switch c.Params["length"] {
	case "short":
		d.Long = ""
	case "long":
		d.Short = ""
	}
	if d.Short == "" && d.Long == "" {
		return Parsed{Warnings: []string{"The model returned no summary."}}, nil
	}
	return Parsed{Text: firstOf(d.Long, d.Short), Data: mustJSON(d)}, nil
}

// ---- extraction ----------------------------------------------------------------

// Sheet is an entity a model proposes: a suggestion, never a record.
type Sheet struct {
	Name    string            `json:"name"`
	Kind    string            `json:"kind" enum:"character,place,item,org,concept"`
	Aliases []string          `json:"aliases,omitempty"`
	Summary string            `json:"summary,omitempty"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// SheetsData is the parsed answer of an extraction recipe.
type SheetsData struct {
	Sheets    []Sheet `json:"sheets"`
	Dropped   int     `json:"dropped"`
	Truncated bool    `json:"truncated"`
}

var fieldKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

var entityKinds = map[string]bool{"character": true, "place": true, "item": true, "org": true, "concept": true}

func init() {
	for _, v := range []struct {
		id, name, scope string
		chips           []string
		what            string
	}{
		{"extract.chapter", "Find entities in a chapter", ScopeDocument, []string{ChipDocument, ChipEntities, ChipBrief}, "the chapter"},
		{"extract.selection", "Find entities in a selection", ScopeSelection, []string{ChipSelection, ChipEntities, ChipBrief}, "the selected text"},
	} {
		v := v
		RegisterRecipe(Recipe{
			ID: v.id, Version: 1, Name: v.name, Action: ActionExtraction, Result: ResultJSON, Scope: v.scope,
			Desc:         "Propose Compendium entities (characters, places, items, organisations, concepts) found in " + v.what + ", as draft sheets you accept or dismiss one by one. Nothing is created until you accept a sheet, and a sheet that matches an existing name or alias is refused.",
			DefaultChips: v.chips, MaxOutputTokens: 2400, ExpectedOutputRatio: 25, Class: "cheap",
			Build: func(b BuildContext) (Prompt, error) {
				if v.scope == ScopeSelection {
					if _, ok := b.Chip(ChipSelection); !ok {
						return Prompt{}, errors.New("the selected text is required: this action works on a selection")
					}
				} else if _, ok := b.Chip(ChipDocument); !ok {
					return Prompt{}, errors.New("the chapter's text is required")
				}
				return Prompt{
					System: "You help an author build a story bible. List the named characters, places, items, organisations and concepts that appear in " + v.what + " and matter to the story. Do not list anything that is already described in the entity sheets of the material, by name or by alias. Use only what the text says; do not invent details.",
					Output: `A JSON object {"entities": [...]} with at most 30 entries. Each: {"name": "the name as the text writes it", "kind": "character, place, item, org or concept", "aliases": ["other names the text uses"], "summary": "one or two sentences from the text", "fields": {"key": "value"}} where fields is optional and uses short lower-case keys. No other commentary.`,
					Task:   "List the entities in " + v.what + " that are not in the sheets already.",
					Blocks: b.Blocks(),
				}, nil
			},
			Parse: parseSheets,
		})
	}
}

func parseSheets(c ParseContext) (Parsed, error) {
	items, trunc, over, ok := SalvageList(c.Raw, "entities", 4*MaxEntitiesFound)
	if !ok {
		return Parsed{Warnings: []string{"The answer was not a list of entities; it is shown as text and nothing was kept."}}, nil
	}
	data := SheetsData{Sheets: []Sheet{}, Truncated: trunc, Dropped: b2i(over)}
	seen := map[string]bool{}
	for _, it := range items {
		var r struct {
			Name    string          `json:"name"`
			Kind    string          `json:"kind"`
			Aliases []string        `json:"aliases"`
			Summary string          `json:"summary"`
			Fields  json.RawMessage `json:"fields"`
		}
		if json.Unmarshal(it, &r) != nil {
			data.Dropped++
			continue
		}
		name := CleanLine(r.Name, 100)
		kind := strings.ToLower(CleanLine(r.Kind, 20))
		if name == "" || !entityKinds[kind] || seen[strings.ToLower(name)] || len(data.Sheets) >= MaxEntitiesFound || Insertable(name) != "" {
			data.Dropped++
			continue
		}
		seen[strings.ToLower(name)] = true
		s := Sheet{Name: name, Kind: kind, Summary: CleanLine(r.Summary, 500)}
		if Insertable(s.Summary) != "" {
			s.Summary = ""
		}
		for _, a := range r.Aliases {
			a = CleanLine(a, 100)
			if a != "" && !strings.EqualFold(a, name) && Insertable(a) == "" && len(s.Aliases) < 8 {
				s.Aliases = append(s.Aliases, a)
			}
		}
		var fm map[string]any
		if len(r.Fields) > 0 && json.Unmarshal(r.Fields, &fm) == nil {
			s.Fields = map[string]string{}
			for k, v := range fm {
				val, isStr := v.(string)
				if k = strings.ToLower(CleanLine(k, 40)); !isStr || !fieldKeyRe.MatchString(k) || len(s.Fields) >= 12 {
					continue
				}
				if val = CleanLine(val, 300); val != "" && Insertable(val) == "" {
					s.Fields[k] = val
				}
			}
			if len(s.Fields) == 0 {
				s.Fields = nil
			}
		}
		data.Sheets = append(data.Sheets, s)
	}
	var warn []string
	if data.Dropped > 0 {
		warn = append(warn, fmt.Sprintf("%d entr(y/ies) were left out: unreadable, repeated, of an unknown kind or over the limit.", data.Dropped))
	}
	if trunc {
		warn = append(warn, "The answer was cut off; the entities that finished are kept.")
	}
	return Parsed{Data: mustJSON(data), Warnings: warn}, nil
}

// ---- chat -----------------------------------------------------------------------

// ChatData is the parsed answer of a chat turn: the replacement it proposed for
// the selection, if any. The rest of the answer is the text.
type ChatData struct {
	Replacement string `json:"replacement,omitempty"`
}

var replacementFence = regexp.MustCompile("(?s)```replacement[ \t]*\n(.*?)```")

func init() {
	RegisterRecipe(Recipe{
		ID: "chat", Version: 1, Name: "Chat", Action: ActionChat, Result: ResultText, Scope: ScopeNone,
		Desc:         "Talk about your writing with the context you choose. When you have a selection and the answer proposes new wording for it, that wording is offered as a suggestion you can accept with a diff; nothing else is ever inserted.",
		DefaultChips: []string{ChipSelection, ChipBrief, ChipStyleGuide}, MaxOutputTokens: 1400, ExpectedOutputRatio: 60, Class: "strong",
		Params: []ParamSpec{{Name: "message", Desc: "Your message.", Required: true, MaxLen: 4000}},
		Build: func(b BuildContext) (Prompt, error) {
			msg := b.Params["message"]
			if msg == "" {
				return Prompt{}, errors.New("a message is required")
			}
			p := Prompt{
				System: "You are the author's writing partner in a conversation about their manuscript. Answer directly and briefly, using the material when it is relevant, and say so when you cannot tell. If the author asks you to reword the selected text, put the proposed wording alone in a fenced block that opens with ```replacement and closes with ```, and say anything else outside the block; never put anything but the new wording inside it.",
				Output: "Plain text. Answer directly and briefly unless the author asks for more.",
				Task:   msg,
				Blocks: b.Blocks(),
			}
			if len(b.Turns) > 0 {
				p.Turns = append(append([]Turn{}, b.Turns...), Turn{Role: RoleUser, Content: msg})
				p.Task = ""
			}
			return p, nil
		},
		Parse: func(c ParseContext) (Parsed, error) {
			sel, ok := firstChip(c.Chips, ChipSelection)
			m := replacementFence.FindStringSubmatch(c.Raw)
			if !ok || m == nil {
				return Parsed{}, nil
			}
			txt, warn := CleanReplacement("```\n"+m[1]+"```", sel.Text)
			if txt == "" {
				return Parsed{Warnings: warn}, nil
			}
			return Parsed{Data: mustJSON(ChatData{Replacement: txt}), Warnings: warn}, nil
		},
	})
}

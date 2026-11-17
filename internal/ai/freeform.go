package ai

import "errors"

// The one recipe the core ships: a free-form request with whatever context the
// person chose. It is the worked example of a recipe, what the core's own tests
// run, and a useful action in its own right ("ask about this passage").
func init() {
	RegisterRecipe(Recipe{
		ID: "freeform", Version: 1, Name: "Ask", Action: ActionChat, Result: ResultText, Scope: ScopeNone,
		Desc:            "Ask the writing partner something, with the context you choose. The answer is a suggestion; nothing is inserted unless you accept it.",
		DefaultChips:    []string{ChipBrief, ChipStyleGuide},
		Params:          []ParamSpec{{Name: "instruction", Desc: "What to ask or do.", Required: true, MaxLen: 4000}},
		MaxOutputTokens: 1200,
		Class:           "strong",
		Build: func(b BuildContext) (Prompt, error) {
			ins := b.Params["instruction"]
			if ins == "" {
				return Prompt{}, errors.New("an instruction is required")
			}
			p := Prompt{
				System: "Help the author with the task below, using the material when it is relevant.",
				Output: "Plain text. Answer directly and briefly unless the task asks for more.",
				Task:   ins,
				Blocks: b.Blocks(),
			}
			if len(b.Turns) > 0 { // a conversation: the instruction is the newest turn
				p.Turns = append(append([]Turn{}, b.Turns...), Turn{Role: RoleUser, Content: ins})
				p.Task = ""
			}
			return p, nil
		},
	})
}

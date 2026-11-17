package ai

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Policy actions. A project's AI policy allows or refuses each one, and every
// recipe names the one it belongs to.
const (
	ActionSelection    = "selection"    // rewrite, tighten, expand, describe, ...
	ActionAlternatives = "alternatives" // AI alternatives for a selection
	ActionChat         = "chat"
	ActionCritique     = "critique"
	ActionSummaries    = "summaries"
	ActionContinuity   = "continuity"
	ActionExtraction   = "extraction"
	ActionLab          = "lab" // marks and dimmed ranges, never prose
)

// Actions lists every policy action.
var Actions = []string{ActionSelection, ActionAlternatives, ActionChat, ActionCritique, ActionSummaries, ActionContinuity, ActionExtraction, ActionLab}

// ValidAction reports whether a is a policy action.
func ValidAction(a string) bool {
	for _, x := range Actions {
		if x == a {
			return true
		}
	}
	return false
}

// ResultKind says what a recipe's answer is.
type ResultKind string

// Result kinds.
const (
	ResultText  ResultKind = "text"  // prose or a list, shown as text
	ResultNotes ResultKind = "notes" // critique notes, never prose to insert
	ResultMarks ResultKind = "marks" // ranges and reasons (the Lab)
	ResultJSON  ResultKind = "json"  // structured data the recipe parses
)

// Scope says what a recipe needs as its target.
const (
	ScopeNone      = "none"      // nothing: a chat, a brainstorm
	ScopeSelection = "selection" // a selection in a document
	ScopeDocument  = "document"  // a whole document
	ScopeProject   = "project"   // the project as a whole
)

// Chip kinds. The context builder knows how to load each one.
const (
	ChipSelection        = "selection"
	ChipSentence         = "sentence"  // the sentence around the selection
	ChipParagraph        = "paragraph" // the paragraph around the selection
	ChipDocument         = "document"  // the target document's text
	ChipPreviousSummary  = "previous_summary"
	ChipPreviousDocument = "previous_document"
	ChipSynopsis         = "synopsis"
	ChipBrief            = "brief"
	ChipStyleGuide       = "style_guide"
	ChipEntities         = "entities"
	ChipResearch         = "research"
	ChipOutline          = "outline"
	ChipSummaries        = "summaries"   // the synopses of every earlier chapter or scene, in reading order
	ChipTimeline         = "timeline"    // the timeline's events and the findings of its deterministic check
	ChipInstruction      = "instruction" // free text from the person
)

// ChipKinds lists every chip kind.
var ChipKinds = []string{ChipSelection, ChipSentence, ChipParagraph, ChipDocument, ChipPreviousSummary, ChipPreviousDocument, ChipSynopsis,
	ChipBrief, ChipStyleGuide, ChipEntities, ChipResearch, ChipOutline, ChipSummaries, ChipTimeline, ChipInstruction}

// ValidChipKind reports whether k is a chip kind.
func ValidChipKind(k string) bool {
	for _, x := range ChipKinds {
		if x == k {
			return true
		}
	}
	return false
}

// Chip is one piece of loaded, authorized context. By the time a recipe sees it
// the server has checked that the caller may read it, that it is not marked
// never-send, and that it is within the size limits.
type Chip struct {
	Kind   string
	Label  string // what the preview shows: "Selected text", "Entity: Máire Nolan"
	Source string // an id (document, entity, note), empty for free text
	Text   string
	Tokens int
	Bytes  int
}

// ParamSpec declares one parameter of a recipe.
type ParamSpec struct {
	Name     string
	Desc     string
	Required bool
	Enum     []string // the allowed values, when it is a choice
	MaxLen   int      // bytes; default 2000
	Default  string
}

// BuildContext is what a recipe's Build receives.
type BuildContext struct {
	Params map[string]string // validated against Recipe.Params, defaults filled in
	Chips  []Chip            // in the order of the request
	Turns  []Turn            // earlier chat turns, for chat recipes
	// Language is the project's language tag, Title its title.
	Language string
	Title    string
}

// Chip returns the first chip of a kind.
func (b BuildContext) Chip(kind string) (Chip, bool) {
	for _, c := range b.Chips {
		if c.Kind == kind {
			return c, true
		}
	}
	return Chip{}, false
}

// ChipsOf returns every chip of a kind.
func (b BuildContext) ChipsOf(kind string) []Chip {
	var out []Chip
	for _, c := range b.Chips {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// Blocks turns chips into blocks, in order, optionally only some kinds. A
// recipe that wants every chip as material calls it with no kinds.
func (b BuildContext) Blocks(kinds ...string) []Block {
	var out []Block
	for _, c := range b.Chips {
		if len(kinds) > 0 {
			ok := false
			for _, k := range kinds {
				ok = ok || k == c.Kind
			}
			if !ok {
				continue
			}
		}
		out = append(out, Block{Kind: c.Kind, Label: c.Label, Source: c.Source, Text: c.Text})
	}
	return out
}

// ParseContext is what a recipe's Parse receives.
type ParseContext struct {
	Params map[string]string
	// Chips are the pieces of context the request carried, as the server loaded
	// them (before redaction). A parser checks what the model cites against them:
	// an answer may name only what it was given.
	Chips []Chip
	// Raw is the model's answer after redaction was undone.
	Raw string
}

// Parsed is a recipe's parsed answer. Text is what to show; Data holds the
// structured form (notes, marks, a JSON object) when there is one. Warnings are
// plain sentences about anything that did not parse, shown beside the raw text.
type Parsed struct {
	Text     string
	Data     json.RawMessage
	Warnings []string
}

// Recipe is one action the AI can perform. A recipe is code, versioned in the
// repository, not a row in a database: its prompt wording is reviewable.
type Recipe struct {
	// ID is a short stable name: lower case, digits, dots, dashes, underscores.
	ID      string
	Version int
	Name    string
	Desc    string
	// Action is the policy flag that allows it.
	Action string
	Result ResultKind
	Scope  string
	// DefaultChips are the chip kinds loaded when the request names none, in
	// order. "entities" here means the entities named in the text.
	DefaultChips []string
	Params       []ParamSpec
	// MaxOutputTokens is the ceiling for this recipe's answer; the server clamps
	// it further (model, policy, instance cap).
	MaxOutputTokens int
	// ExpectedOutputRatio is the usual output size as a share of the input, in
	// percent, for the cost shown before a run (default 60).
	ExpectedOutputRatio int
	Temperature         *float64
	// Class is the model class the recipe prefers: "cheap", "strong" or "local".
	// It chooses among the defaults a project or the instance set per class.
	Class string
	// Build makes the prompt from the validated chips and parameters.
	Build func(BuildContext) (Prompt, error)
	// Parse turns the model's text into the recipe's result. Nil means the text
	// is the result. A parse failure is not an error: return the raw text with
	// a warning and no Data, so nothing is lost and nothing is applied.
	Parse func(ParseContext) (Parsed, error)
}

var recipeIDRe = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,39}$`)

var (
	recipeMu sync.RWMutex
	recipes  = map[string]Recipe{}
)

// RegisterRecipe adds a recipe. It panics on a malformed or duplicate one,
// which is a programming error found the first time the process starts.
// Recipes are registered from init functions of the packages that define them.
func RegisterRecipe(r Recipe) {
	if !recipeIDRe.MatchString(r.ID) {
		panic(fmt.Sprintf("ai: recipe id %q is not lower case letters, digits, dots, dashes and underscores", r.ID))
	}
	if r.Build == nil || r.Version < 1 || strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.Desc) == "" {
		panic(fmt.Sprintf("ai: recipe %s needs a name, a description, a version and a Build", r.ID))
	}
	if !ValidAction(r.Action) {
		panic(fmt.Sprintf("ai: recipe %s has an unknown action %q", r.ID, r.Action))
	}
	switch r.Result {
	case ResultText, ResultNotes, ResultMarks, ResultJSON:
	default:
		panic(fmt.Sprintf("ai: recipe %s has an unknown result kind %q", r.ID, r.Result))
	}
	switch r.Scope {
	case ScopeNone, ScopeSelection, ScopeDocument, ScopeProject:
	default:
		panic(fmt.Sprintf("ai: recipe %s has an unknown scope %q", r.ID, r.Scope))
	}
	for _, k := range r.DefaultChips {
		if !ValidChipKind(k) {
			panic(fmt.Sprintf("ai: recipe %s names an unknown chip kind %q", r.ID, k))
		}
	}
	if r.MaxOutputTokens <= 0 {
		r.MaxOutputTokens = 1000
	}
	if r.ExpectedOutputRatio <= 0 {
		r.ExpectedOutputRatio = 60
	}
	if r.Class == "" {
		r.Class = "strong"
	}
	recipeMu.Lock()
	defer recipeMu.Unlock()
	if _, dup := recipes[r.ID]; dup {
		panic("ai: duplicate recipe " + r.ID)
	}
	recipes[r.ID] = r
}

// LookupRecipe returns a recipe by id.
func LookupRecipe(id string) (Recipe, bool) {
	recipeMu.RLock()
	defer recipeMu.RUnlock()
	r, ok := recipes[id]
	return r, ok
}

// Recipes lists every recipe, sorted by id.
func Recipes() []Recipe {
	recipeMu.RLock()
	defer recipeMu.RUnlock()
	out := make([]Recipe, 0, len(recipes))
	for _, r := range recipes {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CheckParams validates request parameters against the recipe's declarations
// and fills defaults. Unknown names are refused, so a parameter can never carry
// text the recipe did not ask for.
func (r Recipe) CheckParams(in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	known := map[string]ParamSpec{}
	for _, p := range r.Params {
		known[p.Name] = p
	}
	for k := range in {
		if _, ok := known[k]; !ok {
			return nil, fmt.Errorf("parameter %q: this recipe has no such parameter", k)
		}
	}
	for _, p := range r.Params {
		v, given := in[p.Name]
		v = strings.TrimSpace(v)
		if !given || v == "" {
			v = p.Default
		}
		if v == "" {
			if p.Required {
				return nil, fmt.Errorf("parameter %q is required", p.Name)
			}
			continue
		}
		max := p.MaxLen
		if max <= 0 {
			max = 2000
		}
		if len(v) > max {
			return nil, fmt.Errorf("parameter %q is limited to %d bytes", p.Name, max)
		}
		if len(p.Enum) > 0 {
			ok := false
			for _, e := range p.Enum {
				ok = ok || e == v
			}
			if !ok {
				return nil, fmt.Errorf("parameter %q must be one of %s", p.Name, strings.Join(p.Enum, ", "))
			}
		}
		out[p.Name] = v
	}
	return out, nil
}

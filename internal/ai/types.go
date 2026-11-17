// Package ai is the AI library: provider adapters over a guarded HTTP client,
// the fixed prompt framing that keeps manuscript text as data, token and cost
// estimates, and the recipe registry that actions plug into. It has no
// database and no access control; internal/domain/ai holds those and calls it.
//
// Nothing here ever sends a tool, function or tool_choice field: a completion
// request is a list of messages and a few numeric parameters, nothing else.
package ai

// Kinds of provider.
const (
	KindOpenAICompatible = "openai_compatible"
	KindOpenRouter       = "openrouter"
	KindOpenCode         = "opencode"
	KindOllama           = "ollama"
)

// Kinds lists every provider kind.
var Kinds = []string{KindOpenAICompatible, KindOpenRouter, KindOpenCode, KindOllama}

// ValidKind reports whether k is a provider kind.
func ValidKind(k string) bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}

// Message roles.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message is one chat message. The field order is the wire order.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Payload is a completion request in the provider-neutral form: messages and
// numeric parameters. A provider adapter turns it into its own body.
type Payload struct {
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature *float64  `json:"temperature,omitempty"`
	// JSON asks for a JSON object back where the provider supports it.
	JSON bool `json:"json,omitempty"`
}

// Usage is what a call used. Reported is true when the provider said so (the
// counts are then the provider's); otherwise they are the caller's estimate.
type Usage struct {
	InputTokens  int
	OutputTokens int
	Reported     bool
	// CostMicros is the cost the provider reported, in micro-units of the
	// instance currency, when it reports one (OpenRouter does). Nil is unknown.
	CostMicros *int64
}

// Result is a finished completion.
type Result struct {
	Text   string
	Usage  Usage
	Finish string
	// Truncated is true when the output hit the provider's length limit or the
	// client's response cap, or a stream ended before its end marker.
	Truncated bool
}

// Model is one entry of a provider's model list.
type Model struct {
	ID            string   `json:"id"`
	Label         string   `json:"label,omitempty"`
	ContextWindow int      `json:"context_window,omitempty"`
	MaxOutput     int      `json:"max_output,omitempty"`
	InputMicros   *int64   `json:"input_micros_per_mtok,omitempty" desc:"Micro-units per million input tokens when the provider lists a price."`
	OutputMicros  *int64   `json:"output_micros_per_mtok,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
}

// Header is one extra request header.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Routing holds the OpenRouter routing preferences. They are settings, not
// hidden defaults: data collection is denied unless the person changes it.
type Routing struct {
	// AllowDataCollection lets routed providers keep prompts. Default false.
	AllowDataCollection bool     `json:"allow_data_collection,omitempty"`
	ZDR                 bool     `json:"zdr,omitempty"`
	Order               []string `json:"order,omitempty"`
	NoFallbacks         bool     `json:"no_fallbacks,omitempty"`
}

// Delta is a piece of streamed text.
type Delta struct{ Text string }

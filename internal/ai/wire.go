package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// The wire bodies. They are structs, never maps, so the bytes are a function of
// the values alone: the same payload, model, kind and stream flag always give
// the same bytes, and Encode is the only place a body is made. The preview
// shows these bytes and the send uses them, so what a person approved is what
// the provider receives.
//
// There is no tools, functions, tool_choice or function_call field in any of
// them, by construction.

type oaiBody struct {
	Model          string         `json:"model"`
	Messages       []Message      `json:"messages"`
	MaxTokens      int            `json:"max_tokens,omitempty"`
	Temperature    *float64       `json:"temperature,omitempty"`
	Stream         bool           `json:"stream,omitempty"`
	StreamOptions  *oaiStreamOpts `json:"stream_options,omitempty"`
	ResponseFormat *oaiFormat     `json:"response_format,omitempty"`
	Provider       *orRouting     `json:"provider,omitempty"`
	Usage          *orUsage       `json:"usage,omitempty"`
}

type oaiStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaiFormat struct {
	Type string `json:"type"`
}

type orRouting struct {
	DataCollection string   `json:"data_collection,omitempty"`
	ZDR            bool     `json:"zdr,omitempty"`
	Order          []string `json:"order,omitempty"`
	AllowFallbacks *bool    `json:"allow_fallbacks,omitempty"`
}

type orUsage struct {
	Include bool `json:"include"`
}

type ollamaBody struct {
	Model     string        `json:"model"`
	Messages  []Message     `json:"messages"`
	Stream    bool          `json:"stream"`
	Format    string        `json:"format,omitempty"`
	Options   ollamaOptions `json:"options"`
	KeepAlive string        `json:"keep_alive,omitempty"`
}

type ollamaOptions struct {
	NumPredict  int      `json:"num_predict,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

// openCodeBody is the one-shot generate request. ASSUMED SHAPE: the OpenCode
// documentation names the route (POST /api/experimental/generate/text) but not
// its body, and the author could not check it against a running instance. It is
// kept in this one struct so a correction is one edit.
type openCodeBody struct {
	Model     string `json:"model"`
	System    string `json:"system,omitempty"`
	Prompt    string `json:"prompt"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

// MaxModelLen bounds a model id.
const MaxModelLen = 200

// ValidModel reports whether s can be sent as a model id: printable, no
// whitespace or control characters, at most MaxModelLen bytes.
func ValidModel(s string) bool {
	if s == "" || len(s) > MaxModelLen {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '"' || r == '\\' {
			return false
		}
	}
	return true
}

// Encode builds the exact request body for a model: the bytes that are sent.
func (c *Client) Encode(model string, p Payload, stream bool) ([]byte, error) {
	return Encode(c.ep, model, p, stream)
}

// Encode builds the exact request body for ep and model without allocating an HTTP client.
func Encode(ep Endpoint, model string, p Payload, stream bool) ([]byte, error) {
	if !ValidModel(model) {
		return nil, errors.New("ai: not a usable model name")
	}
	if len(p.Messages) == 0 {
		return nil, errors.New("ai: no messages")
	}
	for _, m := range p.Messages {
		if m.Role != RoleSystem && m.Role != RoleUser && m.Role != RoleAssistant {
			return nil, fmt.Errorf("ai: unknown role %q", m.Role)
		}
	}
	switch ep.Kind {
	case KindOpenAICompatible, KindOpenRouter:
		b := oaiBody{Model: model, Messages: p.Messages, MaxTokens: p.MaxTokens, Temperature: p.Temperature, Stream: stream}
		if stream {
			b.StreamOptions = &oaiStreamOpts{IncludeUsage: true}
		}
		if p.JSON {
			b.ResponseFormat = &oaiFormat{Type: "json_object"}
		}
		if ep.Kind == KindOpenRouter {
			r := orRouting{ZDR: ep.Routing.ZDR, Order: ep.Routing.Order}
			if !ep.Routing.AllowDataCollection {
				r.DataCollection = "deny"
			}
			if ep.Routing.NoFallbacks {
				f := false
				r.AllowFallbacks = &f
			}
			b.Provider = &r
			b.Usage = &orUsage{Include: true}
		}
		return marshal(b)
	case KindOllama:
		b := ollamaBody{Model: model, Messages: p.Messages, Stream: stream, Options: ollamaOptions{NumPredict: p.MaxTokens, Temperature: p.Temperature}, KeepAlive: ep.KeepAlive}
		if p.JSON {
			b.Format = "json"
		}
		return marshal(b)
	case KindOpenCode:
		var sys []string
		var rest []Message
		for _, m := range p.Messages {
			if m.Role == RoleSystem {
				sys = append(sys, m.Content)
			} else {
				rest = append(rest, m)
			}
		}
		prompt := ""
		if len(rest) == 1 {
			prompt = rest[0].Content
		} else {
			var sb strings.Builder
			for i, m := range rest {
				if i > 0 {
					sb.WriteString("\n\n")
				}
				if m.Role == RoleAssistant {
					sb.WriteString("Assistant:\n")
				} else {
					sb.WriteString("User:\n")
				}
				sb.WriteString(m.Content)
			}
			prompt = sb.String()
		}
		return marshal(openCodeBody{Model: model, System: strings.Join(sys, "\n\n"), Prompt: prompt, MaxTokens: p.MaxTokens})
	}
	return nil, errors.New("ai: unknown provider kind")
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // the text is sent as it is, not as HTML-safe escapes
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

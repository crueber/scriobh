package ai

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"

	"github.com/crueber/scriobh/internal/netguard"
)

// Embeddings. Two request shapes are spoken:
//
//   - Ollama: POST {base}/api/embed with {"model", "input": [...]}, answered by
//     {"embeddings": [[...], ...], "prompt_eval_count": n}. A server too old to
//     have /api/embed answers 404; the older /api/embeddings ({"model",
//     "prompt"} -> {"embedding": [...]}, one text at a time) is tried then.
//   - OpenAI-compatible (also OpenRouter): POST {base}/embeddings with {"model",
//     "input": [...], "encoding_format": "float"}, answered by {"data": [{"index",
//     "embedding"}, ...], "usage": {"prompt_tokens": n}}. The base address already
//     holds the version path (.../v1), as it does for chat.
//
// Nothing about the answer is trusted: the body is read through the client's
// response cap, the number of vectors must equal the number of texts, every
// vector must have the same length (at most MaxEmbedDim), every number must be
// finite after it is narrowed to float32, and a vector of zeros is refused (no
// direction, so no cosine). OpenCode has no embedding route and is refused.

// Embedding limits.
const (
	// MaxEmbedBatch is the most texts one request may carry.
	MaxEmbedBatch = 64
	// MaxEmbedInputBytes is the longest single text.
	MaxEmbedInputBytes = 64 << 10
	// MaxEmbedDim is the longest vector accepted. Models in use are 384 to 3072.
	MaxEmbedDim = 4096
	// MinEmbedDim is the shortest.
	MinEmbedDim = 8
)

// EmbedResult is what an embedding call returned.
type EmbedResult struct {
	Vectors [][]float32
	Dim     int
	// InputTokens is the provider's count when it gave one (Reported), else 0.
	InputTokens int
	Reported    bool
}

// SupportsEmbeddings reports whether a provider kind has an embedding route.
func SupportsEmbeddings(kind string) bool {
	return kind == KindOllama || kind == KindOpenAICompatible || kind == KindOpenRouter
}

// Embed returns one vector for each text, in order.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) (EmbedResult, error) {
	return c.EmbedChecked(ctx, model, inputs, nil)
}

// EmbedChecked is Embed with a check called before each request that carries text after the first: a server that
// has no /api/embed is sent its texts one request at a time, and a caller that must stop sending the moment a
// document is withdrawn needs to look again before each of them. An error from before stops the call, and is returned.
func (c *Client) EmbedChecked(ctx context.Context, model string, inputs []string, before func(context.Context) error) (EmbedResult, error) {
	if !SupportsEmbeddings(c.ep.Kind) {
		return EmbedResult{}, perr(ClassRejected, 0, "this kind of provider has no embedding route")
	}
	if len(inputs) == 0 || len(inputs) > MaxEmbedBatch {
		return EmbedResult{}, perr(ClassRejected, 0, "a request carries 1 to %d texts", MaxEmbedBatch)
	}
	if !ValidModel(model) {
		return EmbedResult{}, perr(ClassRejected, 0, "not a usable model name")
	}
	for _, s := range inputs {
		if len(s) > MaxEmbedInputBytes {
			return EmbedResult{}, perr(ClassRejected, 0, "a text is longer than %d bytes", MaxEmbedInputBytes)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, c.lim.Request)
	defer cancel()
	if c.ep.Kind == KindOllama {
		return c.embedOllama(ctx, model, inputs, before)
	}
	body, _ := json.Marshal(struct {
		Model  string   `json:"model"`
		Input  []string `json:"input"`
		Format string   `json:"encoding_format"`
	}{model, inputs, "float"})
	raw, err := c.post(ctx, "/embeddings", body)
	if err != nil {
		return EmbedResult{}, err
	}
	var o struct {
		Data []struct {
			Index     *int      `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage *struct {
			Prompt *int64 `json:"prompt_tokens"`
			Total  *int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return EmbedResult{}, perr(ClassBadResponse, 0, "not JSON")
	}
	if len(o.Data) != len(inputs) {
		return EmbedResult{}, perr(ClassBadResponse, 0, "%d vectors for %d texts", len(o.Data), len(inputs))
	}
	ordered := make([][]float64, len(inputs))
	for pos, d := range o.Data {
		i := pos
		if d.Index != nil {
			i = *d.Index
		}
		if i < 0 || i >= len(inputs) || ordered[i] != nil {
			return EmbedResult{}, perr(ClassBadResponse, 0, "bad vector index")
		}
		ordered[i] = d.Embedding
	}
	res, err := finishEmbed(ordered)
	if err != nil {
		return EmbedResult{}, err
	}
	if o.Usage != nil {
		n := o.Usage.Prompt
		if n == nil {
			n = o.Usage.Total
		}
		if n != nil {
			if t, ok := saneTokens(*n); ok {
				res.InputTokens, res.Reported = t, true
			}
		}
	}
	return res, nil
}

func (c *Client) embedOllama(ctx context.Context, model string, inputs []string, before func(context.Context) error) (EmbedResult, error) {
	req := struct {
		Model     string   `json:"model"`
		Input     []string `json:"input"`
		KeepAlive string   `json:"keep_alive,omitempty"`
	}{model, inputs, c.ep.KeepAlive}
	body, _ := json.Marshal(req)
	raw, err := c.post(ctx, "/api/embed", body)
	if err != nil {
		if pe, ok := AsProviderError(err); ok && pe.Class == ClassNotFound {
			return c.embedOllamaOld(ctx, model, inputs, before)
		}
		return EmbedResult{}, err
	}
	var o struct {
		Embeddings [][]float64 `json:"embeddings"`
		PromptEval *int64      `json:"prompt_eval_count"`
		Error      string      `json:"error"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return EmbedResult{}, perr(ClassBadResponse, 0, "not JSON")
	}
	if o.Error != "" {
		return EmbedResult{}, perr(ClassRejected, 0, "ollama error")
	}
	if len(o.Embeddings) != len(inputs) {
		return EmbedResult{}, perr(ClassBadResponse, 0, "%d vectors for %d texts", len(o.Embeddings), len(inputs))
	}
	res, err := finishEmbed(o.Embeddings)
	if err != nil {
		return EmbedResult{}, err
	}
	if o.PromptEval != nil {
		if t, ok := saneTokens(*o.PromptEval); ok {
			res.InputTokens, res.Reported = t, true
		}
	}
	return res, nil
}

// embedOllamaOld uses /api/embeddings, which takes one text per request.
func (c *Client) embedOllamaOld(ctx context.Context, model string, inputs []string, before func(context.Context) error) (EmbedResult, error) {
	all := make([][]float64, 0, len(inputs))
	for _, s := range inputs {
		if before != nil {
			if err := before(ctx); err != nil {
				return EmbedResult{}, err
			}
		}
		body, _ := json.Marshal(struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}{model, s})
		raw, err := c.post(ctx, "/api/embeddings", body)
		if err != nil {
			return EmbedResult{}, err
		}
		var o struct {
			Embedding []float64 `json:"embedding"`
			Error     string    `json:"error"`
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			return EmbedResult{}, perr(ClassBadResponse, 0, "not JSON")
		}
		if o.Error != "" {
			return EmbedResult{}, perr(ClassRejected, 0, "ollama error")
		}
		all = append(all, o.Embedding)
	}
	return finishEmbed(all)
}

// post sends a JSON body and returns the answer's body, through the response
// cap. A redirect is not followed (see NewClient) and is an error.
func (c *Client) post(ctx context.Context, path string, body []byte) ([]byte, error) {
	req, err := c.request(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, perr(ClassBadResponse, 0, "building the request failed")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, c.transportError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, c.statusError(resp)
	}
	raw, err := netguard.ReadLimited(resp.Body, c.lim.MaxResponse)
	if err != nil {
		if errors.Is(err, netguard.ErrTooLarge) {
			return nil, perr(ClassTooLarge, resp.StatusCode, "response over %d bytes", c.lim.MaxResponse)
		}
		return nil, c.transportError(ctx, err)
	}
	return raw, nil
}

// finishEmbed narrows the vectors to float32 and checks them: one length for
// all, within bounds, every number finite after narrowing, a direction.
func finishEmbed(in [][]float64) (EmbedResult, error) {
	if len(in) == 0 {
		return EmbedResult{}, perr(ClassBadResponse, 0, "no vectors")
	}
	dim := len(in[0])
	if dim < MinEmbedDim || dim > MaxEmbedDim {
		return EmbedResult{}, perr(ClassBadResponse, 0, "a vector of %d numbers", dim)
	}
	out := make([][]float32, len(in))
	for i, v := range in {
		if len(v) != dim {
			return EmbedResult{}, perr(ClassBadResponse, 0, "vectors of different lengths")
		}
		f := make([]float32, dim)
		var norm float64
		for j, x := range v {
			y := float32(x)
			if math.IsNaN(float64(y)) || math.IsInf(float64(y), 0) {
				return EmbedResult{}, perr(ClassBadResponse, 0, "a number that is not finite")
			}
			f[j] = y
			norm += float64(y) * float64(y)
		}
		if norm == 0 || math.IsInf(norm, 0) || math.IsNaN(norm) {
			return EmbedResult{}, perr(ClassBadResponse, 0, "a vector with no direction")
		}
		out[i] = f
	}
	return EmbedResult{Vectors: out, Dim: dim}, nil
}

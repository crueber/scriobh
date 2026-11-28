package ai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/crueber/scriobh/internal/netguard"
)

// Limits are the per-call bounds of a client.
type Limits struct {
	Dial        time.Duration // connect
	FirstByte   time.Duration // until the response headers
	Request     time.Duration // a whole non-streamed call
	Idle        time.Duration // longest silence in a stream
	StreamMax   time.Duration // longest stream
	MaxResponse int64         // bytes of a response body, streamed or not
}

// DefaultLimits are the bounds used when none are given.
var DefaultLimits = Limits{Dial: 10 * time.Second, FirstByte: 60 * time.Second, Request: 120 * time.Second,
	Idle: 60 * time.Second, StreamMax: 10 * time.Minute, MaxResponse: 4 << 20}

func (l Limits) fill() Limits {
	d := DefaultLimits
	if l.Dial > 0 {
		d.Dial = l.Dial
	}
	if l.FirstByte > 0 {
		d.FirstByte = l.FirstByte
	}
	if l.Request > 0 {
		d.Request = l.Request
	}
	if l.Idle > 0 {
		d.Idle = l.Idle
	}
	if l.StreamMax > 0 {
		d.StreamMax = l.StreamMax
	}
	if l.MaxResponse > 0 {
		d.MaxResponse = l.MaxResponse
	}
	return d
}

// Endpoint is everything needed to talk to one provider. APIKey is the opened
// secret, held for the life of the client and never written anywhere.
type Endpoint struct {
	Kind     string
	BaseURL  string
	APIKey   string
	Username string // OpenCode's basic-auth user name; default "opencode"
	Headers  []Header
	Routing  Routing
	// KeepAlive is Ollama's keep_alive value ("5m"), empty for its default.
	KeepAlive string
	// Mode is what the connection guard allows: PublicOnly, AllowLocal for a
	// provider an administrator marked local, RequireLocal for a project that
	// must stay on this machine or its network.
	Mode netguard.Mode
}

// ClientOptions are the seams around a client.
type ClientOptions struct {
	Log    *slog.Logger
	Limits Limits
	// Guard carries the test seams of the dialer (resolver, classifier).
	Guard netguard.Options
}

// Client talks to one provider. It is safe for concurrent use.
type Client struct {
	ep  Endpoint
	lim Limits
	hc  *http.Client
	tr  *http.Transport
	log *slog.Logger
}

// extraHeaderAllowed lists the headers a person may add to a provider. The
// credential is never one of them: it travels only in the header the adapter
// sets itself.
var extraHeaderAllowed = map[string]bool{
	"http-referer": true, "x-title": true, "openai-organization": true, "openai-project": true,
}

// AllowedExtraHeaders lists the header names a provider may carry.
func AllowedExtraHeaders() []string {
	return []string{"HTTP-Referer", "X-Title", "OpenAI-Organization", "OpenAI-Project"}
}

// CheckHeader validates one extra header.
func CheckHeader(h Header) error {
	if !extraHeaderAllowed[strings.ToLower(h.Name)] {
		return errors.New("that header is not allowed; allowed: " + strings.Join(AllowedExtraHeaders(), ", "))
	}
	if len(h.Value) > 200 {
		return errors.New("a header value is limited to 200 characters")
	}
	for _, r := range h.Value {
		if r < 0x20 || r > 0x7e {
			return errors.New("a header value must be plain printable ASCII")
		}
	}
	return nil
}

// ValidateEndpoint checks that ep has a known provider kind and a valid http(s)
// base URL without credentials.
func ValidateEndpoint(ep Endpoint) error {
	if !ValidKind(ep.Kind) {
		return errors.New("ai: unknown provider kind")
	}
	u, err := url.Parse(ep.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errors.New("ai: the base address must be an http or https URL without credentials")
	}
	return nil
}

// NewClient builds a client for an endpoint. It does not connect.
func NewClient(ep Endpoint, o ClientOptions) (*Client, error) {
	if err := ValidateEndpoint(ep); err != nil {
		return nil, err
	}
	ep.BaseURL = strings.TrimRight(ep.BaseURL, "/")
	lim := o.Limits.fill()
	g := o.Guard
	g.Mode, g.DialTimeout = ep.Mode, lim.Dial
	tr := &http.Transport{
		Proxy:                  nil, // never a proxy from the environment: it would be the address checked
		DialContext:            netguard.Dialer(g),
		TLSHandshakeTimeout:    lim.Dial,
		ResponseHeaderTimeout:  lim.FirstByte,
		MaxResponseHeaderBytes: 64 << 10,
		MaxIdleConns:           4,
		IdleConnTimeout:        30 * time.Second,
		DisableCompression:     false,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	c := &Client{ep: ep, lim: lim, tr: tr, log: log}
	c.hc = &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return c, nil
}

// Close drops idle connections.
func (c *Client) Close() { c.tr.CloseIdleConnections() }

// Kind is the provider kind.
func (c *Client) Kind() string { return c.ep.Kind }

func (c *Client) chatPath() string {
	switch c.ep.Kind {
	case KindOllama:
		return "/api/chat"
	case KindOpenCode:
		return "/api/experimental/generate/text"
	}
	return "/chat/completions"
}

func (c *Client) modelsPath() string {
	switch c.ep.Kind {
	case KindOllama:
		return "/api/tags"
	case KindOpenCode:
		return "/api/model"
	}
	return "/models"
}

var requestSeq atomic.Uint64

// request makes a request with the credential in its header and nowhere else.
func (c *Client) request(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.ep.BaseURL+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Scriobh")
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, h := range c.ep.Headers {
		if extraHeaderAllowed[strings.ToLower(h.Name)] && CheckHeader(h) == nil {
			req.Header.Set(h.Name, h.Value)
		}
	}
	if c.ep.Kind == KindOpenRouter && req.Header.Get("X-Title") == "" {
		req.Header.Set("X-Title", "Scriobh")
	}
	if c.ep.APIKey != "" {
		if c.ep.Kind == KindOpenCode {
			user := c.ep.Username
			if user == "" {
				user = "opencode"
			}
			req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+c.ep.APIKey)))
		} else {
			req.Header.Set("Authorization", "Bearer "+c.ep.APIKey)
		}
	}
	return req, nil
}

// transportError turns a failure to complete a round trip into a class. The
// detail is scrubbed of the key and the address's user information.
func (c *Client) transportError(ctx context.Context, err error) *ProviderError {
	detail := ScrubError(err, c.ep.APIKey)
	switch {
	case errors.Is(err, netguard.ErrBlocked):
		return &ProviderError{Class: ClassBlocked, Detail: detail}
	case errors.Is(ctx.Err(), context.Canceled):
		return &ProviderError{Class: ClassCancelled, Detail: detail}
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return &ProviderError{Class: ClassTimeout, Detail: detail}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &ProviderError{Class: ClassTimeout, Detail: detail}
	}
	if strings.Contains(strings.ToLower(detail), "address not allowed") {
		return &ProviderError{Class: ClassBlocked, Detail: detail}
	}
	return &ProviderError{Class: ClassUnreachable, Detail: detail}
}

// statusError builds the error for a non-2xx answer. At most a short, scrubbed
// piece of the provider's error message is kept, for the log.
func (c *Client) statusError(resp *http.Response) *ProviderError {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	msg := ""
	var e struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil {
		switch x := e.Error.(type) {
		case string:
			msg = x
		case map[string]any:
			if s, ok := x["message"].(string); ok {
				msg = s
			}
		}
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	class := classFromStatus(resp.StatusCode)
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		class = ClassBadResponse
		msg = "redirect refused"
	}
	pe := &ProviderError{Class: class, Status: resp.StatusCode, Detail: strings.TrimSpace(ScrubError(errors.New(msg), c.ep.APIKey))}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && n > 0 && n < 3600 {
			pe.RetryAfter = n
		}
	}
	return pe
}

// sane limits reported token counts: a provider that reports a negative or an
// absurd number must not corrupt the books.
func saneTokens(n int64) (int, bool) {
	if n < 0 || n > 50_000_000 {
		return 0, false
	}
	return int(n), true
}

// ---- non-streamed call ---------------------------------------------------------------------

// Chat sends an encoded body (from Encode with stream false) and returns the
// completion.
func (c *Client) Chat(ctx context.Context, body []byte) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.lim.Request)
	defer cancel()
	req, err := c.request(ctx, http.MethodPost, c.chatPath(), body)
	if err != nil {
		return Result{}, perr(ClassBadResponse, 0, "building the request failed")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return Result{}, c.transportError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{}, c.statusError(resp)
	}
	raw, err := netguard.ReadLimited(resp.Body, c.lim.MaxResponse)
	if err != nil {
		if errors.Is(err, netguard.ErrTooLarge) {
			return Result{}, perr(ClassTooLarge, resp.StatusCode, "response over %d bytes", c.lim.MaxResponse)
		}
		return Result{}, c.transportError(ctx, err)
	}
	return c.parseChat(raw, resp.Header.Get("Content-Type"))
}

type oaiResp struct {
	Choices []struct {
		Message struct {
			Content any `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaiUsage `json:"usage"`
}

type oaiUsage struct {
	Prompt     *int64   `json:"prompt_tokens"`
	Completion *int64   `json:"completion_tokens"`
	Cost       *float64 `json:"cost"`
}

func (u *oaiUsage) apply(r *Result) {
	if u == nil || u.Prompt == nil || u.Completion == nil {
		return
	}
	in, ok1 := saneTokens(*u.Prompt)
	out, ok2 := saneTokens(*u.Completion)
	if !ok1 || !ok2 {
		return
	}
	r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.Reported = in, out, true
	if u.Cost != nil && !math.IsNaN(*u.Cost) && *u.Cost >= 0 && *u.Cost < 1e6 {
		m := int64(math.Round(*u.Cost * 1e6))
		r.Usage.CostMicros = &m
	}
}

func contentString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any: // some servers return a list of parts
		var sb strings.Builder
		for _, p := range x {
			if m, ok := p.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					sb.WriteString(s)
				}
			}
		}
		return sb.String()
	}
	return ""
}

type ollamaResp struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Done       bool   `json:"done"`
	DoneReason string `json:"done_reason"`
	PromptEval *int64 `json:"prompt_eval_count"`
	Eval       *int64 `json:"eval_count"`
	Error      string `json:"error"`
}

func (o *ollamaResp) usage(r *Result) {
	if o.PromptEval == nil || o.Eval == nil {
		return
	}
	in, ok1 := saneTokens(*o.PromptEval)
	out, ok2 := saneTokens(*o.Eval)
	if ok1 && ok2 {
		r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.Reported = in, out, true
	}
}

func (c *Client) parseChat(raw []byte, contentType string) (Result, error) {
	switch c.ep.Kind {
	case KindOllama:
		var o ollamaResp
		if err := json.Unmarshal(raw, &o); err != nil {
			return Result{}, perr(ClassBadResponse, 0, "not JSON")
		}
		if o.Error != "" {
			return Result{}, perr(ClassRejected, 0, "ollama error: %s", clip(o.Error))
		}
		r := Result{Text: o.Message.Content, Finish: o.DoneReason, Truncated: o.DoneReason == "length"}
		o.usage(&r)
		return r, nil
	case KindOpenCode:
		// ASSUMED: a JSON object with the text under "text", "content" or
		// "output"; a bare JSON string; or plain text.
		t := bytes.TrimSpace(raw)
		if len(t) > 0 && t[0] == '{' {
			var m map[string]any
			if json.Unmarshal(t, &m) != nil {
				return Result{}, perr(ClassBadResponse, 0, "not JSON")
			}
			for _, k := range []string{"text", "content", "output"} {
				if s, ok := m[k].(string); ok {
					return Result{Text: s}, nil
				}
			}
			return Result{}, perr(ClassBadResponse, 0, "no text field")
		}
		if len(t) > 0 && t[0] == '"' {
			var s string
			if json.Unmarshal(t, &s) != nil {
				return Result{}, perr(ClassBadResponse, 0, "not a JSON string")
			}
			return Result{Text: s}, nil
		}
		if strings.HasPrefix(contentType, "text/") {
			return Result{Text: string(raw)}, nil
		}
		return Result{}, perr(ClassBadResponse, 0, "unrecognised answer")
	}
	var o oaiResp
	if err := json.Unmarshal(raw, &o); err != nil {
		return Result{}, perr(ClassBadResponse, 0, "not JSON")
	}
	if len(o.Choices) == 0 {
		return Result{}, perr(ClassBadResponse, 0, "no choices")
	}
	r := Result{Text: contentString(o.Choices[0].Message.Content), Finish: o.Choices[0].FinishReason, Truncated: o.Choices[0].FinishReason == "length"}
	o.Usage.apply(&r)
	return r, nil
}

func clip(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// ---- streamed call -------------------------------------------------------------------------

// maxLine bounds one line of a stream.
const maxLine = 1 << 20

// Stream sends an encoded body (from Encode with stream true) and calls onDelta
// with each piece of text as it arrives. If onDelta returns an error the stream
// is dropped, which cancels the request at the provider. On a failure after
// some text arrived, the partial Result is returned with the error.
func (c *Client) Stream(ctx context.Context, body []byte, onDelta func(string) error) (Result, error) {
	if c.ep.Kind == KindOpenCode { // one-shot only: the answer arrives whole
		res, err := c.Chat(ctx, body)
		if err == nil && res.Text != "" {
			if derr := onDelta(res.Text); derr != nil {
				return res, perr(ClassCancelled, 0, "sink refused")
			}
		}
		return res, err
	}
	cctx, cancel := context.WithTimeout(ctx, c.lim.StreamMax)
	defer cancel()
	var idleHit atomic.Bool
	idle := time.AfterFunc(c.lim.Idle+c.lim.FirstByte, func() { idleHit.Store(true); cancel() })
	defer idle.Stop()
	fail := func(err error) *ProviderError {
		if idleHit.Load() {
			return &ProviderError{Class: ClassTimeout, Detail: "stream idle"}
		}
		return c.transportError(cctx, err)
	}
	req, err := c.request(cctx, http.MethodPost, c.chatPath(), body)
	if err != nil {
		return Result{}, perr(ClassBadResponse, 0, "building the request failed")
	}
	req.Header.Set("Accept", "text/event-stream, application/x-ndjson, application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return Result{}, fail(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{}, c.statusError(resp)
	}
	lr := &netguard.LimitedReader{R: resp.Body, Max: c.lim.MaxResponse}
	br := bufio.NewReaderSize(lr, 64<<10)
	var res Result
	var text strings.Builder
	finished := false
	for {
		idle.Reset(c.lim.Idle)
		line, tooLong, err := readLine(br)
		if lr.Bytes() > c.lim.MaxResponse { // the cap was passed: the last line is cut off and is not read
			res.Text, res.Truncated = text.String(), true
			return res, perr(ClassTooLarge, 0, "stream over %d bytes", c.lim.MaxResponse)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			line = nil // a line cut off by an error is not read: it would only look malformed
		}
		if len(line) > 0 || tooLong {
			if tooLong {
				res.Text, res.Truncated = text.String(), true
				return res, perr(ClassBadResponse, 0, "a stream line over %d bytes", maxLine)
			}
			done, derr := c.streamLine(line, &res, &text, onDelta)
			if derr != nil {
				res.Text, res.Truncated = text.String(), true
				return res, derr
			}
			if done {
				finished = true
				break // the end marker comes after the usage chunk; nothing useful follows it
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			res.Text, res.Truncated = text.String(), true
			if errors.Is(err, netguard.ErrTooLarge) {
				return res, perr(ClassTooLarge, 0, "stream over %d bytes", c.lim.MaxResponse)
			}
			return res, fail(err)
		}
	}
	res.Text = text.String()
	if !finished {
		res.Truncated = true
	}
	return res, nil
}

// readLine reads one line without its newline, refusing a line over maxLine.
func readLine(br *bufio.Reader) (line []byte, tooLong bool, err error) {
	var out []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		out = append(out, chunk...)
		if len(out) > maxLine {
			return nil, true, nil
		}
		if err != nil {
			return out, false, err
		}
		if !isPrefix {
			return out, false, nil
		}
	}
}

// streamLine handles one line. done is true when the end marker was seen.
func (c *Client) streamLine(line []byte, res *Result, text *strings.Builder, onDelta func(string) error) (done bool, err error) {
	emit := func(s string) error {
		if s == "" {
			return nil
		}
		text.WriteString(s)
		if err := onDelta(s); err != nil {
			return perr(ClassCancelled, 0, "sink refused")
		}
		return nil
	}
	if c.ep.Kind == KindOllama {
		var o ollamaResp
		if json.Unmarshal(line, &o) != nil {
			return false, perr(ClassBadResponse, 0, "stream line is not JSON")
		}
		if o.Error != "" {
			return false, perr(ClassRejected, 0, "ollama error: %s", clip(o.Error))
		}
		if err := emit(o.Message.Content); err != nil {
			return false, err
		}
		if o.Done {
			res.Finish, res.Truncated = o.DoneReason, o.DoneReason == "length"
			o.usage(res)
			return true, nil
		}
		return false, nil
	}
	s := strings.TrimSpace(string(line))
	if s == "" || strings.HasPrefix(s, ":") { // keep-alive comments
		return false, nil
	}
	if !strings.HasPrefix(s, "data:") {
		return false, nil
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "data:"))
	if s == "[DONE]" {
		return true, nil
	}
	var o struct {
		Choices []struct {
			Delta struct {
				Content any `json:"content"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *oaiUsage       `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(s), &o) != nil {
		return false, perr(ClassBadResponse, 0, "stream chunk is not JSON")
	}
	if len(o.Error) > 0 && string(o.Error) != "null" {
		return false, perr(ClassUnavailable, 0, "error in stream: %s", clip(string(o.Error)))
	}
	if len(o.Choices) > 0 {
		if err := emit(contentString(o.Choices[0].Delta.Content)); err != nil {
			return false, err
		}
		if fr := o.Choices[0].FinishReason; fr != nil && *fr != "" {
			res.Finish, res.Truncated = *fr, *fr == "length"
		}
	}
	o.Usage.apply(res)
	return false, nil
}

// ---- models --------------------------------------------------------------------------------

// Models lists the models the provider offers. Not every provider can: a
// plain OpenAI-compatible server may have no list, which is a not_found error
// here and a manual entry for the person.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, c.lim.Request)
	defer cancel()
	req, err := c.request(ctx, http.MethodGet, c.modelsPath(), nil)
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
		return nil, perr(ClassTooLarge, resp.StatusCode, "model list too large")
	}
	return parseModels(c.ep.Kind, raw)
}

const maxModels = 2000

func perMTok(v any) *int64 {
	var f float64
	switch x := v.(type) {
	case string:
		p, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return nil
		}
		f = p
	case float64:
		f = x
	default:
		return nil
	}
	if math.IsNaN(f) || f < 0 || f > 1 { // a price per token above 1 unit is nonsense; negative means "varies"
		return nil
	}
	m := int64(math.Round(f * 1e12))
	return &m
}

func numInt(v any) int {
	if f, ok := v.(float64); ok && f > 0 && f < 1e9 {
		return int(f)
	}
	return 0
}

func parseModels(kind string, raw []byte) ([]Model, error) {
	var out []Model
	add := func(m Model) {
		if ValidModel(m.ID) && len(out) < maxModels {
			if len(m.Label) > 200 {
				m.Label = m.Label[:200]
			}
			out = append(out, m)
		}
	}
	switch kind {
	case KindOllama:
		var o struct {
			Models []struct {
				Name    string `json:"name"`
				Model   string `json:"model"`
				Details struct {
					Family string `json:"family"`
					Size   string `json:"parameter_size"`
				} `json:"details"`
			} `json:"models"`
		}
		if json.Unmarshal(raw, &o) != nil {
			return nil, perr(ClassBadResponse, 0, "not JSON")
		}
		for _, m := range o.Models {
			id := m.Name
			if id == "" {
				id = m.Model
			}
			add(Model{ID: id, Label: strings.TrimSpace(m.Details.Family + " " + m.Details.Size)})
		}
	case KindOpenCode:
		// ASSUMED shape: an array (or {"models"|"items"|"data": [...]}) of
		// strings or objects with id, or providerID and modelID.
		var any1 any
		if json.Unmarshal(raw, &any1) != nil {
			return nil, perr(ClassBadResponse, 0, "not JSON")
		}
		list, _ := any1.([]any)
		if m, ok := any1.(map[string]any); ok {
			for _, k := range []string{"models", "items", "data"} {
				if l, ok := m[k].([]any); ok {
					list = l
				}
			}
		}
		for _, e := range list {
			switch x := e.(type) {
			case string:
				add(Model{ID: x})
			case map[string]any:
				id, _ := x["id"].(string)
				p, _ := x["providerID"].(string)
				mm, _ := x["modelID"].(string)
				if p != "" && mm != "" {
					id = p + "/" + mm
				}
				name, _ := x["name"].(string)
				add(Model{ID: id, Label: name, ContextWindow: numInt(x["contextWindow"])})
			}
		}
	default:
		var o struct {
			Data []struct {
				ID            string  `json:"id"`
				Name          string  `json:"name"`
				ContextLength float64 `json:"context_length"`
				Pricing       struct {
					Prompt     any `json:"prompt"`
					Completion any `json:"completion"`
				} `json:"pricing"`
				Top struct {
					MaxCompletion float64 `json:"max_completion_tokens"`
				} `json:"top_provider"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &o) != nil {
			return nil, perr(ClassBadResponse, 0, "not JSON")
		}
		for _, m := range o.Data {
			add(Model{ID: m.ID, Label: m.Name, ContextWindow: numInt(m.ContextLength), MaxOutput: numInt(m.Top.MaxCompletion),
				InputMicros: perMTok(m.Pricing.Prompt), OutputMicros: perMTok(m.Pricing.Completion)})
		}
	}
	return out, nil
}

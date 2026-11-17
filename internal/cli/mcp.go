package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// `scriobh-cli mcp` serves the Model Context Protocol over stdio for a client
// that launches it (Claude Desktop, Cursor, OpenCode, ...). It is a proxy and
// nothing else: each line the client writes is POSTed to the server's /mcp
// endpoint with the API key, and each answer is written back as one line. The
// tools, the resources and every rule about who may call what live in the
// server; this file knows only how to frame messages, so the stdio server and
// the HTTP server cannot disagree, and the slim CLI links nothing new.
//
// Notifications are not forwarded. The server keeps no session, so none of them
// means anything to it, and the one that matters, notifications/cancelled, is
// carried out here: it closes that request's HTTP connection, which is how the
// server hears of a cancel.

const (
	mcpMaxLine     = 8 << 20 // the server's own cap is 2 MiB; this only bounds memory
	mcpMaxInflight = 16
	mcpDrain       = 30 * time.Second
)

type mcpOptions struct {
	groups    string
	readOnly  bool
	permanent bool
}

// mcpCmd runs `scriobh-cli mcp [--groups a,b] [--read-only]`.
func (a *app) mcpCmd(args []string) error {
	rest := stripLeadingGlobals(args)
	if len(rest) > 0 {
		rest = rest[1:] // "mcp"
	}
	var opt mcpOptions
	for i := 0; i < len(rest); i++ {
		n, val, hasVal := strings.Cut(strings.TrimLeft(rest[i], "-"), "=")
		switch n {
		case "groups":
			if !hasVal {
				if i+1 >= len(rest) {
					return usagef("--groups needs a list such as doc,search")
				}
				i++
				val = rest[i]
			}
			opt.groups = val
		case "read-only", "readonly":
			opt.readOnly = true
		case "permanent":
			opt.permanent = true
		case "server", "key", "config", "timeout", "retries":
			if !hasVal {
				i++ // prescan has read it
			}
		case "help", "h":
			fmt.Fprint(a.rt.Stderr, mcpHelp)
			return nil
		default:
			return usagef("unknown option %q for mcp; see: scriobh-cli mcp --help", rest[i])
		}
	}
	server, key, err := a.rt.Credentials(a.g)
	if err != nil {
		return err
	}
	if server == "" || key == "" {
		return &Fail{Code: "no_server", Message: "scriobh-cli mcp needs SCRIOBH_URL and SCRIOBH_API_KEY (or a saved login).", Exit: ExitUsage,
			Hint: "Set them in the MCP client's configuration for this server."}
	}
	base, err := NormalizeServer(server)
	if err != nil {
		return err
	}
	hc := a.rt.HTTP
	if hc == nil {
		hc = &http.Client{}
	}
	hc2 := *hc
	hc2.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	p := &mcpProxy{in: a.rt.Stdin, out: a.rt.Stdout, errw: a.rt.Stderr, hc: &hc2, key: key, host: base.Host, timeout: 120 * time.Second, inflight: map[string]context.CancelFunc{}, stopped: map[string]bool{}}
	if a.g.Timeout != "" {
		if d, err := time.ParseDuration(a.g.Timeout); err == nil && d > 0 {
			p.timeout = d
		}
	}
	u := *base
	u.Path = base.Path + "/mcp"
	q := url.Values{}
	if opt.groups != "" {
		q.Set("groups", opt.groups)
	}
	if opt.readOnly {
		q.Set("readonly", "1")
	}
	if opt.permanent {
		q.Set("permanent", "1")
	}
	u.RawQuery = q.Encode()
	p.endpoint = u.String()
	p.run(a.ctx)
	return nil
}

const mcpHelp = `scriobh-cli mcp: serve the Model Context Protocol over stdio.

  scriobh-cli mcp [--groups doc,search,...] [--read-only] [--permanent]

Reads SCRIOBH_URL and SCRIOBH_API_KEY like every other command (or the saved login).
The client starts this process; it forwards each message to the server's /mcp endpoint.
--groups limits the tools to those op groups (doc, project, compendium, entity, search, ...).
--read-only offers only the tools that cannot change anything.
--permanent asks for the tools that destroy data beyond the trash; the server offers them only if its administrator allowed it (SCRIOBH_MCP_PERMANENT_TOOLS=1) and the key can write.
`

type mcpProxy struct {
	in       io.Reader
	out      io.Writer
	errw     io.Writer
	hc       *http.Client
	key      string
	host     string
	endpoint string
	timeout  time.Duration

	wmu      sync.Mutex
	mu       sync.Mutex
	inflight map[string]context.CancelFunc
	stopped  map[string]bool // requests the client cancelled
	version  atomic.Value    // the legacy revision the client negotiated, once it has
}

// probe is the little the proxy reads of a message.
type mcpProbe struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name      string                     `json:"name"`
		URI       string                     `json:"uri"`
		RequestID json.RawMessage            `json:"requestId"`
		Meta      map[string]json.RawMessage `json:"_meta"`
	} `json:"params"`
}

func (p *mcpProxy) write(line []byte) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, _ = p.out.Write(append(bytes.TrimRight(line, "\r\n"), '\n'))
}

func (p *mcpProxy) writeError(id json.RawMessage, code int, msg string) {
	if len(id) == 0 {
		return // a notification gets no answer, not even a failed one
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "error": map[string]any{"code": code, "message": msg}})
	p.write(b)
}

func (p *mcpProxy) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	sem := make(chan struct{}, mcpMaxInflight)
	rd := bufio.NewReaderSize(p.in, 64<<10)
	for {
		line, tooLong, err := readLine(rd, mcpMaxLine)
		if len(bytes.TrimSpace(line)) > 0 || tooLong {
			if tooLong {
				p.writeError(json.RawMessage("null"), -32600, "That message is too large.")
			} else {
				sem <- struct{}{}
				wg.Add(1)
				go func(line []byte) {
					defer func() { <-sem; wg.Done() }()
					p.handle(ctx, line)
				}(append([]byte(nil), line...))
			}
		}
		if err != nil {
			break
		}
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(mcpDrain):
		cancel()
		<-done
	}
}

// readLine reads one line of at most max bytes. A longer line is read to its end
// and dropped, so one oversized message does not end the session.
func readLine(rd *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	for {
		chunk, isPrefix, e := rd.ReadLine()
		if e != nil {
			return line, tooLong, e
		}
		if !tooLong {
			if len(line)+len(chunk) > max {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if !isPrefix {
			return line, tooLong, nil
		}
	}
}

func headerSafe(v string) bool {
	if v == "" || strings.HasPrefix(v, "=?base64?") && strings.HasSuffix(v, "?=") {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return false
		}
	}
	return v == strings.TrimSpace(v)
}

func encodeHeaderValue(v string) string {
	if headerSafe(v) {
		return v
	}
	return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(v)) + "?="
}

func (p *mcpProxy) handle(ctx context.Context, line []byte) {
	trim := bytes.TrimSpace(line)
	var pr mcpProbe
	batch := len(trim) > 0 && trim[0] == '['
	if !batch {
		_ = json.Unmarshal(trim, &pr) // a message that does not parse is still forwarded: the server says why
	}
	if !batch && pr.Method != "" && len(pr.ID) == 0 {
		// The server keeps no session, so a notification means nothing to it, and the
		// one that matters (cancel) is the closing of a connection. They are not forwarded:
		// each would spend a rate-limit token of the key for no effect.
		if pr.Method == "notifications/cancelled" {
			p.cancel(pr.Params.RequestID)
		}
		return
	}
	idKey := string(bytes.TrimSpace(pr.ID))
	rctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	if idKey != "" {
		p.mu.Lock()
		p.inflight[idKey] = cancel
		p.mu.Unlock()
		defer func() { p.mu.Lock(); delete(p.inflight, idKey); p.mu.Unlock() }()
	}
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, p.endpoint, bytes.NewReader(trim))
	if err != nil {
		p.writeError(pr.ID, -32603, "Could not build the request.")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("User-Agent", "scriobh-cli/"+Version)
	if !batch {
		var ver string
		if raw, ok := pr.Params.Meta["io.modelcontextprotocol/protocolVersion"]; ok {
			_ = json.Unmarshal(raw, &ver)
			req.Header.Set("MCP-Protocol-Version", ver)
			req.Header.Set("Mcp-Method", pr.Method)
			switch pr.Method {
			case "tools/call", "prompts/get":
				req.Header.Set("Mcp-Name", encodeHeaderValue(pr.Params.Name))
			case "resources/read":
				req.Header.Set("Mcp-Name", encodeHeaderValue(pr.Params.URI))
			}
		} else if v, _ := p.version.Load().(string); v != "" {
			req.Header.Set("MCP-Protocol-Version", v)
		}
	} else if v, _ := p.version.Load().(string); v != "" {
		req.Header.Set("MCP-Protocol-Version", v)
	}
	res, err := p.hc.Do(req)
	if err != nil {
		if idKey != "" && p.wasCancelled(idKey) {
			return // cancelled by the client: no answer
		}
		p.writeError(pr.ID, -32603, "Could not reach "+p.host+": "+scrub(err, p.key))
		return
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		p.writeError(pr.ID, -32603, fmt.Sprintf("The server redirected the request (%d). Redirects are not followed, so the key stays where you sent it; set SCRIOBH_URL to the final address.", res.StatusCode))
		return
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		p.writeError(pr.ID, -32603, "The connection to "+p.host+" broke.")
		return
	}
	if res.StatusCode == http.StatusAccepted || (len(bytes.TrimSpace(body)) == 0 && res.StatusCode < 300) {
		return
	}
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		for _, data := range sseData(body) {
			p.write(data)
		}
		return
	}
	body = bytes.TrimSpace(body)
	var obj struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if len(body) > 0 && body[0] == '{' && json.Unmarshal(body, &obj) == nil && obj.JSONRPC == "2.0" && (len(obj.Result) > 0 || len(obj.Error) > 0) {
		if pr.Method == "initialize" && len(obj.Result) > 0 {
			var r struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if json.Unmarshal(obj.Result, &r) == nil && r.ProtocolVersion != "" {
				p.version.Store(r.ProtocolVersion)
			}
		}
		if (string(obj.ID) == "null" || len(obj.ID) == 0) && len(pr.ID) > 0 {
			// A refusal that could not read the id (a bad key, a rate limit) is
			// answered to the request that was sent.
			obj.ID = pr.ID
			body, _ = json.Marshal(obj)
		}
		var buf bytes.Buffer
		if json.Compact(&buf, body) == nil {
			p.write(buf.Bytes())
		}
		return
	}
	if len(body) > 0 && (body[0] == '[' || body[0] == '{') {
		var buf bytes.Buffer
		if json.Compact(&buf, body) == nil && batch {
			p.write(buf.Bytes())
			return
		}
	}
	p.writeError(pr.ID, -32603, fmt.Sprintf("The server answered %d, which is not an MCP answer. Is %s a Scríobh server with MCP on?", res.StatusCode, p.host))
}

func (p *mcpProxy) cancel(raw json.RawMessage) {
	k := string(bytes.TrimSpace(raw))
	p.mu.Lock()
	c := p.inflight[k]
	if c != nil {
		p.stopped[k] = true
	}
	p.mu.Unlock()
	if c != nil {
		c()
	}
}

func (p *mcpProxy) wasCancelled(k string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	ok := p.stopped[k]
	delete(p.stopped, k)
	return ok
}

// sseData returns the data payloads of a complete event stream, one per event.
func sseData(b []byte) [][]byte {
	var out [][]byte
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			out = append(out, []byte(strings.Join(cur, "\n")))
			cur = nil
		}
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimRight(l, "\r")
		switch {
		case l == "":
			flush()
		case strings.HasPrefix(l, "data:"):
			cur = append(cur, strings.TrimPrefix(strings.TrimPrefix(l, "data:"), " "))
		}
	}
	flush()
	return out
}

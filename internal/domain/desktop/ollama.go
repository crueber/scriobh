package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/crueber/scriobh/internal/netguard"
)

// OllamaAddress is the one address the look for a local model server asks: Ollama's default, on this computer.
const OllamaAddress = "127.0.0.1:11434"

// Limits of the look: what is read, how long it may take, and how much of the answer is kept.
const (
	detectTimeout  = 3 * time.Second
	detectMaxBytes = 1 << 20
	detectMaxItems = 200
	detectNameMax  = 200
)

// Model is one model the server on this computer has.
type Model struct {
	Name string `json:"name"`
	// Size is the model's size on disk in bytes, when the server says.
	Size       int64  `json:"size,omitempty"`
	Parameters string `json:"parameters,omitempty" desc:"The parameter count as the server words it (7B)."`
}

// DetectOut is desktop.localmodel.detect.
type DetectOut struct {
	Found   bool    `json:"found" desc:"Whether a model server answered on this computer."`
	Address string  `json:"address"`
	Models  []Model `json:"models,omitempty"`
}

// DetectOllama asks the model server on this computer for its models: one GET of /api/tags.
func DetectOllama(ctx context.Context) ([]Model, error) { return detectAt(ctx, OllamaAddress) }

// detectAt is DetectOllama for an address a test listens on. The connection goes to addr whatever the request names,
// never through a proxy and never following a redirect; what comes back is another program's and is treated so: at
// most detectMaxBytes are read, at most detectMaxItems kept, and each name is cut and stripped of control characters.
func detectAt(ctx context.Context, addr string) ([]Model, error) {
	if !netguard.IsLoopbackAddress(addr) {
		return nil, errors.New("not this computer")
	}
	d := &net.Dialer{Timeout: detectTimeout}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: netguard.Observed(func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp", addr)
		}),
		DisableKeepAlives: true, ResponseHeaderTimeout: detectTimeout, MaxResponseHeaderBytes: 16 << 10,
	}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr, Timeout: detectTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("not a model server")
	}
	raw, err := netguard.ReadLimited(resp.Body, detectMaxBytes)
	if err != nil {
		return nil, err
	}
	var body struct {
		Models []struct {
			Name    string `json:"name"`
			Size    int64  `json:"size"`
			Details struct {
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Models == nil {
		return nil, errors.New("not a model server")
	}
	out := make([]Model, 0, min(len(body.Models), detectMaxItems))
	for _, m := range body.Models {
		if len(out) == detectMaxItems {
			break
		}
		name := cleanText(m.Name, detectNameMax)
		if name == "" {
			continue
		}
		size := m.Size
		if size < 0 {
			size = 0
		}
		out = append(out, Model{Name: name, Size: size, Parameters: cleanText(m.Details.ParameterSize, 16)})
	}
	return out, nil
}

// cleanText keeps the printable part of a string another program sent, up to max characters.
func cleanText(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == max {
			break
		}
		if unicode.IsControl(r) || r == unicode.ReplacementChar || !unicode.IsPrint(r) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

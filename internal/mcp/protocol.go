package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Protocol revisions this server speaks, newest first. Two eras share one
// endpoint, chosen per request by what the request carries:
//
//   - Modern (2026-07-28): no handshake. Every request carries its protocol
//     version and capabilities in params._meta, and on HTTP in the
//     MCP-Protocol-Version, Mcp-Method and Mcp-Name headers, which must agree
//     with the body. Results carry resultType.
//   - Legacy (2025-11-25 and earlier): the initialize handshake. The server
//     keeps no session over it: it answers initialize, mints no session id, and
//     treats every later request on its own (the MCP-Protocol-Version header,
//     when sent, says which legacy revision the client is on).
const (
	VersionModern = "2026-07-28"
	// VersionLegacyLatest is what an initialize that asks for an unknown revision is answered with.
	VersionLegacyLatest = "2025-11-25"
	// VersionDefaultLegacy is assumed for a legacy HTTP request that names no version.
	VersionDefaultLegacy = "2025-03-26"
)

// SupportedVersions lists every revision, newest first.
var SupportedVersions = []string{VersionModern, "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

func isLegacyVersion(v string) bool {
	for _, s := range SupportedVersions[1:] {
		if s == v {
			return true
		}
	}
	return false
}

// Meta keys of the modern era.
const (
	metaVersion = "io.modelcontextprotocol/protocolVersion"
	metaCaps    = "io.modelcontextprotocol/clientCapabilities"
	metaServer  = "io.modelcontextprotocol/serverInfo"
)

// JSON-RPC and MCP error codes.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603

	CodeHeaderMismatch      = -32020
	CodeUnsupportedProtocol = -32022
	// CodeResourceNotFoundLegacy is what the legacy era answers for a resource that is not there.
	CodeResourceNotFoundLegacy = -32002
)

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// reply is one JSON-RPC response before it is encoded.
type reply struct {
	id     json.RawMessage // nil encodes as null
	result any
	err    *rpcError
	// status is the HTTP status the transport uses when this is the only message.
	status int
}

func (r reply) marshal() []byte {
	id := r.id
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	var buf bytes.Buffer
	buf.WriteString(`{"jsonrpc":"2.0","id":`)
	buf.Write(id)
	if r.err != nil {
		b, _ := json.Marshal(r.err)
		buf.WriteString(`,"error":`)
		buf.Write(b)
	} else {
		b, err := json.Marshal(r.result)
		if err != nil {
			b = []byte(`null`)
		}
		buf.WriteString(`,"result":`)
		buf.Write(b)
	}
	buf.WriteString(`}`)
	return buf.Bytes()
}

func errReply(id json.RawMessage, status, code int, msg string, data any) reply {
	return reply{id: id, status: status, err: &rpcError{Code: code, Message: msg, Data: data}}
}

// message is the shape every incoming JSON-RPC object is decoded to first.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

// validID reports whether raw is a JSON string or an integer: the ids MCP allows
// (null is not one).
func validID(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 256 {
		return false
	}
	switch raw[0] {
	case '"':
		var s string
		return json.Unmarshal(raw, &s) == nil
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		var n json.Number
		if json.Unmarshal(raw, &n) != nil {
			return false
		}
		return !strings.ContainsAny(string(n), ".eE")
	}
	return false
}

// decodeHeaderValue undoes the =?base64?...?= sentinel encoding of Mcp-Name.
func decodeHeaderValue(v string) (string, bool) {
	const pre, suf = "=?base64?", "?="
	if strings.HasPrefix(v, pre) && strings.HasSuffix(v, suf) && len(v) >= len(pre)+len(suf) {
		b, err := base64.StdEncoding.DecodeString(v[len(pre) : len(v)-len(suf)])
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return v, true
}

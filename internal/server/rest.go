package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/idempotency"
	"github.com/crueber/scriobh/internal/ops"
)

// bindOps generates one REST route per registered op, plus the op schema.
// Authorization that does not need the input runs before the body is read;
// project membership runs after the path and query are bound but still
// before the body is read.
func (s *Server) bindOps() {
	for _, e := range s.Reg.Entries() {
		s.mux.HandleFunc(e.Op.Method+" "+e.Op.Path, s.restHandler(e))
		if e.Op.MaxBody > 0 {
			s.bigBody = append(s.bigBody, e)
		}
	}
	s.schema = s.Reg.Schema() // every op is registered by now
	s.schemaJSON = encodeJSON(s.schema)
	s.mux.HandleFunc("GET /api/v1/_ops", func(w http.ResponseWriter, r *http.Request) {
		if !authFrom(r.Context()).P.Authenticated() {
			writeJSONError(w, ops.Unauthenticated())
			return
		}
		etag := `"` + s.schema.Hash + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.schemaJSON)
	})
	s.mux.HandleFunc("GET /api/v1/_whoami", s.whoami)
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		// A known path with the wrong method is 405, not 404.
		var allow []string
		for _, e := range s.Reg.Entries() {
			if pathMatches(e.Op.Path, r.URL.Path) {
				allow = append(allow, e.Op.Method)
			}
		}
		if len(allow) > 0 {
			w.Header().Set("Allow", strings.Join(allow, ", "))
			writeJSONError(w, &ops.Error{Code: ops.CodeMethod, Message: "That method is not allowed here."})
			return
		}
		writeJSONError(w, ops.NotFound("endpoint"))
	})
}

func (s *Server) restHandler(e *ops.Entry) http.HandlerFunc {
	fields := ops.InputFields(e.In)
	hasBody := false
	for _, f := range fields {
		if f.Location == "body" {
			hasBody = true
		}
	}
	mutating := e.Op.Method != http.MethodGet && e.Op.Method != http.MethodHead
	// A file response is not kept for replay: an export is rebuilt on request.
	_, isFile := reflect.Zero(e.Out).Interface().(ops.Download)
	return func(w http.ResponseWriter, r *http.Request) {
		p := authFrom(r.Context()).P
		if err := s.Reg.PreAuthorize(p, e); err != nil {
			writeJSONError(w, ops.AsError(err))
			return
		}
		dry, derr := dryRunRequested(r)
		if derr != nil {
			writeDryRunValueError(w, derr)
			return
		}
		in := e.NewIn()
		if err := bindParams(in, fields, r); err != nil {
			writeJSONError(w, ops.AsError(err))
			return
		}
		if e.NeedsInputAuth() {
			if err := s.Reg.AuthorizeInput(ops.Precheck(r.Context()), p, e, in); err != nil {
				writeJSONError(w, ops.AsError(err))
				return
			}
		}
		if e.Op.RawBody {
			// The body is a file: it is handed to the op as a stream, after the
			// caller has been authorized and without being read here. A preview
			// is refused (an upload writes a file) before a byte is read.
			if dry {
				s.serveDryRun(w, p, e, r, in)
				return
			}
			if r.ContentLength > e.Op.MaxBody {
				writeJSONError(w, ops.TooLarge())
				return
			}
			ctx := ops.WithRawBody(r.Context(), ops.RawBody{R: r.Body, ContentType: r.Header.Get("Content-Type"), Length: r.ContentLength})
			out, err := s.Reg.Call(ctx, p, e.Op.Name, in)
			if err != nil {
				writeJSONError(w, ops.AsError(err))
				return
			}
			writeJSONBody(w, e.Op.Status, encodeJSON(out), false)
			return
		}
		var raw []byte
		if hasBody && mutating {
			// A big body (an import) is held twice while it is handled, as received and as decoded: the bytes
			// of all of them in flight at once share one budget, and one that does not fit is told to wait.
			if e.Op.MaxBody >= bigBody {
				n := 2 * e.Op.MaxBody
				if r.ContentLength > 0 && r.ContentLength < e.Op.MaxBody {
					n = 2 * r.ContentLength
				}
				if !takeBodyBudget(n) {
					writeJSONError(w, &ops.Error{Code: ops.CodeRateLimited, Message: "The server is busy reading other large uploads. Try again in a moment.", RetryAfterSeconds: 5})
					return
				}
				defer giveBodyBudget(n)
			}
			var err error
			if raw, err = readBody(r.Body, r.ContentLength, e.Op.MaxBody); err != nil {
				var mbe *http.MaxBytesError
				if errors.As(err, &mbe) {
					writeJSONError(w, ops.TooLarge())
					return
				}
				writeJSONError(w, ops.Invalid("", "Could not read the request body."))
				return
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(in); err != nil && err != io.EOF {
				writeJSONError(w, ops.Invalid("", "Malformed JSON: "+err.Error()))
				return
			}
			if _, err := dec.Token(); err != io.EOF {
				writeJSONError(w, ops.Invalid("", "Unexpected data after the JSON body."))
				return
			}
			if e.Op.MaxBody >= bigBody && (r.Header.Get("Idempotency-Key") == "" || e.Op.Ephemeral) {
				raw = nil // the file is in the input now; the bytes it came in as are not needed again
			}
		}
		if dry {
			s.serveDryRun(w, p, e, r, in)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" || !mutating || isFile || (e.Op.SecretInput && !e.Op.SecretOutput) || e.Op.Ephemeral {
			out, err := s.Reg.Call(r.Context(), p, e.Op.Name, in)
			if err != nil {
				writeJSONError(w, ops.AsError(err))
				return
			}
			if dl, ok := out.(ops.Download); ok {
				if img, ok := out.(ops.Inline); ok {
					writeInline(w, r, dl, img.Inline())
					return
				}
				writeDownload(w, dl)
				return
			}
			writeJSONBody(w, e.Op.Status, encodeJSON(out), false)
			return
		}
		if !idempotency.ValidKey(key) {
			writeJSONError(w, ops.Invalid("Idempotency-Key", "An Idempotency-Key is 1 to 200 printable ASCII characters."))
			return
		}
		// An op that takes a typed secret and also answers with one (share.create) still treats a repeated key as a
		// conflict, so a retry cannot make a second link, but its fingerprint leaves the body out: a stored hash of a body
		// that holds a passphrase would be a guessing oracle.
		fpBody := raw
		if k, ok := in.(interface{ KeepBodyOutOfFingerprint() bool }); e.Op.SecretInput || (ok && k.KeepBodyOutOfFingerprint()) {
			fpBody = nil // a hash of a private note's words would be a guessing oracle, as a hash of a passphrase would
		}
		fp := idempotency.Fingerprint(e.Op.Name, r.Method, r.URL.Path, r.URL.Query(), fpBody)
		resp, replay, err := s.Idem.Do(r.Context(), s.idemPrincipal(r), key, e.Op.Name, fp, func() (idempotency.Result, error) {
			out, err := s.Reg.Call(r.Context(), p, e.Op.Name, in)
			if err != nil {
				return idempotency.Result{}, err
			}
			// What only one person may see (a private comment) is not written down in the answer: the record holds a reference, and a
			// repeated key answers by reading the thread again as whoever repeats it. The words are stored in one place, `comments`.
			res := idempotency.Result{
				Response:  idempotency.Response{Status: e.Op.Status, Body: encodeJSON(out)},
				Store:     !e.Op.SecretOutput,
				Tombstone: e.Op.SecretOutput,
			}
			if rf, ok := out.(interface{ ReplayRef() string }); ok && rf.ReplayRef() != "" && !e.Op.SecretOutput {
				res.Kept = encodeJSON(map[string]string{replayMarker: rf.ReplayRef()})
			}
			return res, nil
		})
		if err != nil {
			writeJSONError(w, ops.AsError(err))
			return
		}
		if replay {
			if id := replayRefIn(resp.Body); id != "" {
				// the answer is the thread as it is now, as the person repeating the request sees it (it may have been changed or deleted since)
				pid := ""
				if pr, ok := in.(interface{ ProjectRef() string }); ok {
					pid = pr.ProjectRef()
				}
				out, err := s.Reg.Call(r.Context(), p, "comment.get", review.GetIn{ProjectID: pid, CommentID: id})
				if err != nil {
					if ops.AsError(err).Code == ops.CodeNotFound {
						writeJSONError(w, ops.Conflict("This request already ran and wrote the comment, which has been deleted since. Do not repeat it."))
						return
					}
					writeJSONError(w, ops.AsError(err))
					return
				}
				if t, ok := out.(review.ThreadItem); ok && t.Item.Deleted {
					writeJSONError(w, ops.Conflict("This request already ran and wrote the comment, which has been deleted since. Do not repeat it."))
					return
				}
				writeJSONBody(w, resp.Status, encodeJSON(out), true)
				return
			}
		}
		writeJSONBody(w, resp.Status, resp.Body, replay)
	}
}

// replayMarker names the one field of a stored record that is a reference rather than an answer.
const replayMarker = "replay_comment"

// replayRefIn is the comment id a stored record refers to, or "".
func replayRefIn(body []byte) string {
	if !bytes.Contains(body, []byte(replayMarker)) {
		return ""
	}
	var m map[string]string
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	return m[replayMarker]
}

// idemPrincipal scopes an idempotency key to whoever sent it: one API key, or
// one person's browser session.
func (s *Server) idemPrincipal(r *http.Request) string {
	a := authFrom(r.Context())
	if a.KeyID != "" {
		return "key:" + a.KeyID
	}
	return "user:" + a.P.UserID
}

func encodeJSON(v any) []byte {
	var b bytes.Buffer
	_ = json.NewEncoder(&b).Encode(v)
	return b.Bytes()
}

func writeJSONBody(w http.ResponseWriter, status int, body []byte, replay bool) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	if replay {
		h.Set("Idempotent-Replay", "true")
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// dryRunRequested reads the preview request: "Prefer: dry-run" (RFC 7240:
// comma separated name[=value] tokens, names case-insensitive; dry-run with any
// value but false, 0, no or off asks for it) or "X-Scriobh-Dry-Run" (1, true,
// yes or on; 0, false, no, off or empty mean none). Either one asking is enough.
// A value this cannot read is an error and never a silent real run: an agent
// that asked for a preview must not get a commit.
func dryRunRequested(r *http.Request) (bool, *dryRunValueError) {
	want := false
	for _, v := range r.Header.Values("X-Scriobh-Dry-Run") {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			want = true
		case "", "0", "false", "no", "off":
		default:
			return false, &dryRunValueError{"X-Scriobh-Dry-Run must be 1, true, yes or on (or 0, false, no, off for none), not " + strconv.Quote(v) + "."}
		}
	}
	for _, v := range r.Header.Values("Prefer") {
		for _, tok := range strings.Split(v, ",") {
			tok, _, _ = strings.Cut(tok, ";") // parameters of the preference
			name, val, _ := strings.Cut(tok, "=")
			if !strings.EqualFold(strings.TrimSpace(name), "dry-run") {
				continue
			}
			switch strings.ToLower(strings.Trim(strings.TrimSpace(val), `"`)) {
			case "false", "0", "no", "off":
			default:
				want = true
			}
		}
	}
	return want, nil
}

// dryRunValueError is a dry-run header that cannot be understood.
type dryRunValueError struct{ msg string }

// writeDryRunValueError answers 400 invalid.
func writeDryRunValueError(w http.ResponseWriter, e *dryRunValueError) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": &ops.Error{Code: ops.CodeInvalid, Message: e.msg, Field: "X-Scriobh-Dry-Run"}})
}

// serveDryRun runs the op as a preview and answers with what it would have
// returned plus dry_run, would_change and changes. A file response is
// described, not sent.
func (s *Server) serveDryRun(w http.ResponseWriter, p ops.Principal, e *ops.Entry, r *http.Request, in any) {
	w.Header().Set("Preference-Applied", "dry-run") // on errors too: a preview was what ran
	res, err := s.Reg.CallDry(r.Context(), p, e.Op.Name, in)
	if err != nil {
		writeJSONError(w, ops.AsError(err))
		return
	}
	obj := map[string]any{}
	if dl, ok := res.Out.(ops.Download); ok {
		name, ctype, body := dl.Download()
		obj["file"] = map[string]any{"filename": name, "content_type": ctype, "bytes": len(body)}
	} else if raw, merr := json.Marshal(res.Out); merr == nil {
		_ = json.Unmarshal(raw, &obj)
	}
	obj["dry_run"] = true
	obj["would_change"] = res.Changes > 0
	obj["changes"] = res.Changes
	w.Header().Set("Preference-Applied", "dry-run")
	writeJSONBody(w, e.Op.Status, encodeJSON(obj), false)
}

// writeDownload sends an op's file as an attachment. The type and the name come
// from the op, never from the request; nosniff keeps a browser from
// reinterpreting the bytes, and the file is never cached.
func writeDownload(w http.ResponseWriter, dl ops.Download) {
	name, contentType, body := dl.Download()
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", ops.ContentDisposition(name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// writeInline sends an image op's file to be shown, not saved: inline, cached by
// its content hash (the browser revalidates, and a matching If-None-Match gets
// 304 with no body). The image gets its own policy: it may not load anything,
// run anything or be framed, even when opened on its own.
func writeInline(w http.ResponseWriter, r *http.Request, dl ops.Download, etag string) {
	name, contentType, body := dl.Download()
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", "inline; filename=\""+ops.SafeFilename(name)+"\"")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'none'; sandbox")
	h.Set("Cache-Control", "private, no-cache")
	h.Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func asMaxBytes(err error, target **http.MaxBytesError) bool { return errors.As(err, target) }

// bindParams fills path and query fields of the input.
func bindParams(in any, fields []ops.FieldLoc, r *http.Request) error {
	v := reflect.ValueOf(in).Elem()
	for _, f := range fields {
		var raw string
		switch f.Location {
		case "path":
			raw = r.PathValue(f.Name)
		case "query":
			raw = r.URL.Query().Get(f.Name)
			if raw == "" {
				continue
			}
		default:
			continue
		}
		fv := v.FieldByIndex(f.Index)
		switch fv.Kind() {
		case reflect.String:
			fv.SetString(raw)
		case reflect.Int, reflect.Int64:
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return ops.Invalid(f.Name, "Expected a number.")
			}
			fv.SetInt(n)
		case reflect.Bool:
			fv.SetBool(strings.EqualFold(raw, "true") || raw == "1")
		}
	}
	return nil
}

// pathMatches reports whether a request path fits an op path pattern, where
// {name} matches one segment.
func pathMatches(pattern, path string) bool {
	ps, qs := strings.Split(strings.Trim(pattern, "/"), "/"), strings.Split(strings.Trim(path, "/"), "/")
	if len(ps) != len(qs) {
		return false
	}
	for i := range ps {
		if strings.HasPrefix(ps[i], "{") && strings.HasSuffix(ps[i], "}") {
			continue
		}
		if ps[i] != qs[i] {
			return false
		}
	}
	return true
}

// Big bodies in flight: an import body is up to 48 MiB and is held about twice while it is handled (the bytes
// received and the file decoded from them). All of them together may hold 192 MiB: with two 31 MiB PDFs
// that is two at once, and a third waits.
const (
	bigBody           = 8 << 20
	maxBodiesInFlight = 192 << 20
)

var bodiesInFlight atomic.Int64

func takeBodyBudget(n int64) bool {
	for {
		cur := bodiesInFlight.Load()
		if cur+n > maxBodiesInFlight {
			return false
		}
		if bodiesInFlight.CompareAndSwap(cur, cur+n) {
			return true
		}
	}
}

func giveBodyBudget(n int64) { bodiesInFlight.Add(-n) }

// readBody reads a request body into a buffer made the size the request says it has (when that is a size the op
// allows), so that a 40 MiB body is not copied again and again as it grows.
func readBody(body io.Reader, contentLength, max int64) ([]byte, error) {
	if contentLength > 0 && contentLength <= max {
		buf := bytes.NewBuffer(make([]byte, 0, contentLength+512))
		if _, err := buf.ReadFrom(body); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	return io.ReadAll(body)
}

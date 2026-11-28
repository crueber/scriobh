package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxInput bounds a file or stdin read into one value.
const maxInput = 32 << 20

// maxUploadFiles and maxUploadBytes bound a directory read for an upload.
const (
	maxUploadFiles = 10000
	maxUploadBytes = 64 << 20
)

// inputs reads values that come from files or stdin.
type inputs struct {
	stdin     io.Reader
	stdinUsed bool
}

func (in *inputs) readStdin() ([]byte, error) {
	if in.stdinUsed {
		return nil, usagef("standard input can supply only one value")
	}
	in.stdinUsed = true
	b, err := io.ReadAll(io.LimitReader(in.stdin, maxInput+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxInput {
		return nil, usagef("standard input is over %d MiB", maxInput>>20)
	}
	return b, nil
}

func readInputFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, usagef("cannot read %s: %v", path, errText(err))
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxInput+1))
	if err != nil {
		return nil, usagef("cannot read %s: %v", path, errText(err))
	}
	if len(b) > maxInput {
		return nil, usagef("%s is over %d MiB", path, maxInput>>20)
	}
	return b, nil
}

func errText(err error) string {
	var pe *fs.PathError
	if e, ok := err.(*fs.PathError); ok {
		pe = e
		return pe.Err.Error()
	}
	return err.Error()
}

// Request is a fully bound HTTP request for one op.
type Request struct {
	Op     Op
	Method string
	Path   string
	Query  url.Values
	Body   []byte // nil when the op sends none
	// ContentType is set when Body is a file rather than JSON.
	ContentType string
}

// readUpload reads the file for a raw-body op: a path, @path, or - for standard
// input. It returns the bytes and the file's base name ("" for standard input).
func (in *inputs) readUpload(v string) ([]byte, string, error) {
	if v == "-" {
		b, err := in.readStdin()
		return b, "", err
	}
	path := strings.TrimPrefix(v, "@")
	b, err := readInputFile(path)
	if err != nil {
		return nil, "", err
	}
	return b, filepath.Base(path), nil
}

// textValue expands one string value: - is stdin, @path is a file, @@ is a
// literal @.
func (in *inputs) textValue(raw string) (string, error) {
	switch {
	case raw == "-":
		b, err := in.readStdin()
		return string(b), err
	case strings.HasPrefix(raw, "@@"):
		return raw[1:], nil
	case strings.HasPrefix(raw, "@") && len(raw) > 1:
		b, err := readInputFile(raw[1:])
		if err != nil {
			if ue, ok := err.(*UsageError); ok {
				ue.Hint = "Write @@ for a value that really starts with @."
			}
			return "", err
		}
		return string(b), nil
	}
	return raw, nil
}

// jsonValue expands a value for an array, object or any field: JSON text,
// @file or - holding JSON, or one of the upload shortcuts.
func (in *inputs) jsonValue(f Field, vals []string) (any, error) {
	if sub := uploadKind(f); sub != "" && len(vals) == 1 && strings.HasPrefix(vals[0], "@") && vals[0] != "@" {
		switch sub {
		case "files":
			return readFolder(strings.TrimPrefix(vals[0], "@"))
		case "bytes":
			b, err := readInputFile(strings.TrimPrefix(vals[0], "@"))
			if err != nil {
				return nil, err
			}
			return base64.StdEncoding.EncodeToString(b), nil
		}
	}
	// A list of strings is a repeated flag.
	if f.Type == "array" && itemType(f) == "string" && !(len(vals) == 1 && strings.HasPrefix(strings.TrimSpace(vals[0]), "[")) &&
		!(len(vals) == 1 && (vals[0] == "-" || strings.HasPrefix(vals[0], "@"))) {
		return vals, nil
	}
	if len(vals) != 1 {
		return nil, usagef("--%s takes one JSON value", f.Flag())
	}
	text := vals[0]
	if text == "-" || (strings.HasPrefix(text, "@") && len(text) > 1) {
		s, err := in.textValue(text)
		if err != nil {
			return nil, err
		}
		text = s
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, &UsageError{Msg: fmt.Sprintf("--%s is not valid JSON: %v", f.Flag(), err), Hint: "Pass JSON, @file.json, or - for standard input."}
	}
	if dec.More() {
		return nil, usagef("--%s has more than one JSON value", f.Flag())
	}
	return v, nil
}

func itemType(f Field) string { t, _ := f.Items["type"].(string); return t }

// uploadKind says whether a field is one of the shapes the CLI can read from
// disk: a list of {path, content|data} files, or a byte string (an archive).
func uploadKind(f Field) string {
	if f.Type != "array" {
		return ""
	}
	if props, ok := f.Items["properties"].(map[string]any); ok {
		_, hasPath := props["path"]
		_, hasContent := props["content"]
		_, hasData := props["data"]
		if hasPath && (hasContent || hasData) {
			return "files"
		}
	}
	if f.Items != nil && len(f.Items) == 0 {
		return "bytes" // a Go []byte: base64 on the wire
	}
	return ""
}

// readFolder reads a directory into a list of {path, content} entries (data,
// base64, for a file that is not UTF-8). Hidden files and directories are
// skipped, symbolic links are not followed, and the total is bounded.
func readFolder(dir string) ([]map[string]any, error) {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return nil, usagef("%s is not a directory", dir)
	}
	var out []map[string]any
	var total int64
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if len(out) >= maxUploadFiles {
			return usagef("%s holds more than %d files", dir, maxUploadFiles)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if total += int64(len(b)); total > maxUploadBytes {
			return usagef("%s holds more than %d MiB", dir, maxUploadBytes>>20)
		}
		rel, _ := filepath.Rel(dir, path)
		e := map[string]any{"path": filepath.ToSlash(rel)}
		if utf8.Valid(b) {
			e["content"] = string(b)
		} else {
			e["data"] = base64.StdEncoding.EncodeToString(b)
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		if ue, ok := err.(*UsageError); ok {
			return nil, ue
		}
		return nil, usagef("cannot read %s: %v", dir, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["path"].(string) < out[j]["path"].(string) })
	return out, nil
}

// scalar converts one typed value.
func scalar(f Field, raw string) (any, error) {
	switch f.Type {
	case "integer":
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, usagef("--%s takes a whole number, not %q", f.Flag(), raw)
		}
		return n, nil
	case "number":
		n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return nil, usagef("--%s takes a number, not %q", f.Flag(), raw)
		}
		return n, nil
	case "boolean":
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, usagef("--%s takes true or false, not %q", f.Flag(), raw)
		}
		return b, nil
	}
	if len(f.Enum) > 0 {
		for _, e := range f.Enum {
			if e == raw {
				return raw, nil
			}
		}
		return nil, usagef("--%s must be one of %s, not %q", f.Flag(), strings.Join(f.Enum, ", "), raw)
	}
	return raw, nil
}

// fieldValue converts everything typed for a field into its wire value.
func (in *inputs) fieldValue(inv *Invocation, f Field) (any, bool, error) {
	if path := inv.FileFlags[f.Name]; path != "" {
		b, err := readInputFile(path)
		if err != nil {
			return nil, false, err
		}
		return string(b), true, nil
	}
	vals := inv.Values[f.Name]
	if len(vals) == 0 {
		return nil, false, nil
	}
	switch f.Type {
	case "array", "object", "any":
		v, err := in.jsonValue(f, vals)
		return v, err == nil, err
	}
	if len(vals) > 1 {
		return nil, false, usagef("--%s was given more than once", f.Flag())
	}
	raw := vals[0]
	if f.Type == "string" && f.Ref == "" && len(f.Enum) == 0 {
		s, err := in.textValue(raw)
		return s, err == nil, err
	}
	v, err := scalar(f, raw)
	return v, err == nil, err
}

// dataObject reads the --data body.
func (in *inputs) dataObject(spec string) (map[string]any, error) {
	if spec == "" {
		return nil, nil
	}
	text, err := in.textValue(spec)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, &UsageError{Msg: "--data must be a JSON object", Hint: "Pass JSON, @file.json, or - for standard input."}
	}
	return m, nil
}

// destructive reports whether an op needs --yes: it deletes, purges, revokes
// or prunes.
func destructive(o Op) bool {
	if o.Method == "DELETE" || o.Permanent {
		return true
	}
	ws := o.Words()
	switch ws[len(ws)-1] {
	case "trash", "purge", "delete", "revoke", "revoke-others", "prune":
		return true
	}
	return false
}

// Validate checks what can be checked without the network or any file: the
// types and enum values of every flag, and the required flags when no --data
// body can supply them. Run it before any name is looked up, so a typing
// mistake costs no request.
func (inv *Invocation) Validate() error {
	for _, f := range inv.Fields {
		vals := inv.Values[f.Name]
		if len(vals) == 0 || f.Ref != "" {
			continue
		}
		switch f.Type {
		case "integer", "number", "boolean":
			if len(vals) > 1 {
				return usagef("--%s was given more than once", f.Flag())
			}
			if _, err := scalar(f, vals[0]); err != nil {
				return err
			}
		case "string":
			if len(f.Enum) > 0 && len(vals) == 1 {
				if _, err := scalar(f, vals[0]); err != nil {
					return err
				}
			}
		}
	}
	if inv.Global.Data == "" {
		if miss := inv.Missing(nil); len(miss) > 0 {
			return &UsageError{Msg: fmt.Sprintf("%q needs %s", inv.Op.Command(), strings.Join(miss, ", ")),
				Hint: "Run: scriobh-cli " + inv.Op.Command() + " --help"}
		}
	}
	return nil
}

func (inv *Invocation) isDryRun() bool {
	if inv.Global.DryRun {
		return true
	}
	vals := inv.Values["dry_run"]
	return len(vals) > 0 && strings.EqualFold(strings.TrimSpace(vals[len(vals)-1]), "true")
}

// Preflight refuses what the server would refuse or what the command line
// promises not to do, before any request: a preview of an op that cannot be
// previewed, a destructive command without --yes, and a file response with
// nowhere to put it. The skill's examples are held to the same rules.
func (inv *Invocation) Preflight() error {
	op, g := inv.Op, inv.Global
	dry := inv.isDryRun()
	if dry && op.NoDryRun {
		return &Fail{Code: "dry_run_unsupported", Exit: ExitValidation,
			Message: op.Command() + " cannot be previewed with --dry-run; it has effects a rollback cannot undo.",
			Hint:    "Read the command's help, check the input yourself, and run it without --dry-run."}
	}
	if destructive(op) && !g.Yes && !dry {
		return &UsageError{Msg: op.Command() + " is destructive and needs --yes.",
			Hint: "Preview it first with --dry-run, then repeat with --yes."}
	}
	if isFileOp(op) && g.Output == "" && !dry {
		return &UsageError{Msg: op.Command() + " returns a file; say where to put it with --output FILE (or - for standard output).",
			Hint: "Example: scriobh-cli " + op.Command() + " --output result.out"}
	}
	return nil
}

// Bind turns an invocation into a request: values are converted, files and
// standard input read, required inputs checked, and the path filled. resolved
// replaces the value of a ref field with the id a name resolved to.
func Bind(inv *Invocation, stdin io.Reader, resolved map[string]string) (*Request, error) {
	in := &inputs{stdin: stdin}
	data, err := in.dataObject(inv.Global.Data)
	if err != nil {
		return nil, err
	}
	dataKeys := map[string]bool{}
	for k := range data {
		dataKeys[k] = true
	}
	if miss := inv.Missing(dataKeys); len(miss) > 0 {
		return nil, &UsageError{Msg: fmt.Sprintf("%q needs %s", inv.Op.Command(), strings.Join(miss, ", ")),
			Hint: "Run: scriobh-cli " + inv.Op.Command() + " --help"}
	}
	req := &Request{Op: inv.Op, Method: inv.Op.Method, Path: inv.Op.Path, Query: url.Values{}}
	body := map[string]any{}
	for k, v := range data {
		body[k] = v
	}
	var upload []byte
	for _, f := range inv.Fields {
		if f.Loc == "file" {
			vals := inv.Values[f.Name]
			if len(vals) != 1 {
				return nil, usagef("--%s takes one file", f.Flag())
			}
			b, name, err := in.readUpload(vals[0])
			if err != nil {
				return nil, err
			}
			upload = b
			// The name defaults to the file's, unless one was given.
			if _, given := inv.Values["filename"]; !given && name != "" {
				for _, g := range inv.Fields {
					if g.Name == "filename" && g.Loc == "query" {
						req.Query.Set("filename", name)
					}
				}
			}
			continue
		}
		var v any
		var ok bool
		if id, has := resolved[f.Name]; has {
			v, ok = id, true
		} else if v, ok, err = in.fieldValue(inv, f); err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		switch f.Loc {
		case "path":
			s := fmt.Sprint(v)
			if s == "" {
				return nil, usagef("--%s must not be empty", f.Flag())
			}
			req.Path = strings.ReplaceAll(req.Path, "{"+f.Name+"}", url.PathEscape(s))
		case "query":
			req.Query.Set(f.Name, fmt.Sprint(v))
		default:
			body[f.Name] = v
		}
	}
	if strings.Contains(req.Path, "{") {
		return nil, usagef("%q is missing a path value", inv.Op.Command())
	}
	// The op's own preview flag follows the global one, so both mean the same.
	if inv.Global.DryRun {
		for _, f := range inv.Fields {
			if f.Name == "dry_run" && f.Loc == "body" && f.Type == "boolean" {
				body["dry_run"] = true
			}
		}
	}
	if inv.Op.RawBody {
		req.Body, req.ContentType = upload, "application/octet-stream"
		return req, nil
	}
	if hasBodyFields(inv.Fields) && inv.Op.Mutates() {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req.Body = bytes.TrimSpace(b)
	}
	return req, nil
}

func hasBodyFields(fs []Field) bool {
	for _, f := range fs {
		if f.Loc == "body" {
			return true
		}
	}
	return false
}

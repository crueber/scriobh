package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

// SchemaVersion is the version of the op-schema document format.
const SchemaVersion = 1

// OpSchema is one op as published at GET /api/v1/_ops.
type OpSchema struct {
	Name   string `json:"name"`
	Method string `json:"method"`
	Path   string `json:"path"`
	UI     string `json:"ui,omitempty"`
	Desc   string `json:"desc"`
	Auth   Auth   `json:"auth"`
	Scope  string `json:"scope"`
	Status int    `json:"status"`
	// NoDryRun is true for an op that refuses --dry-run.
	NoDryRun bool `json:"no_dry_run,omitempty"`
	// RawBody is true when the request body is the bytes of a file.
	RawBody bool `json:"raw_body,omitempty"`
	// Permanent is true for an op that destroys data beyond the trash.
	Permanent bool           `json:"permanent,omitempty"`
	Input     map[string]any `json:"input"`
	Output    map[string]any `json:"output"`
}

// Schema is the whole registry, with a content hash so clients can detect a
// stale snapshot.
type Schema struct {
	Version int        `json:"version"`
	Hash    string     `json:"hash"`
	Ops     []OpSchema `json:"ops"`
}

// Schema builds the schema document. It is deterministic.
func (r *Registry) Schema() Schema {
	s := Schema{Version: SchemaVersion}
	for _, e := range r.Entries() {
		s.Ops = append(s.Ops, OpSchema{
			Name: e.Op.Name, Method: e.Op.Method, Path: e.Op.Path, UI: e.Op.UI, Desc: e.Op.Desc,
			Auth: e.Op.Auth, Scope: e.Op.Scope, Status: e.Op.Status, NoDryRun: e.Op.NoDryRun, RawBody: e.Op.RawBody, Permanent: e.Op.Permanent,
			Input: schemaOf(e.In), Output: schemaOf(e.Out),
		})
	}
	b, _ := json.Marshal(s.Ops)
	sum := sha256.Sum256(b)
	s.Hash = "sha256:" + hex.EncodeToString(sum[:])
	return s
}

var timeType = reflect.TypeOf(time.Time{})

// schemaOf renders a JSON-schema-ish description of t. Field tags:
//
//	json:"name,omitempty"  body field (omitempty = optional)
//	path:"id"              path parameter (always required)
//	query:"q"              query parameter
//	desc:"..."             description
//	enum:"a,b"             allowed values
//	ref:"project"          the field holds the id of that kind of object, so
//	                       a client may accept a name and look the id up
func schemaOf(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaOf(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaOf(t.Elem())}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Struct:
		if t == timeType {
			return map[string]any{"type": "string", "format": "date-time"}
		}
		props := map[string]any{}
		var required []string
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if isEmbeddedStruct(f) {
				sub := schemaOf(f.Type)
				if sp, ok := sub["properties"].(map[string]any); ok {
					for k, v := range sp {
						props[k] = v
					}
				}
				if sr, ok := sub["required"].([]string); ok {
					required = append(required, sr...)
				}
				continue
			}
			if !f.IsExported() {
				continue
			}
			name, loc, optional := fieldName(f)
			if name == "" {
				continue
			}
			p := schemaOf(f.Type)
			if loc != "body" {
				p["in"] = loc
			}
			if d := f.Tag.Get("desc"); d != "" {
				p["description"] = d
			}
			if ref := f.Tag.Get("ref"); ref != "" {
				p["ref"] = ref
			}
			if en := f.Tag.Get("enum"); en != "" {
				var vals []any
				for _, v := range strings.Split(en, ",") {
					vals = append(vals, v)
				}
				p["enum"] = vals
			}
			props[name] = p
			if !optional {
				required = append(required, name)
			}
		}
		out := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			out["required"] = required
		}
		return out
	}
	return map[string]any{}
}

// fieldName resolves a struct field's wire name and location.
func fieldName(f reflect.StructField) (name, loc string, optional bool) {
	if v := f.Tag.Get("path"); v != "" {
		return v, "path", false
	}
	if v := f.Tag.Get("query"); v != "" {
		return v, "query", true
	}
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", "", false
	}
	n, opts, _ := strings.Cut(tag, ",")
	if n == "" {
		n = f.Name
	}
	return n, "body", strings.Contains(opts, "omitempty") || f.Type.Kind() == reflect.Pointer
}

// isEmbeddedStruct reports whether f is an anonymous embedded struct whose
// fields should be flattened into the parent object schema and input fields.
func isEmbeddedStruct(f reflect.StructField) bool {
	if !f.Anonymous {
		return false
	}
	if f.Tag.Get("json") != "" || f.Tag.Get("path") != "" || f.Tag.Get("query") != "" {
		return false
	}
	ft := f.Type
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	return ft.Kind() == reflect.Struct && ft != timeType
}

// FieldLoc describes where an input field lives on the wire.
type FieldLoc struct {
	Index    []int
	Name     string
	Location string // body, path, query
}

// InputFields lists the input struct's wire fields, for binders.
func InputFields(t reflect.Type) []FieldLoc {
	return inputFields(t, nil)
}

func inputFields(t reflect.Type, prefix []int) []FieldLoc {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var out []FieldLoc
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		idx := append(append([]int(nil), prefix...), i)
		if isEmbeddedStruct(f) {
			out = append(out, inputFields(f.Type, idx)...)
			continue
		}
		if !f.IsExported() {
			continue
		}
		if n, loc, _ := fieldName(f); n != "" {
			out = append(out, FieldLoc{Index: idx, Name: n, Location: loc})
		}
	}
	return out
}

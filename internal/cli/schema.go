// Package cli is the agent-facing command line: it reads the op schema a
// server publishes at /api/v1/_ops and turns every op into a command, so the
// CLI never knows an op by name. It does not import the server, the database
// or the op packages (a test lists its imports); it speaks HTTP and JSON only.
//
// The same package renders the agent skill from a schema, so the skill, the
// command reference in it and the commands the CLI accepts cannot disagree.
package cli

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed snapshot.json
var snapshotJSON []byte

// Schema is the op schema document served at GET /api/v1/_ops.
type Schema struct {
	Version int    `json:"version"`
	Hash    string `json:"hash"`
	Ops     []Op   `json:"ops"`
}

// Op is one op as published.
type Op struct {
	Name     string `json:"name"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	UI       string `json:"ui,omitempty"`
	Desc     string `json:"desc"`
	Auth     Auth   `json:"auth"`
	Scope    string `json:"scope"`
	Status   int    `json:"status"`
	NoDryRun bool   `json:"no_dry_run,omitempty"`
	// RawBody is true when the request body is the bytes of a file: the command
	// takes --file, and the CLI sends the file as the body.
	RawBody   bool           `json:"raw_body,omitempty"`
	Permanent bool           `json:"permanent,omitempty"`
	Input     map[string]any `json:"input"`
	Output    map[string]any `json:"output"`
}

// Auth is what an op requires of the caller.
type Auth struct {
	Kind string `json:"kind"`
	Role string `json:"role,omitempty"`
}

// Embedded returns the schema this build was made with.
func Embedded() Schema {
	var s Schema
	if err := json.Unmarshal(snapshotJSON, &s); err != nil {
		panic("cli: embedded snapshot.json is not valid: " + err.Error())
	}
	return s
}

// ParseSchema decodes a schema document.
func ParseSchema(b []byte) (Schema, error) {
	var s Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return Schema{}, fmt.Errorf("the schema is not valid JSON: %w", err)
	}
	if len(s.Ops) == 0 {
		return Schema{}, fmt.Errorf("the schema lists no operations")
	}
	return s, nil
}

// Field is one input of an op.
type Field struct {
	// Name is the wire name (project_id, base_revision).
	Name string
	// Loc is where it travels: path, query or body.
	Loc      string
	Type     string // string, integer, number, boolean, array, object, any
	Required bool
	Desc     string
	Enum     []string
	// Ref is the kind of object the value identifies ("project"), from the
	// schema's ref tag or inferred from the path; empty when it is not an id.
	Ref string
	// Items is the element schema of an array field.
	Items map[string]any
}

// Flag is the field's flag name: the wire name with hyphens.
func (f Field) Flag() string { return strings.ReplaceAll(f.Name, "_", "-") }

// Fields lists an op's inputs: path parameters first, then query, then body,
// each group by name.
func (o Op) Fields() []Field {
	props, _ := o.Input["properties"].(map[string]any)
	required := map[string]bool{}
	if rs, ok := o.Input["required"].([]any); ok {
		for _, r := range rs {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	var out []Field
	for name, raw := range props {
		p, _ := raw.(map[string]any)
		f := Field{Name: name, Loc: "body", Required: required[name]}
		if v, ok := p["in"].(string); ok {
			f.Loc = v
		}
		f.Type, _ = p["type"].(string)
		if f.Type == "" {
			f.Type = "any"
		}
		f.Desc, _ = p["description"].(string)
		if en, ok := p["enum"].([]any); ok {
			for _, e := range en {
				f.Enum = append(f.Enum, fmt.Sprint(e))
			}
		}
		f.Ref, _ = p["ref"].(string)
		f.Items, _ = p["items"].(map[string]any)
		out = append(out, f)
	}
	if o.RawBody {
		// The file travels as the request body. It is a flag like any other, so
		// help, the required-input check and the skill all list it.
		out = append(out, Field{Name: "file", Loc: "file", Type: "string", Required: true,
			Desc: "The file to upload: a path (or @path), or - for standard input."})
	}
	rank := map[string]int{"path": 0, "query": 1, "body": 2, "file": 3}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Loc] != rank[out[j].Loc] {
			return rank[out[i].Loc] < rank[out[j].Loc]
		}
		if out[i].Loc == "path" {
			// Path values bind to bare arguments in the order they appear in the
			// address: .../projects/{id}/attachments/{att_id} takes the project first.
			return strings.Index(o.Path, "{"+out[i].Name+"}") < strings.Index(o.Path, "{"+out[j].Name+"}")
		}
		return out[i].Name < out[j].Name
	})
	for i := range out {
		if out[i].Ref == "" {
			out[i].Ref = inferRef(o, out[i])
		}
	}
	return out
}

// Words is the command spelling of the op: doc.update is "doc update", and an
// underscore in a word becomes a hyphen (doc.resolve_link is "doc resolve-link").
func (o Op) Words() []string {
	parts := strings.Split(o.Name, ".")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(p, "_", "-")
	}
	return parts
}

// Command is the op's command as typed: "doc update".
func (o Op) Command() string { return strings.Join(o.Words(), " ") }

// Group is the first word of the command.
func (o Op) Group() string { return o.Words()[0] }

// Mutates reports whether the op changes state (anything but GET and HEAD).
func (o Op) Mutates() bool { return o.Method != "GET" && o.Method != "HEAD" }

// PathParams lists the {names} in the path, in order.
func (o Op) PathParams() []string {
	var out []string
	for _, seg := range strings.Split(o.Path, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			out = append(out, seg[1:len(seg)-1])
		}
	}
	return out
}

// kindAliases maps a path segment or id prefix to the kind of object.
var kindAliases = map[string]string{"document": "doc", "documents": "doc", "docs": "doc"}

// singular turns a collection segment into a kind: projects to project,
// entities to entity, docs to doc.
func singular(seg string) string {
	if k, ok := kindAliases[seg]; ok {
		return k
	}
	switch {
	case strings.HasSuffix(seg, "ies"):
		return seg[:len(seg)-3] + "y"
	case strings.HasSuffix(seg, "sses"):
		return seg[:len(seg)-2]
	case strings.HasSuffix(seg, "s") && !strings.HasSuffix(seg, "ss"):
		return seg[:len(seg)-1]
	}
	return seg
}

// inferRef works out what kind of object a field's value identifies when the
// schema does not say: the path segment before {id}, a name ending in _id, or
// the op's own noun for a sibling or parent reference.
func inferRef(o Op, f Field) string {
	name := f.Name
	if f.Type != "string" {
		return ""
	}
	if f.Loc == "path" && name == "id" {
		segs := strings.Split(strings.Trim(o.Path, "/"), "/")
		for i, s := range segs {
			if s == "{id}" && i > 0 && !strings.HasPrefix(segs[i-1], "{") && !strings.HasPrefix(segs[i-1], "_") {
				return singular(segs[i-1])
			}
		}
		return ""
	}
	switch name {
	case "parent_id", "after", "before":
		return o.Group()
	}
	if strings.HasSuffix(name, "_id") && len(name) > 3 {
		k := strings.TrimSuffix(name, "_id")
		if a, ok := kindAliases[k]; ok {
			k = a
		}
		return k
	}
	return ""
}

// Index finds ops by command words.
type Index struct {
	Schema Schema
	byName map[string]Op
}

// NewIndex indexes a schema.
func NewIndex(s Schema) *Index {
	ix := &Index{Schema: s, byName: map[string]Op{}}
	for _, o := range s.Ops {
		ix.byName[o.Name] = o
	}
	return ix
}

// ByName returns an op by its registry name.
func (ix *Index) ByName(name string) (Op, bool) { o, ok := ix.byName[name]; return o, ok }

// Match finds the op whose command is the longest prefix of args and returns
// the words left over. Words are matched case-insensitively.
func (ix *Index) Match(args []string) (op Op, rest []string, ok bool) {
	best := -1
	for _, o := range ix.Schema.Ops {
		ws := o.Words()
		if len(ws) > len(args) || len(ws) <= best {
			continue
		}
		match := true
		for i, w := range ws {
			if !strings.EqualFold(args[i], w) {
				match = false
				break
			}
		}
		if match {
			op, rest, ok, best = o, args[len(ws):], true, len(ws)
		}
	}
	return
}

// Groups returns the sorted distinct first words, with their ops.
func (ix *Index) Groups() map[string][]Op {
	g := map[string][]Op{}
	for _, o := range ix.Schema.Ops {
		g[o.Group()] = append(g[o.Group()], o)
	}
	return g
}

// SortedOps returns the ops in command order.
func (ix *Index) SortedOps() []Op {
	ops := append([]Op(nil), ix.Schema.Ops...)
	sort.Slice(ops, func(i, j int) bool { return ops[i].Command() < ops[j].Command() })
	return ops
}

package skillgen

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/ops"
)

// APIDocPath is the hand-written API reference. Only the part between the two
// markers below is generated; everything else in the file is prose.
const APIDocPath = "docs/API.md"

// The markers that fence the generated op table in docs/API.md. Each must
// appear exactly once, begin first, on a line of its own.
const (
	APIBegin = "<!-- BEGIN GENERATED: op table (make skill) -->"
	APIEnd   = "<!-- END GENERATED: op table -->"
)

// GenerateAll is Generate plus the API reference: it reads docs/API.md under
// root and returns it with the op table regenerated, so `make skill` and the
// drift test treat it as one more generated file.
func GenerateAll(reg *ops.Registry, root string) (map[string][]byte, error) {
	files, err := Generate(reg)
	if err != nil {
		return nil, err
	}
	have, err := os.ReadFile(filepath.Join(root, APIDocPath))
	if err != nil {
		return nil, fmt.Errorf("%s is missing: it holds the markers the op table is written between: %w", APIDocPath, err)
	}
	doc, err := SpliceAPIDoc(have, reg.Schema())
	if err != nil {
		return nil, err
	}
	files[APIDocPath] = doc
	return files, nil
}

// SpliceAPIDoc returns the API reference with the op table between the markers
// replaced by one rendered from the registry's schema. It is the same function
// `make skill` writes with and the drift test compares with.
func SpliceAPIDoc(current []byte, s ops.Schema) ([]byte, error) {
	text := string(current)
	if n := strings.Count(text, APIBegin); n != 1 {
		return nil, fmt.Errorf("%s must hold the marker %q exactly once (found %d)", APIDocPath, APIBegin, n)
	}
	if n := strings.Count(text, APIEnd); n != 1 {
		return nil, fmt.Errorf("%s must hold the marker %q exactly once (found %d)", APIDocPath, APIEnd, n)
	}
	b, e := strings.Index(text, APIBegin), strings.Index(text, APIEnd)
	if b > e {
		return nil, fmt.Errorf("%s: the BEGIN marker must come before the END marker", APIDocPath)
	}
	var out strings.Builder
	out.WriteString(text[:b+len(APIBegin)])
	out.WriteString("\n\n")
	out.WriteString(RenderAPITable(s))
	out.WriteString("\n")
	out.WriteString(text[e:])
	return []byte(out.String()), nil
}

// RenderAPITable renders every op of the schema as Markdown tables grouped by
// the first segment of the op name. It is deterministic: the same schema gives
// the same bytes.
func RenderAPITable(s ops.Schema) string {
	groups := map[string][]ops.OpSchema{}
	for _, o := range s.Ops {
		g, _, _ := strings.Cut(o.Name, ".")
		groups[g] = append(groups[g], o)
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)

	var b strings.Builder
	fmt.Fprintf(&b, "%d operations, schema `%s`. Generated from the op registry by `make skill`; do not edit between the markers.\n", len(s.Ops), s.Hash[:19])
	for _, g := range names {
		list := groups[g]
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		fmt.Fprintf(&b, "\n#### `%s` (%d)\n\n", g, len(list))
		b.WriteString("| Op | Request | Access | Scope | Notes | What it does |\n| --- | --- | --- | --- | --- | --- |\n")
		for _, o := range list {
			fmt.Fprintf(&b, "| `%s` | `%s %s` | %s | %s | %s | %s |\n", o.Name, o.Method, o.Path, accessCell(o.Auth), o.Scope, notesCell(o), cell(firstSentence(o.Desc)))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// accessCell says who may call the op, in words.
func accessCell(a ops.Auth) string {
	var who string
	switch a.Kind {
	case ops.AuthAuthenticated:
		who = "any signed-in caller"
	case ops.AuthWriter:
		who = "instance writer"
	case ops.AuthAdmin:
		who = "instance admin"
	case ops.AuthProject:
		who = "project " + a.Role
	case ops.AuthCompendium:
		who = "compendium " + a.Role
	default:
		who = string(a.Kind)
	}
	return who
}

// notesCell lists what is unusual about an op: who cannot call it, what a
// caller cannot do with it and what it answers on success.
func notesCell(o ops.OpSchema) string {
	var n []string
	if o.Auth.SessionOnly {
		n = append(n, "browser session only")
	}
	if o.Auth.Hidden {
		n = append(n, "404 to others")
	}
	if o.NoDryRun {
		n = append(n, "no dry run")
	}
	if o.Permanent {
		n = append(n, "permanent")
	}
	if o.RawBody {
		n = append(n, "body is the file")
	}
	if o.Status != 200 {
		n = append(n, fmt.Sprintf("%d on success", o.Status))
	}
	return strings.Join(n, ", ")
}

// firstSentence is the start of the op's description: whole sentences until it
// is at least 60 characters, cut at a word boundary when it is still long.
func firstSentence(d string) string {
	d = strings.Join(strings.Fields(d), " ")
	end := 0
	for end < len(d) {
		i := strings.Index(d[end:], ". ")
		if i < 0 {
			end = len(d)
			break
		}
		end += i + 1
		if len([]rune(d[:end])) >= 60 {
			break
		}
		end++ // the space after the full stop
	}
	d = strings.TrimSpace(d[:end])
	const max = 150
	if r := []rune(d); len(r) > max {
		head := string(r[:max])
		cut := strings.LastIndex(head, " ")
		if cut < 40 {
			cut = len(head)
		}
		d = strings.TrimRight(head[:cut], " ,;:") + "…"
	}
	return d
}

// cell makes text safe for a Markdown table cell.
func cell(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}

package cli

import (
	"fmt"
	"strings"
)

// placeholder is how a flag's value is shown in usage text.
func placeholder(f Field) string {
	switch {
	case f.Ref != "":
		return f.Ref + " name or id"
	case len(f.Enum) > 0:
		return strings.Join(f.Enum, "|")
	}
	switch f.Type {
	case "integer":
		return "N"
	case "number":
		return "N"
	case "boolean":
		return ""
	case "array":
		if uploadKind(f) == "files" {
			return "@DIR"
		}
		if uploadKind(f) == "bytes" {
			return "@FILE"
		}
		return "JSON"
	case "object", "any":
		return "JSON"
	}
	return "TEXT"
}

// UsageLine is a command's synopsis: positionals, then required flags, then
// [flags] for the rest.
func UsageLine(o Op) string {
	var b strings.Builder
	b.WriteString("scriobh-cli " + o.Command())
	fs := o.Fields()
	for _, f := range fs {
		if f.Loc == "path" {
			n := f.Ref
			if n == "" {
				n = f.Name
			}
			fmt.Fprintf(&b, " <%s>", n)
		}
	}
	optional := false
	for _, f := range fs {
		if f.Loc == "path" {
			continue
		}
		if !f.Required {
			optional = true
			continue
		}
		if p := placeholder(f); p != "" {
			fmt.Fprintf(&b, " --%s %s", f.Flag(), p)
		} else {
			fmt.Fprintf(&b, " --%s", f.Flag())
		}
	}
	if optional {
		b.WriteString(" [flags]")
	}
	return b.String()
}

// FlagHelp is one line of a command's flag list.
func FlagHelp(f Field) string {
	var b strings.Builder
	name := "--" + f.Flag()
	if f.Loc == "path" {
		name = "<" + firstNonEmpty(f.Ref, f.Name) + ">, --" + f.Flag()
		if f.Ref != "" && f.Ref != f.Flag() {
			name += ", --" + f.Ref
		}
	} else if f.Ref != "" && f.Ref != f.Flag() {
		name += ", --" + f.Ref
	}
	b.WriteString(name)
	if p := placeholder(f); p != "" && f.Loc != "path" {
		b.WriteString(" " + p)
	}
	var tags []string
	if f.Required {
		tags = append(tags, "required")
	}
	if f.Loc == "query" {
		tags = append(tags, "query")
	}
	if f.Ref != "" {
		tags = append(tags, "name or id")
	}
	if len(tags) > 0 {
		b.WriteString("  (" + strings.Join(tags, ", ") + ")")
	}
	if f.Desc != "" {
		b.WriteString("  " + f.Desc)
	}
	return b.String()
}

// exampleString is a plausible value for a string flag.
func exampleString(f Field) string {
	switch f.Ref {
	case "project":
		return "The Salt Road"
	case "doc":
		return "The Salt Road/Chapter 1"
	case "compendium":
		return "The Salt Road"
	case "entity":
		return "Máire Nolan"
	case "key":
		return "agent one"
	}
	switch f.Name {
	case "att_id":
		return "att_01HZZZZZZZZZZZZZZZZZZZZZZZ"
	case "note_id":
		return "rsn_01HZZZZZZZZZZZZZZZZZZZZZZZ"
	case "tag_id", "into_id":
		return "tag_01HZZZZZZZZZZZZZZZZZZZZZZZ"
	case "title":
		return "Chapter 1"
	case "name":
		return "The Salt Road"
	case "content", "body", "text":
		return "@chapter-1.md"
	case "q", "query":
		return "the harbour"
	case "token":
		return "TOKEN"
	case "label", "note":
		return "first pass"
	case "confirm_title":
		return "The Salt Road"
	case "kind":
		return "chapter"
	}
	return "text"
}

// ExampleValue renders a flag value for an example command, already quoted
// for a shell.
func ExampleValue(f Field) string {
	switch {
	case f.Loc == "file":
		return "@./figure.png"
	case len(f.Enum) > 0:
		return f.Enum[0]
	case f.Type == "integer" || f.Type == "number":
		return "1"
	case f.Type == "boolean":
		return ""
	case f.Type == "array" && uploadKind(f) == "files":
		return "@./manuscript"
	case f.Type == "array" && uploadKind(f) == "bytes":
		return "@book.zip"
	case f.Type == "array" && itemType(f) == "string":
		return "one"
	case f.Type == "array":
		return "'[]'"
	case f.Type == "object" || f.Type == "any":
		return "'{}'"
	}
	return shellQuote(exampleString(f))
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@./_-:=,", r))
	}) < 0 {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(s) + `"`
}

// Example is a runnable command line for an op: positionals for the path, and
// every required flag with a plausible value.
func Example(o Op) string {
	var b strings.Builder
	b.WriteString("scriobh-cli " + o.Command())
	fs := o.Fields()
	for _, f := range fs {
		if f.Loc == "path" {
			b.WriteString(" " + ExampleValue(f))
		}
	}
	for _, f := range fs {
		if f.Loc == "path" || !f.Required {
			continue
		}
		if v := ExampleValue(f); v != "" {
			fmt.Fprintf(&b, " --%s %s", f.Flag(), v)
		} else {
			fmt.Fprintf(&b, " --%s", f.Flag())
		}
	}
	if destructive(o) {
		b.WriteString(" --yes")
	}
	if isFileOp(o) {
		b.WriteString(" --output result.out")
	}
	return b.String()
}

// SplitCommand splits a command line the way a shell would for the simple
// cases the skill uses: single and double quotes and backslash escapes. It is
// exported for the test that checks every example in the skill.
func SplitCommand(line string) ([]string, error) {
	var out []string
	var cur strings.Builder
	in := false
	var quote rune
	esc := false
	for _, r := range line {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				esc = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			esc, in = true, true
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		case r == '#' && !in:
			return out, nil // a trailing comment
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 || esc {
		return nil, fmt.Errorf("unterminated quote in %q", line)
	}
	if in {
		out = append(out, cur.String())
	}
	return out, nil
}

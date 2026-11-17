package cli

import (
	"fmt"
	"sort"
	"strings"
)

// UsageError is a mistake in how the command was typed. It exits 2.
type UsageError struct {
	Msg  string
	Hint string
}

func (e *UsageError) Error() string { return e.Msg }

func usagef(format string, a ...any) *UsageError { return &UsageError{Msg: fmt.Sprintf(format, a...)} }

// Global holds the flags every command accepts.
type Global struct {
	Server         string
	Key            string
	JSON           bool
	DryRun         bool
	IdempotencyKey string
	NoIdempotency  bool
	Yes            bool
	Output         string
	All            bool
	MaxPages       int
	Data           string
	FailOnChange   bool
	Help           bool
	Config         string
	Timeout        string
	Quiet          bool
	NoCache        bool
	Retries        int
	retriesSet     bool
}

// globalFlag describes one global flag.
type globalFlag struct {
	name  string
	short string
	bool  bool
	help  string
}

var globalFlags = []globalFlag{
	{"server", "", false, "Server URL (or SCRIOBH_URL)."},
	{"key", "", false, "API key (or SCRIOBH_API_KEY, or the saved login)."},
	{"json", "", true, "Print the JSON envelope even on a terminal."},
	{"dry-run", "", true, "Run the command on the server and roll it back; nothing is changed."},
	{"idempotency-key", "", false, "Use this Idempotency-Key for the write (default: a fresh random one)."},
	{"no-idempotency", "", true, "Do not send an Idempotency-Key."},
	{"yes", "y", true, "Confirm a destructive command."},
	{"output", "o", false, "Write a file response here (- for stdout)."},
	{"all", "", true, "Follow next_cursor and collect every page."},
	{"max-pages", "", false, "With --all, stop after this many pages (default 100)."},
	{"data", "", false, "Send this JSON as the request body (@file or - for stdin); flags override its fields."},
	{"fail-on-change", "", true, "With --dry-run, exit 9 when the command would change anything."},
	{"config", "", false, "Config file (default: the user config directory)."},
	{"timeout", "", false, "Request timeout, for example 30s (default 60s)."},
	{"quiet", "q", true, "Print no human text on stderr."},
	{"no-cache", "", true, "Fetch the schema from the server, ignoring the cache."},
	{"retries", "", false, "Retries on a network failure or 502/503/504, with the same key (default 2)."},
	{"help", "h", true, "Show help for the command."},
}

func findGlobal(name string, short bool) (globalFlag, bool) {
	if !short && name == "out" { // --out is --output, as people write it
		name = "output"
	}
	for _, g := range globalFlags {
		if (!short && g.name == name) || (short && g.short == name) {
			return g, true
		}
	}
	return globalFlag{}, false
}

// setGlobal applies one global flag value.
func (g *Global) set(name, val string) error {
	boolean := func() (bool, error) {
		switch strings.ToLower(val) {
		case "", "1", "true", "yes":
			return true, nil
		case "0", "false", "no":
			return false, nil
		}
		return false, usagef("--%s takes true or false, not %q", name, val)
	}
	var err error
	switch name {
	case "server":
		g.Server = val
	case "key":
		g.Key = val
	case "json":
		g.JSON, err = boolean()
	case "dry-run":
		g.DryRun, err = boolean()
	case "idempotency-key":
		g.IdempotencyKey = val
	case "no-idempotency":
		g.NoIdempotency, err = boolean()
	case "yes":
		g.Yes, err = boolean()
	case "output":
		g.Output = val
	case "all":
		g.All, err = boolean()
	case "max-pages":
		_, err = fmt.Sscanf(val, "%d", &g.MaxPages)
		if err != nil || g.MaxPages < 1 {
			err = usagef("--max-pages takes a positive number")
		}
	case "data":
		g.Data = val
	case "fail-on-change":
		g.FailOnChange, err = boolean()
	case "config":
		g.Config = val
	case "timeout":
		g.Timeout = val
	case "quiet":
		g.Quiet, err = boolean()
	case "no-cache":
		g.NoCache, err = boolean()
	case "retries":
		g.retriesSet = true
		_, err = fmt.Sscanf(val, "%d", &g.Retries)
		if err != nil || g.Retries < 0 || g.Retries > 10 {
			err = usagef("--retries takes a number from 0 to 10")
		}
	case "help":
		g.Help, err = boolean()
	}
	return err
}

// Invocation is a parsed command line, before names are resolved.
type Invocation struct {
	Op     Op
	Fields []Field
	Global Global
	// Values holds what was typed for each field, by wire name. A repeated
	// flag appends. A value of the form @file or - is expanded later.
	Values map[string][]string
	// FileFlags holds --<field>-file values.
	FileFlags map[string]string
	// Hints are context flags such as --project when the op has no such field:
	// they scope name resolution.
	Hints map[string]string
}

// flagSpec is one accepted op flag.
type flagSpec struct {
	field Field
	file  bool // --<field>-file
	hint  string
}

// specs builds the op's flag table, with aliases.
func (ix *Index) specs(o Op) (map[string]flagSpec, []Field) {
	fields := o.Fields()
	m := map[string]flagSpec{}
	for _, f := range fields {
		m[f.Flag()] = flagSpec{field: f}
	}
	for _, f := range fields {
		m[f.Name] = flagSpec{field: f}
		if f.Type == "string" && f.Ref == "" && len(f.Enum) == 0 {
			if _, taken := m[f.Flag()+"-file"]; !taken {
				m[f.Flag()+"-file"] = flagSpec{field: f, file: true}
			}
		}
	}
	// A ref field answers to its kind: --project for the project id.
	for _, f := range fields {
		if f.Ref == "" {
			continue
		}
		if _, taken := m[f.Ref]; !taken {
			m[f.Ref] = flagSpec{field: f}
		}
	}
	// Context hints for kinds the op has no field for.
	for k := range ix.refKinds() {
		if _, taken := m[k]; !taken {
			m[k] = flagSpec{hint: k}
		}
	}
	return m, fields
}

// refKinds lists the kinds of object a name can be looked up for: those with a
// list op in the schema.
func (ix *Index) refKinds() map[string]Op {
	out := map[string]Op{}
	for _, o := range ix.Schema.Ops {
		ws := o.Words()
		if len(ws) == 2 && ws[1] == "list" && o.Method == "GET" {
			out[ws[0]] = o
		}
	}
	return out
}

// Parse reads a command line against the schema. It does no I/O.
func (ix *Index) Parse(args []string) (*Invocation, error) {
	var g Global
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") && args[i] != "-" && args[i] != "--" {
		n, err := parseGlobalAt(&g, args, i)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, usagef("unknown option %s before a command", args[i])
		}
		i += n
	}
	words := args[i:]
	op, rest, ok := ix.Match(words)
	if !ok {
		return nil, ix.unknownCommand(words)
	}
	inv := &Invocation{Op: op, Global: g, Values: map[string][]string{}, FileFlags: map[string]string{}, Hints: map[string]string{}}
	specs, fields := ix.specs(op)
	inv.Fields = fields
	var positional []string
	for j := 0; j < len(rest); j++ {
		a := rest[j]
		if a == "--" {
			positional = append(positional, rest[j+1:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		short := !strings.HasPrefix(a, "--")
		if short && len(name) != 1 {
			return nil, usagef("unknown option %s", a)
		}
		spec, isOp := specs[name]
		if short {
			isOp = false
		}
		// An op field wins over a global flag of the same name.
		if isOp {
			boolean := !spec.file && spec.hint == "" && spec.field.Type == "boolean"
			if !hasVal {
				if boolean {
					val = "true"
				} else {
					if j+1 >= len(rest) || (strings.HasPrefix(rest[j+1], "--") && len(rest[j+1]) > 2) {
						return nil, usagef("--%s needs a value", name)
					}
					j++
					val = rest[j]
				}
			}
			switch {
			case spec.hint != "":
				inv.Hints[spec.hint] = val
			case spec.file:
				inv.FileFlags[spec.field.Name] = val
			default:
				inv.Values[spec.field.Name] = append(inv.Values[spec.field.Name], val)
			}
			continue
		}
		gf, isGlobal := findGlobal(name, short)
		if !isGlobal {
			return nil, &UsageError{Msg: fmt.Sprintf("unknown option %s for %q", a, op.Command()), Hint: "Run: scriobh-cli " + op.Command() + " --help"}
		}
		if !hasVal && !gf.bool {
			if j+1 >= len(rest) {
				return nil, usagef("--%s needs a value", gf.name)
			}
			j++
			val = rest[j]
		}
		if err := inv.Global.set(gf.name, val); err != nil {
			return nil, err
		}
	}
	if inv.Global.Help {
		return inv, nil
	}
	if err := inv.bindPositionals(positional); err != nil {
		return nil, err
	}
	return inv, nil
}

// parseGlobalAt parses one global flag at args[i] and returns how many
// arguments it used (0 when it is not a global flag).
func parseGlobalAt(g *Global, args []string, i int) (int, error) {
	a := args[i]
	name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
	short := !strings.HasPrefix(a, "--")
	gf, ok := findGlobal(name, short)
	if !ok {
		return 0, nil
	}
	n := 1
	if !hasVal && !gf.bool {
		if i+1 >= len(args) {
			return 0, usagef("--%s needs a value", gf.name)
		}
		val = args[i+1]
		n = 2
	}
	return n, g.set(gf.name, val)
}

// bindPositionals gives bare arguments to the path parameters in order, then to
// the first required string fields.
func (inv *Invocation) bindPositionals(pos []string) error {
	if len(pos) == 0 {
		return nil
	}
	var targets []Field
	for _, f := range inv.Fields {
		if f.Loc == "path" {
			targets = append(targets, f)
		}
	}
	for _, f := range inv.Fields {
		if f.Loc != "path" && f.Required && f.Type == "string" {
			targets = append(targets, f)
		}
	}
	// A parameter already given by flag is skipped.
	var free []Field
	for _, f := range targets {
		if len(inv.Values[f.Name]) == 0 && inv.FileFlags[f.Name] == "" {
			free = append(free, f)
		}
	}
	if len(pos) > len(free) {
		return &UsageError{Msg: fmt.Sprintf("%q takes %d positional argument(s); extra: %q", inv.Op.Command(), len(free), strings.Join(pos[len(free):], " ")),
			Hint: "Quote values that contain spaces. Run: scriobh-cli " + inv.Op.Command() + " --help"}
	}
	for i, v := range pos {
		inv.Values[free[i].Name] = []string{v}
	}
	return nil
}

// unknownCommand explains an unmatched command with the nearest commands.
func (ix *Index) unknownCommand(words []string) error {
	if len(words) == 0 {
		return &UsageError{Msg: "no command given", Hint: "Run: scriobh-cli ops   to list the commands this server offers."}
	}
	var near []string
	seen := map[string]bool{}
	for _, o := range ix.SortedOps() {
		if strings.EqualFold(o.Group(), words[0]) || (len(words[0]) >= 3 && strings.HasPrefix(o.Group(), words[0][:3])) {
			if !seen[o.Command()] {
				near = append(near, o.Command())
				seen[o.Command()] = true
			}
		}
	}
	if len(near) > 8 {
		near = near[:8]
	}
	msg := fmt.Sprintf("unknown command %q", strings.Join(words, " "))
	hint := "Run: scriobh-cli ops   to list the commands this server offers."
	if len(near) > 0 {
		hint = "Did you mean: " + strings.Join(near, ", ") + "?"
	}
	return &UsageError{Msg: msg, Hint: hint}
}

// Missing lists required inputs that have no value, by flag spelling. dataKeys
// are the fields the --data body supplies.
func (inv *Invocation) Missing(dataKeys map[string]bool) []string {
	var out []string
	for _, f := range inv.Fields {
		if !f.Required {
			continue
		}
		if len(inv.Values[f.Name]) > 0 || inv.FileFlags[f.Name] != "" || dataKeys[f.Name] {
			continue
		}
		out = append(out, "--"+f.Flag())
	}
	sort.Strings(out)
	return out
}

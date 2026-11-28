package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Main runs the CLI and returns its exit code. Output on stdout is for
// programs (a JSON envelope), human text goes to stderr or, on a terminal,
// replaces the envelope; errors are one JSON object on stderr, or readable
// text on a terminal.
func Main(rt *Runtime, args []string) int {
	ctx := context.Background()
	if err := (&app{rt: rt, ctx: ctx}).run(args); err != nil {
		return reportError(rt, err, wantsJSON(args))
	}
	return ExitOK
}

type app struct {
	rt  *Runtime
	ctx context.Context
	g   Global
	// exit is set by a command that succeeds with a non-zero code.
	exit int
}

// exitCode is returned through the error path for a success that still wants a
// non-zero exit (a dry run under --fail-on-change); it prints nothing.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func wantsJSON(args []string) bool {
	for _, a := range args {
		if a == "--json" || a == "--json=true" {
			return true
		}
		if a == "--" {
			break
		}
	}
	return false
}

// reportError prints an error and returns its exit code.
func reportError(rt *Runtime, err error, forceJSON bool) int {
	if ec, ok := err.(exitCode); ok {
		return int(ec)
	}
	var f *Fail
	switch e := err.(type) {
	case *Fail:
		f = e
	case *UsageError:
		f = usageFail(e)
	default:
		f = &Fail{Code: "error", Message: err.Error(), Exit: ExitGeneric}
	}
	if f.Exit == 0 {
		f.Exit = ExitGeneric
	}
	if rt.IsTTY != nil && rt.IsTTY(rt.Stderr) && !forceJSON {
		fmt.Fprintf(rt.Stderr, "error: %s\n", f.Message)
		for _, c := range f.Candidates {
			p := c.Path
			if p == "" {
				p = c.Label
			}
			fmt.Fprintf(rt.Stderr, "  %s  %s\n", c.ID, p)
		}
		if f.Hint != "" {
			fmt.Fprintf(rt.Stderr, "%s\n", f.Hint)
		}
		if f.RetryAfter > 0 {
			fmt.Fprintf(rt.Stderr, "Retry after %d seconds.\n", f.RetryAfter)
		}
		return f.Exit
	}
	b, _ := json.Marshal(map[string]any{"ok": false, "error": f, "exit": f.Exit})
	fmt.Fprintf(rt.Stderr, "%s\n", b)
	return f.Exit
}

// prescan reads the flags that decide which server to ask, before the schema
// (and so the op's own flags) is known.
func prescan(args []string) Global {
	var g Global
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "--") {
			continue
		}
		switch name {
		case "server", "key", "config", "timeout", "retries":
			if !hasVal && i+1 < len(args) {
				val = args[i+1]
				i++
			}
			_ = g.set(name, val)
		case "no-cache":
			g.NoCache = true
		}
	}
	return g
}

func (a *app) run(args []string) error {
	if len(args) == 0 {
		a.printHelp()
		return nil
	}
	a.g = prescan(args)
	// Builtins that never need the schema.
	first := ""
	if r := stripLeadingGlobals(args); len(r) > 0 {
		first = r[0]
	}
	for _, x := range args {
		switch x {
		case "--skill":
			return a.skillFlag(args)
		case "--version":
			fmt.Fprintf(a.rt.Stdout, "scriobh-cli %s\n", Version)
			return nil
		}
		if x == "--" {
			break
		}
	}
	switch first {
	case "version":
		fmt.Fprintf(a.rt.Stdout, "scriobh-cli %s\n", Version)
		return nil
	case "help":
		a.printHelp()
		return nil
	case "login", "logout", "config":
		return a.account(first, args)
	case "completion":
		return a.completion(args)
	case "mcp":
		return a.mcpCmd(args)
	}
	if first == "" || first == "-h" || first == "--help" {
		a.printHelp()
		return nil
	}

	schema, src := a.rt.LoadSchema(a.ctx, a.g, false)
	ix := NewIndex(schema)
	builtinOps := map[string]bool{"ops": true, "schema": true, "whoami": true, "skill": true}
	if builtinOps[first] {
		return a.builtin(first, args, ix, src)
	}
	inv, err := ix.Parse(args)
	if ue, ok := err.(*UsageError); ok && strings.HasPrefix(ue.Msg, "unknown command") && src != SourceLive {
		// The server may have grown a command since the schema was saved.
		if s2, src2 := a.rt.LoadSchema(a.ctx, a.g, true); src2 == SourceLive {
			ix = NewIndex(s2)
			inv, err = ix.Parse(args)
		}
	}
	if err != nil {
		return err
	}
	pre := a.g
	a.g = inv.Global
	if a.g.Server == "" {
		a.g.Server = pre.Server
	}
	if a.g.Key == "" {
		a.g.Key = pre.Key
	}
	if a.g.Config == "" {
		a.g.Config = pre.Config
	}
	if a.g.Timeout == "" {
		a.g.Timeout = pre.Timeout
	}
	if !a.g.retriesSet {
		a.g.Retries, a.g.retriesSet = pre.Retries, pre.retriesSet
	}
	if a.g.Help {
		a.opHelp(inv.Op, ix)
		return nil
	}
	return a.execute(ix, inv)
}

// execute runs one op.
func (a *app) execute(ix *Index, inv *Invocation) error {
	op := inv.Op
	g := a.g
	if err := inv.Preflight(); err != nil {
		return err
	}
	file := isFileOp(op)
	if err := inv.Validate(); err != nil {
		return err
	}
	cl, err := a.rt.NewClient(g)
	if err != nil {
		return err
	}
	if cl.Key == "" {
		return &Fail{Code: "no_credentials", Exit: ExitAuth, Message: "No API key is configured.",
			Hint: "Run: scriobh-cli login --url " + cl.Base.String() + " --key scr_…   (or set SCRIOBH_API_KEY). Create a key in Settings, or with: scriobh-cli key create"}
	}
	if cl.Base.Scheme == "http" && !isLoopback(cl.Base) && !g.Quiet {
		fmt.Fprintf(a.rt.Stderr, "warning: %s is not https; the key travels in clear text.\n", cl.Base.Host)
	}
	resolved, notes, err := ResolveNames(a.ctx, cl, ix, inv)
	if err != nil {
		return err
	}
	req, err := Bind(inv, a.rt.Stdin, resolved)
	if err != nil {
		return err
	}
	hdr := map[string]string{}
	if req.ContentType != "" {
		hdr["Content-Type"] = req.ContentType
	}
	if g.DryRun {
		hdr["Prefer"] = "dry-run"
		hdr["X-Scriobh-Dry-Run"] = "1"
	} else if req.Op.Mutates() && !g.NoIdempotency && !req.Op.RawBody {
		hdr["Idempotency-Key"] = firstNonEmpty(g.IdempotencyKey, newIdempotencyKey())
	}
	if g.IdempotencyKey != "" && req.Op.Mutates() && !g.DryRun {
		hdr["Idempotency-Key"] = g.IdempotencyKey
	}
	resp, pages, err := a.send(cl, req, hdr)
	if err != nil {
		return err
	}
	meta := map[string]any{}
	if len(notes) > 0 {
		meta["resolved"] = notes
	}
	if resp.Header.Get("Idempotent-Replay") == "true" {
		meta["idempotent_replay"] = true
	}
	if pages > 1 {
		meta["pages"] = pages
	}
	if file && !g.DryRun {
		return a.writeFile(op, resp, meta)
	}
	var data any
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return &Fail{Code: "bad_response", Exit: ExitGeneric, Status: resp.Status, Message: "The server answered 200 with something that is not JSON.",
			Hint: "Is --server the address of a Scríobh server?"}
	}
	wouldChange := false
	if m, ok := data.(map[string]any); ok {
		if d, _ := m["dry_run"].(bool); d {
			meta["dry_run"] = true
			wouldChange, _ = m["would_change"].(bool)
			meta["would_change"] = wouldChange
		}
	}
	a.printResult(op, resp.Status, data, meta, notes)
	if g.DryRun && g.FailOnChange && wouldChange {
		return exitCode(ExitWouldChange)
	}
	return nil
}

// send performs the request, following next_cursor under --all.
func (a *app) send(cl *Client, req *Request, hdr map[string]string) (*Response, int, error) {
	do := func(q url.Values) (*Response, error) {
		resp, err := cl.Do(a.ctx, req.Method, req.Path, q, req.Body, hdr)
		if err != nil {
			return nil, err
		}
		if resp.Status >= 400 {
			return nil, fromServer(resp.Status, resp.Header, resp.Body)
		}
		return resp, nil
	}
	first, err := do(req.Query)
	if err != nil {
		return nil, 0, err
	}
	if !a.g.All || !canPage(req.Op) {
		return first, 1, nil
	}
	maxPages := a.g.MaxPages
	if maxPages == 0 {
		maxPages = 100
	}
	var merged map[string]any
	if err := json.Unmarshal(first.Body, &merged); err != nil {
		return first, 1, nil
	}
	items, _ := merged["items"].([]any)
	pages := 1
	for pages < maxPages {
		next, _ := merged["next_cursor"].(string)
		if next == "" {
			break
		}
		q := url.Values{}
		for k, v := range req.Query {
			q[k] = v
		}
		q.Set("cursor", next)
		resp, err := do(q)
		if err != nil {
			return nil, pages, err
		}
		var page map[string]any
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			break
		}
		more, _ := page["items"].([]any)
		items = append(items, more...)
		merged["next_cursor"] = page["next_cursor"]
		pages++
	}
	merged["items"] = items
	if n, _ := merged["next_cursor"].(string); n == "" {
		delete(merged, "next_cursor")
	}
	b, _ := json.Marshal(merged)
	first.Body = b
	return first, pages, nil
}

// canPage says whether an op takes a cursor and answers with next_cursor.
func canPage(o Op) bool {
	hasCursor := false
	for _, f := range o.Fields() {
		hasCursor = hasCursor || (f.Name == "cursor" && f.Loc == "query")
	}
	props, _ := o.Output["properties"].(map[string]any)
	_, hasNext := props["next_cursor"]
	return hasCursor && hasNext
}

// isFileOp says whether an op's REST route answers with a file.
func isFileOp(o Op) bool {
	props, _ := o.Output["properties"].(map[string]any)
	_, body := props["body"]
	_, name := props["filename"]
	_, ctype := props["content_type"]
	if body && name && ctype {
		return true
	}
	return false
}

// writeFile saves a file response.
func (a *app) writeFile(op Op, resp *Response, meta map[string]any) error {
	ct := resp.Header.Get("Content-Type")
	if mt, _, err := mime.ParseMediaType(ct); err == nil && mt == "application/json" {
		// The server answered with JSON where a file was expected (an error was
		// handled earlier): pass it through.
		var data any
		_ = json.Unmarshal(resp.Body, &data)
		a.printResult(op, resp.Status, data, meta, nil)
		return nil
	}
	name := "download"
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		name = filepath.Base(params["filename"])
	}
	if a.g.Output == "-" {
		_, err := a.rt.Stdout.Write(resp.Body)
		return err
	}
	dest := a.g.Output
	if st, err := os.Stat(dest); err == nil && st.IsDir() {
		dest = filepath.Join(dest, name)
	}
	if err := os.WriteFile(dest, resp.Body, 0o644); err != nil {
		return &Fail{Code: "write_failed", Exit: ExitGeneric, Message: "Cannot write " + dest + ": " + errText(err)}
	}
	a.printResult(op, resp.Status, map[string]any{"file": dest, "filename": name, "content_type": ct, "bytes": len(resp.Body)}, meta, nil)
	return nil
}

// printResult writes the success output: the envelope for programs, the data
// alone on a terminal.
func (a *app) printResult(op Op, status int, data any, meta map[string]any, notes []Resolved) {
	tty := a.rt.IsTTY != nil && a.rt.IsTTY(a.rt.Stdout)
	if tty && !a.g.JSON {
		b, _ := json.MarshalIndent(data, "", "  ")
		fmt.Fprintf(a.rt.Stdout, "%s\n", b)
		if !a.g.Quiet {
			for _, n := range notes {
				fmt.Fprintf(a.rt.Stderr, "%s %q is %s\n", n.Kind, n.Input, n.ID)
			}
			if d, _ := meta["dry_run"].(bool); d {
				fmt.Fprintf(a.rt.Stderr, "dry run: nothing was changed (would_change=%v)\n", meta["would_change"])
			}
		}
		return
	}
	env := map[string]any{"ok": true, "op": op.Name, "status": status, "data": data}
	if len(meta) > 0 {
		env["meta"] = meta
	}
	a.writeJSON(env)
}

func (a *app) writeJSON(v any) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	_, _ = a.rt.Stdout.Write(b.Bytes())
}

// ---- builtin commands ---------------------------------------------------------

func (a *app) builtin(name string, args []string, ix *Index, src SchemaSource) error {
	// Drop the leading globals and the command word.
	rest := stripLeadingGlobals(args)
	if len(rest) > 0 {
		rest = rest[1:]
	}
	switch name {
	case "ops":
		return a.opsCmd(rest, ix, src)
	case "schema":
		a.writeJSON(map[string]any{"ok": true, "op": "schema", "source": src, "data": ix.Schema})
		return nil
	case "whoami":
		cl, err := a.rt.NewClient(a.g)
		if err != nil {
			return err
		}
		resp, err := cl.Do(a.ctx, "GET", "/api/v1/_whoami", nil, nil, nil)
		if err != nil {
			return err
		}
		if resp.Status >= 400 {
			return fromServer(resp.Status, resp.Header, resp.Body)
		}
		var data any
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return &Fail{Code: "bad_response", Exit: ExitGeneric, Status: resp.Status, Message: "The server answered 200 with something that is not JSON.",
				Hint: "Is --server the address of a Scríobh server?"}
		}
		a.printResult(Op{Name: "whoami"}, resp.Status, data, nil, nil)
		return nil
	case "skill":
		return a.skillCmd(rest, ix, src)
	}
	return nil
}

func stripLeadingGlobals(args []string) []string {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") && args[i] != "-" && args[i] != "--" {
		var g Global
		n, err := parseGlobalAt(&g, args, i)
		if err != nil || n == 0 {
			break
		}
		i += n
	}
	return args[i:]
}

func (a *app) opsCmd(rest []string, ix *Index, src SchemaSource) error {
	group := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		group = rest[0]
	}
	type row struct {
		Command  string `json:"command"`
		Op       string `json:"op"`
		Method   string `json:"method"`
		Path     string `json:"path"`
		Scope    string `json:"scope"`
		Mutates  bool   `json:"mutates"`
		NoDryRun bool   `json:"no_dry_run,omitempty"`
		Desc     string `json:"desc"`
	}
	var rows []row
	for _, o := range ix.SortedOps() {
		if group != "" && o.Group() != group {
			continue
		}
		rows = append(rows, row{o.Command(), o.Name, o.Method, o.Path, o.Scope, o.Mutates(), o.NoDryRun, o.Desc})
	}
	tty := a.rt.IsTTY != nil && a.rt.IsTTY(a.rt.Stdout)
	if tty && !a.g.JSON {
		w := 0
		for _, r := range rows {
			if len(r.Command) > w {
				w = len(r.Command)
			}
		}
		for _, r := range rows {
			fmt.Fprintf(a.rt.Stdout, "%-*s  %s\n", w, r.Command, firstSentence(r.Desc))
		}
		return nil
	}
	a.writeJSON(map[string]any{"ok": true, "op": "ops", "data": map[string]any{"schema_hash": ix.Schema.Hash, "source": src, "ops": rows}})
	return nil
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

// printHelp is the top-level help.
func (a *app) printHelp() {
	schema, src := a.rt.LoadSchema(a.ctx, a.g, false)
	ix := NewIndex(schema)
	w := a.rt.Stdout
	fmt.Fprintf(w, "scriobh-cli: the Scríobh command line for scripts and agents\n\n")
	fmt.Fprintf(w, "usage: scriobh-cli [options] <group> <verb> [arguments] [flags]\n\n")
	fmt.Fprintf(w, "Every operation the server offers is a command. Commands come from the server's\nschema (%s, %d operations):\n\n", src, len(schema.Ops))
	groups := ix.Groups()
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		var verbs []string
		for _, o := range groups[n] {
			verbs = append(verbs, strings.Join(o.Words()[1:], " "))
		}
		sort.Strings(verbs)
		fmt.Fprintf(w, "  %-10s %s\n", n, strings.Join(verbs, ", "))
	}
	fmt.Fprintf(w, "\nOther commands:\n")
	fmt.Fprintf(w, "  login, logout, config   save, remove or show the server and key\n")
	fmt.Fprintf(w, "  whoami                  who the key acts as, its scope and restriction\n")
	fmt.Fprintf(w, "  ops [group], schema     list the commands, or print the whole schema\n")
	fmt.Fprintf(w, "  skill show|install|uninstall|check, --skill [recipe]\n")
	fmt.Fprintf(w, "  mcp [--groups a,b] [--read-only] [--permanent]   serve the Model Context Protocol over stdio\n")
	fmt.Fprintf(w, "  completion bash|zsh|fish\n")
	fmt.Fprintf(w, "\nOptions (anywhere on the line):\n")
	for _, g := range globalFlags {
		name := "--" + g.name
		if g.short != "" {
			name = "-" + g.short + ", " + name
		}
		fmt.Fprintf(w, "  %-22s %s\n", name, g.help)
	}
	fmt.Fprintf(w, "\nExit codes: ")
	for i, e := range ExitTable {
		if i > 0 {
			fmt.Fprint(w, ", ")
		}
		fmt.Fprintf(w, "%d %s", e.Code, e.Name)
	}
	fmt.Fprintf(w, "\n\nRun `scriobh-cli <command> --help` for a command's flags, `scriobh-cli --skill` for the agent skill.\n")
}

// opHelp prints one command's usage.
func (a *app) opHelp(o Op, ix *Index) {
	w := a.rt.Stdout
	fmt.Fprintf(w, "scriobh-cli %s\n\n%s\n\n", o.Command(), o.Desc)
	fmt.Fprintf(w, "usage: %s\n\n", UsageLine(o))
	fmt.Fprintf(w, "needs: %s scope; %s\n", o.Scope, authText(o.Auth))
	if o.Mutates() {
		if o.NoDryRun {
			fmt.Fprintf(w, "dry run: not supported\n")
		} else {
			fmt.Fprintf(w, "dry run: supported (--dry-run)\n")
		}
		if destructive(o) {
			fmt.Fprintf(w, "destructive: needs --yes\n")
		}
	}
	fs := o.Fields()
	if len(fs) > 0 {
		fmt.Fprintf(w, "\nflags:\n")
		for _, f := range fs {
			fmt.Fprintf(w, "  %s\n", FlagHelp(f))
		}
	}
	fmt.Fprintf(w, "\nGlobal options such as --json, --dry-run and --server apply too; see `scriobh-cli help`.\n")
}

func authText(a Auth) string {
	switch a.Kind {
	case "project":
		return "project " + a.Role + " role"
	case "authenticated":
		return "any signed-in key"
	}
	return a.Kind + " role"
}

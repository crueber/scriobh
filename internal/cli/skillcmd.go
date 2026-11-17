package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// skillFlag handles `scriobh-cli --skill [recipe]`.
func (a *app) skillFlag(args []string) error {
	recipe := ""
	for i, x := range args {
		if x == "--skill" && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			recipe = args[i+1]
		}
	}
	schema, _ := a.rt.LoadSchema(a.ctx, a.g, false)
	return a.printSkill(schema, recipe)
}

func (a *app) printSkill(schema Schema, recipe string) error {
	if recipe == "" || recipe == "full" {
		fmt.Fprint(a.rt.Stdout, RenderSkill(schema, SkillOptions{}))
		return nil
	}
	text, ok := Recipe(schema, recipe)
	if !ok {
		return &UsageError{Msg: fmt.Sprintf("no recipe named %q", recipe), Hint: "Recipes: " + strings.Join(RecipeNames, ", ") + "."}
	}
	fmt.Fprint(a.rt.Stdout, text)
	return nil
}

// skillCmd handles `scriobh-cli skill show|install|uninstall|check`.
func (a *app) skillCmd(rest []string, ix *Index, src SchemaSource) error {
	if len(rest) == 0 {
		return &UsageError{Msg: "skill needs show, install, uninstall or check", Hint: "Example: scriobh-cli skill install --agent claude"}
	}
	verb, rest := rest[0], rest[1:]
	opt, err := parseSkillFlags(&a.g, rest)
	if err != nil {
		return err
	}
	switch verb {
	case "show":
		recipe := ""
		if len(opt.positional) > 0 {
			recipe = opt.positional[0]
		}
		return a.printSkill(ix.Schema, recipe)
	case "install":
		return a.skillInstall(ix.Schema, opt)
	case "uninstall":
		return a.skillUninstall(opt)
	case "check":
		return a.skillCheck(ix.Schema, src, opt)
	}
	return &UsageError{Msg: fmt.Sprintf("unknown skill command %q", verb), Hint: "Use show, install, uninstall or check."}
}

type skillOpts struct {
	agents     []string
	dir        string
	global     bool
	force      bool
	file       string
	positional []string
}

func parseSkillFlags(g *Global, rest []string) (skillOpts, error) {
	var o skillOpts
	for i := 0; i < len(rest); i++ {
		x := rest[i]
		if !strings.HasPrefix(x, "-") || x == "-" {
			o.positional = append(o.positional, x)
			continue
		}
		n, val, hasVal := strings.Cut(strings.TrimLeft(x, "-"), "=")
		need := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(rest) {
				return "", usagef("--%s needs a value", n)
			}
			i++
			return rest[i], nil
		}
		switch n {
		case "agent":
			v, err := need()
			if err != nil {
				return o, err
			}
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					o.agents = append(o.agents, strings.ToLower(p))
				}
			}
		case "dir":
			v, err := need()
			if err != nil {
				return o, err
			}
			o.dir = v
		case "file":
			v, err := need()
			if err != nil {
				return o, err
			}
			o.file = v
		case "global":
			o.global = true
		case "force":
			o.force = true
		default:
			ga, isGlobal := findGlobal(n, !strings.HasPrefix(x, "--"))
			if !isGlobal {
				return o, usagef("unknown option %s for skill", x)
			}
			v := val
			if !hasVal && !ga.bool {
				var err error
				if v, err = need(); err != nil {
					return o, err
				}
			}
			if err := g.set(ga.name, v); err != nil {
				return o, err
			}
		}
	}
	return o, nil
}

// ---- where a skill goes --------------------------------------------------------

type target struct {
	agent  string
	rel    string // relative to the project directory
	global string // relative to the home directory; empty when the agent has none
	kind   string // "file" (Scríobh owns the whole file) or "block" (a marked block in a shared file)
	// detect lists paths relative to the base whose presence suggests the agent.
	detect []string
}

var targets = []target{
	{"claude", ".claude/skills/scriobh/SKILL.md", ".claude/skills/scriobh/SKILL.md", "file", []string{".claude", "CLAUDE.md"}},
	{"codex", "AGENTS.md", ".codex/AGENTS.md", "block", []string{".codex"}},
	{"opencode", "AGENTS.md", ".config/opencode/AGENTS.md", "block", []string{".opencode", "opencode.json", "opencode.jsonc"}},
	{"agents", "AGENTS.md", "", "block", []string{"AGENTS.md"}},
	{"cursor", ".cursor/rules/scriobh.mdc", "", "file", []string{".cursor"}},
	{"gemini", "GEMINI.md", ".gemini/GEMINI.md", "block", []string{"GEMINI.md", ".gemini"}},
	{"windsurf", ".windsurf/rules/scriobh.md", "", "file", []string{".windsurf", ".windsurfrules"}},
	{"copilot", ".github/copilot-instructions.md", "", "block", []string{".github/copilot-instructions.md"}},
}

func agentNames() []string {
	var out []string
	for _, t := range targets {
		out = append(out, t.agent)
	}
	return out
}

func findTarget(agent string) (target, bool) {
	for _, t := range targets {
		if t.agent == agent {
			return t, true
		}
	}
	return target{}, false
}

// base resolves the directory installs go under: the project directory (or
// --dir, or the working directory), or the home directory with --global.
func (a *app) skillBase(o skillOpts) (string, error) {
	if o.global {
		h := a.rt.home()
		if h == "" {
			return "", &Fail{Code: "no_home", Exit: ExitUsage, Message: "Cannot find the home directory for --global."}
		}
		return filepath.Clean(h), nil
	}
	d := o.dir
	if d == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		d = wd
	}
	abs, err := filepath.Abs(d)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return "", usagef("%s is not a directory", d)
	}
	return abs, nil
}

// path returns where a target lives under base, refusing a path that would
// leave base or run through a symbolic link.
func (t target) path(base string, global bool, override string) (string, error) {
	rel := t.rel
	if global {
		rel = t.global
	}
	if override != "" {
		rel = override
	}
	if rel == "" {
		return "", usagef("%s has no global instruction file; install it in a project with --dir", t.agent)
	}
	var full string
	if filepath.IsAbs(rel) {
		full = filepath.Clean(rel)
	} else {
		full = filepath.Join(base, rel)
	}
	r, err := filepath.Rel(base, full)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", usagef("%s is outside %s; installs stay inside the directory you name", rel, base)
	}
	// No component under base may be a symbolic link.
	cur := base
	for _, part := range strings.Split(r, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		if st, err := os.Lstat(cur); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", usagef("%s is a symbolic link; refusing to write through it", cur)
		}
	}
	return full, nil
}

// detectAgents finds the agents whose files exist under base.
func detectAgents(base string) []string {
	var out []string
	seenFile := map[string]bool{}
	for _, t := range targets {
		hit := false
		for _, d := range t.detect {
			if _, err := os.Lstat(filepath.Join(base, d)); err == nil {
				hit = true
			}
		}
		if !hit {
			continue
		}
		// agents is the generic AGENTS.md owner: skip it when codex or opencode already claim the file.
		if t.agent == "agents" && (contains(out, "codex") || contains(out, "opencode")) {
			continue
		}
		if t.kind == "block" && seenFile[t.rel] && t.agent != "codex" && t.agent != "opencode" {
			continue
		}
		seenFile[t.rel] = true
		out = append(out, t.agent)
	}
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// ---- rendering what is written ---------------------------------------------------

const (
	blockBegin = "<!-- scriobh:skill:begin"
	blockEnd   = "<!-- scriobh:skill:end -->"
	fileMark   = "<!-- scriobh:skill:file"
)

var markAttrs = regexp.MustCompile(`sha256=([0-9a-f]{64}) schema=(sha256:[0-9a-f]{64}|unknown)`)

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// stripFrontmatter removes a leading --- block.
func stripFrontmatter(s string) string {
	if strings.HasPrefix(s, "---\n") {
		if i := strings.Index(s[4:], "\n---\n"); i >= 0 {
			return strings.TrimLeft(s[4+i+5:], "\n")
		}
	}
	return s
}

// content builds what a target holds: the whole file for a file target, the
// block body for a block target.
func (t target) content(s Schema) string {
	switch t.kind {
	case "block":
		return stripFrontmatter(RenderSkill(s, SkillOptions{Compact: true}))
	}
	full := RenderSkill(s, SkillOptions{})
	switch t.agent {
	case "cursor":
		return "---\ndescription: Work in a Scríobh writing instance through scriobh-cli\nalwaysApply: false\n---\n\n" + stripFrontmatter(full)
	case "windsurf":
		return "---\ntrigger: model_decision\ndescription: Work in a Scríobh writing instance through scriobh-cli\n---\n\n" + stripFrontmatter(full)
	}
	return full
}

func schemaMark(s Schema) string {
	if s.Hash == "" {
		return "unknown"
	}
	return s.Hash
}

// wrapFile appends the footer that proves Scríobh wrote a file target.
func wrapFile(body string, s Schema) string {
	return body + fmt.Sprintf("\n%s sha256=%s schema=%s -->\n", fileMark, sha(body), schemaMark(s))
}

// wrapBlock marks a block's body with its hash.
func wrapBlock(body string, s Schema) string {
	return fmt.Sprintf("%s sha256=%s schema=%s -->\n%s\n%s\n", blockBegin, sha(body), schemaMark(s), body, blockEnd)
}

// installed is what a file currently holds of ours.
type installed struct {
	Present   bool
	Edited    bool // the content no longer matches its recorded hash
	Schema    string
	Hash      string // the recorded hash
	start     int    // byte range of the whole block (block kind)
	end       int
	body      string
	foreign   bool // a file target that Scríobh did not write
	fileBody  string
	fileExist bool
}

// inspect reads a target file and reports what of ours it holds.
func (t target) inspect(path string) (installed, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return installed{}, nil
	}
	if err != nil {
		return installed{}, err
	}
	s := string(b)
	in := installed{fileExist: true}
	if t.kind == "file" {
		i := strings.LastIndex(s, fileMark)
		if i < 0 {
			in.foreign = true
			return in, nil
		}
		m := markAttrs.FindStringSubmatch(s[i:])
		if m == nil {
			in.foreign = true
			return in, nil
		}
		body := strings.TrimSuffix(s[:i], "\n")
		in.Present, in.Hash, in.Schema, in.body, in.fileBody = true, m[1], m[2], body, s
		in.Edited = sha(body) != m[1]
		return in, nil
	}
	i := strings.Index(s, blockBegin)
	if i < 0 {
		return in, nil
	}
	eol := strings.Index(s[i:], "\n")
	j := strings.Index(s[i:], blockEnd)
	if eol < 0 || j < 0 || j < eol {
		in.Present, in.Edited = true, true // markers damaged
		in.start, in.end = i, i
		return in, nil
	}
	head := s[i : i+eol]
	body := s[i+eol+1 : i+j]
	body = strings.TrimSuffix(body, "\n")
	m := markAttrs.FindStringSubmatch(head)
	in.Present = true
	in.start = i
	in.end = i + j + len(blockEnd)
	if in.end < len(s) && s[in.end] == '\n' {
		in.end++
	}
	in.body = body
	if m == nil {
		in.Edited = true
		return in, nil
	}
	in.Hash, in.Schema = m[1], m[2]
	in.Edited = sha(body) != m[1]
	return in, nil
}

// ---- install ------------------------------------------------------------------------

// Install is the result for one target.
type Install struct {
	Agent  string `json:"agent"`
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Action string `json:"action"` // created, updated, appended, unchanged, removed, skipped, would_*
	Reason string `json:"reason,omitempty"`
}

func (a *app) skillInstall(s Schema, o skillOpts) error {
	base, err := a.skillBase(o)
	if err != nil {
		return err
	}
	agents, err := a.chooseAgents(o, base, true)
	if err != nil {
		return err
	}
	var results []Install
	var refused []string
	for _, ag := range agents {
		t, _ := findTarget(ag)
		path, err := t.path(base, o.global, o.fileFor(t))
		if err != nil {
			return err
		}
		r := Install{Agent: ag, Path: path, Kind: t.kind}
		in, err := t.inspect(path)
		if err != nil {
			return &Fail{Code: "read_failed", Exit: ExitGeneric, Message: "Cannot read " + path + ": " + errText(err)}
		}
		body := t.content(s)
		switch {
		case in.foreign && !o.force:
			r.Action, r.Reason = "refused", "the file exists and was not written by scriobh-cli; use --force to replace it"
			refused = append(refused, path)
		case in.Present && in.Edited && !o.force:
			r.Action, r.Reason = "refused", "the installed text was edited by hand; use --force to overwrite your edits"
			refused = append(refused, path)
		default:
			r.Action = a.writeTarget(t, path, in, body, s)
		}
		results = append(results, r)
	}
	a.printResult(Op{Name: "skill.install"}, 200, map[string]any{"base": base, "dry_run": a.g.DryRun, "installs": results}, nil, nil)
	if len(refused) > 0 {
		return &Fail{Code: "refused", Exit: ExitConflict, Message: fmt.Sprintf("Left %d file(s) alone: %s", len(refused), strings.Join(refused, ", ")),
			Hint: "Nothing you wrote was overwritten. Review the file, or repeat with --force."}
	}
	return nil
}

func (o skillOpts) fileFor(t target) string {
	if o.file != "" && t.kind == "block" {
		return o.file
	}
	return ""
}

// chooseAgents resolves --agent, or detects agents, or uses --file.
func (a *app) chooseAgents(o skillOpts, base string, install bool) ([]string, error) {
	if len(o.agents) == 0 && o.file != "" {
		return []string{"agents"}, nil
	}
	if len(o.agents) == 1 && o.agents[0] == "all" {
		return agentNames(), nil
	}
	for _, ag := range o.agents {
		if _, ok := findTarget(ag); !ok {
			return nil, &UsageError{Msg: fmt.Sprintf("unknown agent %q", ag), Hint: "Agents: " + strings.Join(agentNames(), ", ") + ", or all. Use --file PATH for any other tool."}
		}
	}
	if len(o.agents) > 0 {
		return o.agents, nil
	}
	if o.global {
		var out []string
		for _, t := range targets {
			if t.global == "" {
				continue
			}
			if _, err := os.Lstat(filepath.Join(base, filepath.Dir(t.global))); err == nil && filepath.Dir(t.global) != "." {
				out = append(out, t.agent)
			}
		}
		if len(out) > 0 {
			return out, nil
		}
	} else if out := detectAgents(base); len(out) > 0 {
		return out, nil
	}
	if !install {
		return agentNames(), nil
	}
	return nil, &UsageError{Msg: "no coding agent was found in " + base, Hint: "Say which: --agent " + strings.Join(agentNames(), "|") + ", or --file PATH. Add --global for the home directory."}
}

// writeTarget creates or updates a target and returns the action taken.
func (a *app) writeTarget(t target, path string, in installed, body string, s Schema) string {
	dry := a.g.DryRun
	var out string
	var action string
	switch t.kind {
	case "file":
		out = wrapFile(body, s)
		switch {
		case in.Present && in.fileBody == out:
			return "unchanged"
		case in.Present || in.foreign:
			action = "updated"
		default:
			action = "created"
		}
	default:
		block := wrapBlock(body, s)
		if !in.fileExist {
			out, action = block, "created"
		} else {
			cur, _ := os.ReadFile(path)
			cs := string(cur)
			if in.Present {
				out = cs[:in.start] + block + cs[in.end:]
				if out == cs {
					return "unchanged"
				}
				action = "updated"
			} else {
				sep := "\n"
				if cs != "" && !strings.HasSuffix(cs, "\n") {
					sep = "\n\n"
				} else if cs == "" {
					sep = ""
				}
				out, action = cs+sep+block, "appended"
			}
		}
	}
	if dry {
		return "would_be_" + action
	}
	if err := writeAtomic(path, out); err != nil {
		return "failed: " + errText(err)
	}
	return action
}

// writeAtomic writes through a temporary file in the same directory, keeping
// an existing file's mode.
func writeAtomic(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".scriobh-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ---- uninstall -----------------------------------------------------------------------

func (a *app) skillUninstall(o skillOpts) error {
	base, err := a.skillBase(o)
	if err != nil {
		return err
	}
	agents, err := a.chooseAgents(o, base, false)
	if err != nil {
		return err
	}
	var results []Install
	var refused []string
	done := map[string]bool{}
	for _, ag := range agents {
		t, _ := findTarget(ag)
		path, err := t.path(base, o.global, o.fileFor(t))
		if err != nil {
			continue // an agent with no place here has nothing installed
		}
		if done[path] {
			continue
		}
		in, err := t.inspect(path)
		if err != nil || !in.Present {
			continue
		}
		done[path] = true
		r := Install{Agent: ag, Path: path, Kind: t.kind}
		if in.Edited && !o.force {
			r.Action, r.Reason = "refused", "the installed text was edited by hand; use --force to remove it anyway"
			refused = append(refused, path)
			results = append(results, r)
			continue
		}
		r.Action = a.removeTarget(t, path, in)
		results = append(results, r)
	}
	a.printResult(Op{Name: "skill.uninstall"}, 200, map[string]any{"base": base, "dry_run": a.g.DryRun, "removed": results}, nil, nil)
	if len(refused) > 0 {
		return &Fail{Code: "refused", Exit: ExitConflict, Message: "Left edited file(s) alone: " + strings.Join(refused, ", "), Hint: "Repeat with --force to remove them anyway."}
	}
	return nil
}

func (a *app) removeTarget(t target, path string, in installed) string {
	if t.kind == "file" {
		if a.g.DryRun {
			return "would_be_removed"
		}
		if err := os.Remove(path); err != nil {
			return "failed: " + errText(err)
		}
		// Remove the skill's own folder when it is now empty.
		_ = os.Remove(filepath.Dir(path))
		return "removed"
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		return "failed: " + errText(err)
	}
	cs := string(cur)
	// Take back the blank line that was added in front of the block.
	prefix, suffix := strings.TrimRight(cs[:in.start], "\n"), strings.TrimLeft(cs[in.end:], "\n")
	var out string
	switch {
	case prefix == "":
		out = suffix
	case strings.TrimSpace(suffix) == "":
		out = prefix + "\n"
	default:
		out = prefix + "\n\n" + suffix
	}
	if strings.TrimSpace(out) == "" {
		if a.g.DryRun {
			return "would_be_removed"
		}
		if err := os.Remove(path); err != nil {
			return "failed: " + errText(err)
		}
		return "removed"
	}
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if a.g.DryRun {
		return "would_be_updated"
	}
	if err := writeAtomic(path, out); err != nil {
		return "failed: " + errText(err)
	}
	return "updated"
}

// ---- check ----------------------------------------------------------------------------

// skillCheck compares installed copies and this binary's snapshot with the
// live server's schema hash.
func (a *app) skillCheck(s Schema, src SchemaSource, o skillOpts) error {
	base, err := a.skillBase(o)
	if err != nil {
		return err
	}
	bases := []string{base}
	if !o.global && o.dir == "" {
		if h := a.rt.home(); h != "" && filepath.Clean(h) != base {
			bases = append(bases, filepath.Clean(h))
		}
	}
	server := a.liveHash(src)
	type found struct {
		Agent     string `json:"agent"`
		Path      string `json:"path"`
		Schema    string `json:"schema"`
		Current   bool   `json:"current"`
		HandEdits bool   `json:"hand_edited"`
	}
	var installs []found
	drift := false
	seen := map[string]bool{}
	for i, b := range bases {
		for _, t := range targets {
			path, err := t.path(b, i > 0 || o.global, "")
			if err != nil || seen[path] {
				continue
			}
			in, err := t.inspect(path)
			if err != nil || !in.Present {
				continue
			}
			seen[path] = true
			cur := server != "" && in.Schema == server
			if server == "" {
				cur = in.Schema == s.Hash
			}
			if !cur || in.Edited {
				drift = true
			}
			installs = append(installs, found{t.agent, path, in.Schema, cur, in.Edited})
		}
	}
	data := map[string]any{
		"server_schema":    server,
		"cli_snapshot":     Embedded().Hash,
		"installs":         installs,
		"schema_source":    src,
		"cli_snapshot_ok":  server == "" || Embedded().Hash == server,
		"server_reachable": server != "",
	}
	if server != "" && Embedded().Hash != server {
		drift = true
		data["cli_note"] = "this scriobh-cli was built against a different schema than the server's; the commands it knows come from the server's schema, so it still works, but upgrade it when you can"
	}
	if len(installs) == 0 {
		data["note"] = "no installed skill was found; run: scriobh-cli skill install"
	}
	data["drift"] = drift
	a.printResult(Op{Name: "skill.check"}, 200, data, nil, nil)
	if drift {
		return exitCode(ExitDrift)
	}
	return nil
}

// liveHash asks the server for its schema hash; empty when it cannot be asked.
func (a *app) liveHash(src SchemaSource) string {
	cl, err := a.rt.NewClient(a.g)
	if err != nil || cl.Key == "" {
		return ""
	}
	cl.Retries = 0
	cl.Timeout = 8e9
	resp, err := cl.Do(context.Background(), "GET", "/api/v1/_whoami", nil, nil, nil)
	if err != nil || resp.Status != 200 {
		return ""
	}
	var w struct {
		SchemaHash string `json:"schema_hash"`
	}
	if json.Unmarshal(resp.Body, &w) != nil {
		return ""
	}
	return w.SchemaHash
}

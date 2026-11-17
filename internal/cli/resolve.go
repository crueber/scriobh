package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// A name is resolved to an id on the client, through the list ops the schema
// already offers, and never by guessing: an exact match wins, then a
// case-insensitive one; more than one match is an error that lists them.
//
// Where a value is an id (prefix_26 characters) it passes through untouched,
// so scripts that hold ids pay nothing. "id:xyz" forces an id and "name:xyz"
// forces a name.

var idLike = regexp.MustCompile(`^[a-z]{2,5}_[0-9A-Za-z]{20,}$`)

// Resolved records one name that became an id, for the output envelope.
type Resolved struct {
	Field string `json:"field"`
	Kind  string `json:"kind"`
	Input string `json:"input"`
	ID    string `json:"id"`
}

type item struct {
	ID, Label, Parent string
	Aliases           []string
}

type resolver struct {
	ctx   context.Context
	cl    *Client
	ix    *Index
	scope map[string]string
	cache map[string][]item
	who   *whoamiInfo
	out   []Resolved
}

type whoamiInfo struct {
	Project *struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"project"`
	// Compendium is the one a pinned key works in (its own pin, or its
	// project's primary).
	Compendium *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"compendium"`
}

// scopeKinds are the kinds that other kinds are listed within: a document is
// looked up inside a project, an entity inside a compendium.
func (ix *Index) scopeKinds() map[string]bool {
	out := map[string]bool{}
	for _, o := range ix.refKinds() {
		for _, f := range o.Fields() {
			if f.Loc == "path" && f.Ref != "" {
				out[f.Ref] = true
			}
		}
	}
	return out
}

// ResolveNames turns every name the invocation carries into an id. The result
// maps a field's wire name to its id.
func ResolveNames(ctx context.Context, cl *Client, ix *Index, inv *Invocation) (map[string]string, []Resolved, error) {
	r := &resolver{ctx: ctx, cl: cl, ix: ix, scope: map[string]string{}, cache: map[string][]item{}}
	scopes := ix.scopeKinds()
	// Context flags (--project NAME) name the scope for everything else.
	hintKinds := make([]string, 0, len(inv.Hints))
	for k := range inv.Hints {
		hintKinds = append(hintKinds, k)
	}
	sort.Strings(hintKinds)
	for _, k := range hintKinds {
		id, err := r.resolve(k, inv.Hints[k])
		if err != nil {
			return nil, nil, err
		}
		r.scope[k] = id
	}
	var refs []Field
	for _, f := range inv.Fields {
		if f.Ref != "" && len(inv.Values[f.Name]) == 1 && inv.Values[f.Name][0] != "" && inv.FileFlags[f.Name] == "" {
			refs = append(refs, f)
		}
	}
	// Scope kinds first, so a project named in the same command scopes the rest.
	sort.SliceStable(refs, func(i, j int) bool { return scopes[refs[i].Ref] && !scopes[refs[j].Ref] })
	resolved := map[string]string{}
	for _, f := range refs {
		text := inv.Values[f.Name][0]
		if _, listable := ix.refKinds()[f.Ref]; !listable {
			continue // nothing to look names up in (a tab id, a revision id): the value is an id as given
		}
		if text == "-" || strings.HasPrefix(text, "@") && !strings.HasPrefix(text, "@@") {
			continue // a value read from a file or stdin is an id, passed as is
		}
		id, err := r.resolve(f.Ref, text)
		if err != nil {
			return nil, nil, err
		}
		if id != text {
			resolved[f.Name] = id
			r.out = append(r.out, Resolved{Field: f.Name, Kind: f.Ref, Input: text, ID: id})
		}
		r.scope[f.Ref] = firstNonEmpty(r.scope[f.Ref], id)
	}
	return resolved, r.out, nil
}

// resolve turns one name of a kind into an id.
func (r *resolver) resolve(kind, text string) (string, error) {
	switch {
	case strings.HasPrefix(text, "id:"):
		return text[3:], nil
	case strings.HasPrefix(text, "name:"):
		text = text[5:]
	case idLike.MatchString(text):
		return text, nil
	}
	list, ok := r.ix.refKinds()[kind]
	if !ok {
		return "", &Fail{Code: "usage", Exit: ExitUsage, Message: fmt.Sprintf("Cannot look up a %s by name on this server.", kind),
			Hint: fmt.Sprintf("Pass the %s's id (it looks like %s_…), or write id:%s.", kind, kind[:min(3, len(kind))], "…")}
	}
	// A kind listed inside another (a document inside a project) needs that scope.
	var parentKind, parentParam string
	for _, f := range list.Fields() {
		if f.Loc == "path" && f.Ref != "" {
			parentKind, parentParam = f.Ref, f.Name
		}
	}
	parentID := ""
	if parentKind != "" {
		parentID = r.scope[parentKind]
		if parentID == "" && parentKind == "compendium" {
			// A key restricted to one project or compendium works in one
			// compendium: entities are looked up there without being told.
			if it, ok := r.pinnedCompendium(); ok {
				parentID = it.ID
			}
		}
		if parentID == "" && strings.Contains(text, "/") {
			head, rest, _ := strings.Cut(text, "/")
			// A leading project name; if it is not one, the whole text may still be a title.
			id, err := r.resolve(parentKind, head)
			if f, isFail := err.(*Fail); err != nil && (!isFail || f.Code != "not_found") {
				return "", err // ambiguous, forbidden, unreachable: say so
			}
			if err == nil {
				parentID, text = id, rest
				r.scope[parentKind] = id
			}
		}
		if parentID == "" {
			return "", &Fail{Code: "usage", Exit: ExitUsage,
				Message: fmt.Sprintf("To find a %s by name I need to know which %s it is in.", kind, parentKind),
				Hint:    fmt.Sprintf("Add --%s NAME, or write the path as \"%s name/%s name\", or pass the %s's id.", parentKind, parentKind, kind, kind)}
		}
	}
	items, err := r.list(kind, list, parentParam, parentID, text, "", true)
	if err != nil {
		return "", err
	}
	id, err := r.pick(kind, text, items)
	if f, ok := err.(*Fail); ok && f.Code == "not_found" {
		// The server's substring filter is a shortcut, not the rule: if it
		// found nothing, look through everything before giving up.
		if full, lerr := r.list(kind, list, parentParam, parentID, text, "", false); lerr == nil {
			id, err = r.pick(kind, text, full)
		}
	}
	if f, ok := err.(*Fail); ok && f.Code == "not_found" && hasStateAll(list) {
		// Not among the live ones: it may be archived or in the trash (a restore
		// needs to find it there). Live names were tried first, so this never
		// turns a clear match into an ambiguous one.
		if all, lerr := r.list(kind, list, parentParam, parentID, text, "all", false); lerr == nil {
			if id2, err2 := r.pick(kind, text, all); err2 == nil {
				return id2, nil
			}
		}
	}
	return id, err
}

// hasStateAll says whether a list op can include archived and trashed items.
func hasStateAll(o Op) bool {
	for _, f := range o.Fields() {
		if f.Name == "state" && f.Loc == "query" {
			for _, e := range f.Enum {
				if e == "all" {
					return true
				}
			}
		}
	}
	return false
}

// list fetches the items of a kind, following next_cursor.
func (r *resolver) list(kind string, op Op, parentParam, parentID, text, state string, shortcut bool) ([]item, error) {
	key := kind + "|" + parentID + "|" + state
	hasQ, hasCursor := false, false
	for _, f := range op.Fields() {
		hasQ = hasQ || (f.Name == "q" && f.Loc == "query")
		hasCursor = hasCursor || (f.Name == "cursor" && f.Loc == "query")
	}
	// A plain name can be narrowed on the server; a path cannot.
	text = strings.Join(strings.Fields(text), " ")
	narrow := shortcut && hasQ && !strings.Contains(text, "/") && parentID == ""
	if narrow {
		key += "|q=" + text
	}
	if v, ok := r.cache[key]; ok {
		return v, nil
	}
	path := op.Path
	if parentParam != "" {
		path = strings.ReplaceAll(path, "{"+parentParam+"}", url.PathEscape(parentID))
	}
	var all []item
	cursor := ""
	for page := 0; page < 50; page++ {
		q := url.Values{}
		if narrow {
			q.Set("q", text)
		}
		if state != "" {
			q.Set("state", state)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		resp, err := r.cl.Do(r.ctx, "GET", path, q, nil, nil)
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			f := fromServer(resp.Status, resp.Header, resp.Body)
			if kind == "project" && f.Code == "forbidden" {
				if it, ok := r.pinnedProject(); ok {
					return []item{it}, nil
				}
			}
			if kind == "compendium" && f.Code == "forbidden" {
				if it, ok := r.pinnedCompendium(); ok {
					return []item{it}, nil
				}
			}
			return nil, f
		}
		var body struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"next_cursor"`
		}
		if err := json.Unmarshal(resp.Body, &body); err != nil {
			return nil, &Fail{Code: "bad_response", Message: "The " + kind + " list was not JSON.", Exit: ExitGeneric}
		}
		for _, m := range body.Items {
			it := item{ID: str(m["id"]), Label: firstNonEmpty(str(m["title"]), str(m["name"])), Parent: str(m["parent_id"])}
			if as, ok := m["aliases"].([]any); ok {
				for _, a := range as {
					if s, ok := a.(string); ok {
						it.Aliases = append(it.Aliases, s)
					}
				}
			}
			if it.ID != "" {
				all = append(all, it)
			}
		}
		if body.NextCursor == "" || !hasCursor {
			break
		}
		cursor = body.NextCursor
	}
	r.cache[key] = all
	return all, nil
}

// whoamiOnce asks the server who the key is, once.
func (r *resolver) whoamiOnce() *whoamiInfo {
	if r.who == nil {
		r.who = &whoamiInfo{}
		if resp, err := r.cl.Do(r.ctx, "GET", "/api/v1/_whoami", nil, nil, nil); err == nil && resp.Status == 200 {
			_ = json.Unmarshal(resp.Body, r.who)
		}
	}
	return r.who
}

// pinnedProject answers for a key pinned to one project, which may not list.
func (r *resolver) pinnedProject() (item, bool) {
	if w := r.whoamiOnce(); w.Project != nil {
		return item{ID: w.Project.ID, Label: w.Project.Title}, true
	}
	return item{}, false
}

// pinnedCompendium answers for a key restricted to one project or compendium:
// the compendium it works in.
func (r *resolver) pinnedCompendium() (item, bool) {
	if w := r.whoamiOnce(); w.Compendium != nil {
		return item{ID: w.Compendium.ID, Label: w.Compendium.Name}, true
	}
	return item{}, false
}

func str(v any) string { s, _ := v.(string); return s }

// pick chooses the one item a name means, or fails.
func (r *resolver) pick(kind, text string, items []item) (string, error) {
	tree := false
	for _, it := range items {
		if it.Parent != "" {
			tree = true
		}
	}
	// A whole-text match comes first, so a title that contains a slash works.
	if m := match(items, text); len(m) > 0 {
		return one(kind, text, m, items)
	}
	if tree && strings.Contains(text, "/") {
		segs := strings.Split(text, "/")
		parent := ""
		var cur []item
		for i, seg := range segs {
			var level []item
			for _, it := range items {
				if it.Parent == parent {
					level = append(level, it)
				}
			}
			m := match(level, strings.TrimSpace(seg))
			if len(m) == 0 {
				return "", notFound(kind, text, items, strings.Join(segs[:i+1], "/"))
			}
			if len(m) > 1 {
				return "", ambiguous(kind, text, m, items)
			}
			cur = m
			parent = m[0].ID
		}
		return cur[0].ID, nil
	}
	return "", notFound(kind, text, items, text)
}

func norm(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// match returns the items whose label (or alias) equals text exactly, or failing
// that equals it ignoring case and spacing.
func match(items []item, text string) []item {
	var exact, fold []item
	for _, it := range items {
		if it.Label == text || it.ID == text {
			exact = append(exact, it) // an id counts, for ids that do not look like ids (a backup's file name)
			continue
		}
		hit := false
		for _, a := range it.Aliases {
			if a == text {
				hit = true
			}
		}
		if hit {
			exact = append(exact, it)
			continue
		}
		if norm(it.Label) == norm(text) {
			fold = append(fold, it)
			continue
		}
		for _, a := range it.Aliases {
			if norm(a) == norm(text) {
				fold = append(fold, it)
				break
			}
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return fold
}

func one(kind, text string, m, all []item) (string, error) {
	if len(m) == 1 {
		return m[0].ID, nil
	}
	return "", ambiguous(kind, text, m, all)
}

func paths(all []item) map[string]string {
	byID := map[string]item{}
	for _, it := range all {
		byID[it.ID] = it
	}
	out := map[string]string{}
	for _, it := range all {
		p, cur := it.Label, it
		for i := 0; i < 32 && cur.Parent != ""; i++ {
			par, ok := byID[cur.Parent]
			if !ok {
				break
			}
			p = par.Label + "/" + p
			cur = par
		}
		out[it.ID] = p
	}
	return out
}

func ambiguous(kind, text string, m, all []item) *Fail {
	ps := paths(all)
	f := &Fail{Code: "ambiguous", Exit: ExitUsage, Message: fmt.Sprintf("%q matches %d %ss. Use an id (or a longer path) to say which.", text, len(m), kind)}
	for _, it := range m {
		f.Candidates = append(f.Candidates, Candidate{ID: it.ID, Label: it.Label, Path: ps[it.ID]})
	}
	return f
}

func notFound(kind, text string, all []item, around string) *Fail {
	f := &Fail{Code: "not_found", Exit: ExitNotFound, Message: fmt.Sprintf("No %s named %q.", kind, text)}
	if around != text {
		f.Message = fmt.Sprintf("No %s named %q (nothing matches %q).", kind, text, around)
	}
	ps := paths(all)
	needle := norm(around)
	if i := strings.LastIndex(needle, "/"); i >= 0 {
		needle = strings.TrimSpace(needle[i+1:])
	}
	for _, it := range all {
		if needle != "" && strings.Contains(norm(it.Label), needle) && len(f.Candidates) < 5 {
			f.Candidates = append(f.Candidates, Candidate{ID: it.ID, Label: it.Label, Path: ps[it.ID]})
		}
	}
	if len(f.Candidates) > 0 {
		f.Hint = "Did you mean one of the candidates?"
	}
	return f
}

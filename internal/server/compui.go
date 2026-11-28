package server

import (
	"net/http"
	"strings"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// Shared parts of the Compendium pages (/c/{slug}/...): finding the Compendium a person means, and the tabs and
// crumb every one of those pages shows. A Compendium page is built from ops only, like every page: what the person
// may see is whatever compendium.get answers, and a Compendium they are not a member of is a 404.

// compTabs is what the Compendium tab strip needs: the slug and which tab is the current page.
type compTabs struct{ Slug, Active string }

func ctabs(slug, active string) compTabs { return compTabs{Slug: slug, Active: active} }

// A Compendium's address. Slugs are unique per owner, so two Compendiums a person can read may share one (their own
// "Cosmos" and an administrator's "Cosmos" they edit). /c/{slug} stays the address of the person's own or of the only
// readable match; any other Compendium whose slug is ambiguous among the ones they can read is addressed
// /c/{slug}~{last six characters of its id}. Everything that links to a Compendium takes its address from here, and the
// resolver accepts both forms.

// compSuffix is the part of an id that tells two Compendiums of one slug apart.
func compSuffix(id string) string {
	if len(id) > 6 {
		return id[len(id)-6:]
	}
	return id
}

// compAddressMap gives each Compendium of the readable set its address, by id.
func compAddressMap(items []compendiums.Compendium) map[string]string {
	n := map[string]int{}
	for _, c := range items {
		n[c.Slug]++
	}
	m := make(map[string]string, len(items))
	for _, c := range items {
		m[c.ID] = c.Slug
		if n[c.Slug] > 1 && c.Role != "owner" {
			m[c.ID] = c.Slug + "~" + compSuffix(c.ID)
		}
	}
	return m
}

// compAddresses reads the set the caller can open and addresses it.
func (s *Server) compAddresses(r *http.Request) map[string]string {
	a := authFrom(r.Context())
	cacheable := a.cache != nil && (r.Method == http.MethodGet || r.Method == http.MethodHead)
	if cacheable {
		a.cache.mu.Lock()
		if a.cache.compDone {
			m := a.cache.compAddrs
			a.cache.mu.Unlock()
			return m
		}
		a.cache.mu.Unlock()
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.list", compendiums.ListIn{State: compendiums.StateAll})
	if err != nil {
		return nil
	}
	m := compAddressMap(out.(compendiums.ListOut).Items)
	if cacheable {
		a.cache.mu.Lock()
		a.cache.compAddrs, a.cache.compDone = m, true
		a.cache.mu.Unlock()
	}
	return m
}

// addressed returns c with its Slug replaced by its address, so every page built from it links to the right one.
func (s *Server) addressed(r *http.Request, c compendiums.Compendium) compendiums.Compendium {
	if a, ok := s.compAddresses(r)[c.ID]; ok {
		c.Slug = a
	}
	return c
}

// addressedAll does the same for a list, with one read of the set.
func (s *Server) addressedAll(r *http.Request, items []compendiums.Compendium) []compendiums.Compendium {
	m := s.compAddresses(r)
	for i := range items {
		if a, ok := m[items[i].ID]; ok {
			items[i].Slug = a
		}
	}
	return items
}

// pickCompendium finds the one an address names in the readable set (nil when there is none).
func pickCompendium(items []compendiums.Compendium, address string) *compendiums.Compendium {
	slug, suffix := address, ""
	if i := strings.LastIndex(address, "~"); i > 0 {
		slug, suffix = address[:i], address[i+1:]
	}
	var found *compendiums.Compendium
	for i := range items {
		if items[i].Slug != slug || (suffix != "" && compSuffix(items[i].ID) != suffix) {
			continue
		}
		if found == nil || (items[i].Role == "owner" && found.Role != "owner") {
			found = &items[i]
		}
	}
	return found
}

// compBanner is the quiet line every /c/{address}/... page of a Compendium that is in the trash or on the shelf
// carries: what state it is in, and for an owner the one button that changes it.
type compBanner struct {
	Kind  string // trashed or archived
	ID    string
	Owner bool
	CSRF  string
}

// compBack is the line at the top of a Compendium page that goes back to the writing: where the person was last writing among the
// books that use this Compendium.
type compBack struct {
	Href, Project, Doc string
	HasDoc             bool // the last document is named; an untitled one has an empty Doc
}

// compNav reads the Compendium of a /c/ page, quietly, for the banner of one in the trash or on the shelf, and for the way back to the book
// the person was last writing in. One list of the Compendiums serves both.
func (s *Server) compNav(r *http.Request) (*compBanner, *compBack) {
	rest := strings.TrimPrefix(r.URL.Path, "/c/")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return nil, nil
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.list", compendiums.ListIn{State: compendiums.StateAll})
	if err != nil {
		return nil, nil
	}
	c := pickCompendium(out.(compendiums.ListOut).Items, rest)
	if c == nil {
		return nil, nil
	}
	var back *compBack
	if r.Method == http.MethodGet && !isHTMX(r) && !isFetch(r) {
		back = s.compBackTo(r, c.ID)
	}
	if !c.Trashed && !c.Archived {
		return nil, back
	}
	kind := "archived"
	if c.Trashed {
		kind = "trashed"
	}
	return &compBanner{Kind: kind, ID: c.ID, Owner: c.Role == "owner", CSRF: authFrom(r.Context()).CSRF}, back
}

// compBackTo is the way back to writing from a Compendium's pages: the book the caller opened most recently among the ones that use it
// and that the caller can open, and its last document. The link is made from what the server knows alone, never from the request. A book the
// caller cannot open is never named: compendium.get lists only the ones they can. A book with a passphrase this session has not unlocked
// is named by its title and goes to its unlock page; nothing of its chapters is read.
func (s *Server) compBackTo(r *http.Request, compID string) *compBack {
	ctx, p := r.Context(), s.principal(r)
	co, err := s.Reg.Call(ctx, p, "compendium.get", compendiums.GetIn{ID: compID})
	if err != nil {
		return nil
	}
	use := co.(compendiums.DetailItem).Item.Projects
	if len(use) == 0 {
		return nil
	}
	lo, err := s.Reg.Call(ctx, p, "project.list", projects.ListIn{State: projects.StateAll})
	if err != nil {
		return nil
	}
	mine := map[string]projects.Project{}
	for _, pr := range lo.(projects.ListOut).Items {
		mine[pr.ID] = pr
	}
	// When each of them was last opened by this person. project.list leaves that out for a locked book, so it is read here, from the
	// caller's own state and for the books that use this Compendium only.
	args := []any{p.UserID}
	marks := make([]string, 0, len(use))
	for _, u := range use {
		if pr, ok := mine[u.ID]; ok && !pr.Trashed {
			args = append(args, u.ID)
			marks = append(marks, "?")
		}
	}
	if len(marks) == 0 {
		return nil
	}
	rows, err := s.DB.Read().QueryContext(ctx, `SELECT project_id, last_opened_at FROM user_project_state WHERE user_id = ? AND last_opened_at IS NOT NULL AND last_opened_at <> '' AND project_id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	// The book to go back to is the most recently opened one that is not locked, when there is one: a link to a book that asks for its
	// passphrase first is the poorer way back, and the writer was last in the unlocked one a moment before they were in it. When every
	// one of them is locked, the most recent of those, by its title alone.
	var bestOpen, bestLocked, atOpen, atLocked string
	for rows.Next() {
		var id, at string
		if rows.Scan(&id, &at) != nil {
			continue
		}
		if mine[id].Locked {
			if at > atLocked || (at == atLocked && id > bestLocked) {
				bestLocked, atLocked = id, at
			}
		} else if at > atOpen || (at == atOpen && id > bestOpen) {
			bestOpen, atOpen = id, at
		}
	}
	if rows.Err() != nil {
		return nil
	}
	best := bestOpen
	if best == "" {
		best = bestLocked
	}
	pr, ok := mine[best]
	if !ok {
		return nil
	}
	if pr.Locked || pr.LastDocumentID == "" {
		return &compBack{Href: "/p/" + pr.Address, Project: pr.Title}
	}
	do, err := s.Reg.Call(ctx, p, "doc.get", docs.DocIn{ID: pr.LastDocumentID})
	if err != nil {
		return &compBack{Href: "/p/" + pr.Address, Project: pr.Title}
	}
	return &compBack{Href: "/p/" + pr.Address + "/d/" + pr.LastDocumentID + "#resume", Project: pr.Title, HasDoc: true, Doc: strings.TrimSpace(do.(docs.Item).Item.Title)}
}

// compendiumBySlug finds the Compendium a /c/{address} names among the ones the caller can open, trashed and
// archived ones too (their members open them to restore them). A bare slug that two readable Compendiums share is
// the caller's own; a slug with ~suffix names the one whose id ends so. The Compendium it returns carries its address
// as its Slug.
func (s *Server) compendiumBySlug(w http.ResponseWriter, r *http.Request) (compendiums.Compendium, bool) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return compendiums.Compendium{}, false
	}
	out, err := s.Reg.Call(r.Context(), p, "compendium.list", compendiums.ListIn{State: compendiums.StateAll})
	if err != nil {
		s.fail(w, r, err)
		return compendiums.Compendium{}, false
	}
	items := out.(compendiums.ListOut).Items
	found := pickCompendium(items, r.PathValue("slug"))
	if found == nil {
		s.fail(w, r, ops.NotFound("compendium"))
		return compendiums.Compendium{}, false
	}
	c := *found
	if a, ok := compAddressMap(items)[c.ID]; ok {
		c.Slug = a
	}
	return c, true
}

// compendiumByID reads one Compendium through compendium.get, which authorizes (404 for a stranger), and addresses
// it. It is the one by-id reader: the pages that act on a Compendium named by an id all come through here.
func (s *Server) compendiumByID(w http.ResponseWriter, r *http.Request, id string) (compendiums.Compendium, bool) {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.get", compendiums.GetIn{ID: id})
	if err != nil {
		s.fail(w, r, err)
		return compendiums.Compendium{}, false
	}
	return s.addressed(r, out.(compendiums.DetailItem).Item.Compendium), true
}

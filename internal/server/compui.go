package server

import (
	"net/http"
	"strings"

	"github.com/crueber/scriobh/internal/domain/compendiums"
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
	out, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.list", compendiums.ListIn{State: compendiums.StateAll})
	if err != nil {
		return nil
	}
	return compAddressMap(out.(compendiums.ListOut).Items)
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

// compBannerFor reads the Compendium of a /c/ page, quietly, for its banner.
func (s *Server) compBannerFor(r *http.Request) *compBanner {
	rest := strings.TrimPrefix(r.URL.Path, "/c/")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return nil
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.list", compendiums.ListIn{State: compendiums.StateAll})
	if err != nil {
		return nil
	}
	c := pickCompendium(out.(compendiums.ListOut).Items, rest)
	if c == nil || (!c.Trashed && !c.Archived) {
		return nil
	}
	kind := "archived"
	if c.Trashed {
		kind = "trashed"
	}
	return &compBanner{Kind: kind, ID: c.ID, Owner: c.Role == "owner", CSRF: authFrom(r.Context()).CSRF}
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

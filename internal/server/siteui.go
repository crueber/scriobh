package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/projects"
	site "github.com/crueber/scriobh/internal/domain/site"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
	sitegen "github.com/crueber/scriobh/internal/site"
)

// The static site page, /p/{slug}/site, for the owner and the editors (anyone else finds nothing, as on the Compile
// page). It reads site.status, site.options.get, site.doc.list and site.preview and shows what a publish would make:
// the status line (published, never published, changed since), the posts, pages and documents left out with their
// reasons, the warnings, the options and the per-document settings. It writes through site.options.set, site.doc.set,
// site.build (the zip, returned as a download) and site.publish (with a confirmation naming the folder). Nothing is
// published on page load or on a timer. When the server has site publishing off, the page says so and shows nothing else.

func (s *Server) registerSiteUI(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/site", s.sitePage)
	s.uiRoute("POST /ui/projects/{id}/site/{action}", s.uiSite)
}

// sitePostCap is how many posts the page lists with an edit form before it offers "show all": a long blog's page stays light.
const sitePostCap = 40

type sitePostView struct {
	sitegen.PostInfo
	Row      site.DocRow
	RowDate  string // the publication date set by hand, as YYYY-MM-DD for the date field
	TagsText string
}

type siteExcluded struct {
	site.Excluded
	Role, Pinned string
	// CanPost: a role of post would publish it. Only a part is held out by its kind alone; a status, a do-not-compile
	// mark or an unpublished parent still keeps a forced post out, so the button is not offered there.
	CanPost bool
	// Status: its status keeps it out; the outline is where that changes.
	Status bool
}

type siteWarnGroup struct {
	Code  string
	Items []sitegen.Warning
}

type siteNav struct{ Label, URL string }

type siteData struct {
	Project           projects.Project
	Slug, ProjectID   string
	CSRF              string
	Status            site.StatusOut
	Options           sitegen.Options
	Defaults          sitegen.Options
	Accents           []string
	Preview           *site.PreviewOut
	PreviewErr        string
	Posts, Pages      []sitePostView
	MorePosts         int
	All               bool
	Excluded          []siteExcluded
	SiteWarn, DocWarn []siteWarnGroup
	Warnings          int
	PublishedAt       string
	BuiltAt           string
	ChangedPublish    bool
	ChangedBuild      bool
	About             []sitePostView
	Nav               []siteNav
	Flash, Error      string
	CanOverwrite      bool
	Published         map[string]string
	Field             string
	Roles             []string
}

// siteWide are the warnings about the site as a whole: they come first.
var siteWide = map[string]bool{"no_base_url": true, "no_author": true, "about_missing": true, "accent_replaced": true, "drafts_included": true, "too_many_posts": true, "site_too_large": true}

func (s *Server) siteProject(w http.ResponseWriter, r *http.Request, proj projects.Project) bool {
	if proj.Role != ops.ProjectOwner && proj.Role != ops.ProjectEditor {
		s.fail(w, r, ops.NotFound("page")) // the page is for people who can publish; to anyone else it is not there
		return false
	}
	return true
}

func dateOnly(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

func (s *Server) siteView(r *http.Request, proj projects.Project) (siteData, error) {
	ctx, p := r.Context(), s.principal(r)
	d := siteData{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CSRF: authFrom(ctx).CSRF, All: r.URL.Query().Get("all") == "1", Roles: []string{"auto", "post", "page", "skip"}}
	so, err := s.Reg.Call(ctx, p, "site.status", site.StatusIn{ProjectID: proj.ID})
	if err != nil {
		return d, err
	}
	d.Status = so.(site.StatusOut)
	if !d.Status.Enabled {
		return d, nil
	}
	oo, err := s.Reg.Call(ctx, p, "site.options.get", site.OptionsGetIn{ProjectID: proj.ID})
	if err != nil {
		return d, err
	}
	opts := oo.(site.OptionsOut)
	d.Options, d.Defaults, d.Accents = opts.Options, opts.Defaults, opts.Accents
	for _, n := range d.Options.Nav {
		d.Nav = append(d.Nav, siteNav{n.Label, n.URL})
	}
	for len(d.Nav) < 8 && len(d.Nav) < len(d.Options.Nav)+2 {
		d.Nav = append(d.Nav, siteNav{})
	}
	rows := map[string]site.DocRow{}
	if lo, err := s.Reg.Call(ctx, p, "site.doc.list", site.DocListIn{ProjectID: proj.ID}); err == nil {
		for _, it := range lo.(site.DocListOut).Items {
			rows[it.DocumentID] = it
		}
	}
	d.PublishedAt, d.BuiltAt = utcWhen(d.Status.Publish.At), utcWhen(d.Status.Build.At)
	po, err := s.Reg.Call(ctx, p, "site.preview", site.PreviewIn{ProjectID: proj.ID})
	if err != nil {
		e := ops.AsError(err)
		if e.Code == ops.CodeInternal || e.Code == ops.CodeUnauthenticated {
			return d, err
		}
		d.PreviewErr = e.Message // nothing to publish yet, busy, too large: said on the page, which still shows the options
	} else {
		pv := po.(site.PreviewOut)
		d.Preview = &pv
		view := func(pi sitegen.PostInfo) sitePostView {
			row := rows[pi.DocumentID]
			if row.Role == "" {
				row.Role = "auto"
			}
			return sitePostView{PostInfo: pi, Row: row, RowDate: dateOnly(row.PublishedAt), TagsText: strings.Join(row.Tags, ", ")}
		}
		for i, pi := range pv.Posts {
			if i >= sitePostCap && !d.All {
				d.MorePosts = len(pv.Posts) - sitePostCap
				break
			}
			d.Posts = append(d.Posts, view(pi))
		}
		for _, pi := range pv.Pages {
			v := view(pi)
			d.Pages = append(d.Pages, v)
			d.About = append(d.About, v)
		}
		for _, e := range pv.Excluded {
			role := rows[e.ID].Role
			if role == "" {
				role = "auto"
			}
			d.Excluded = append(d.Excluded, siteExcluded{Excluded: e, Role: role, Pinned: rows[e.ID].PinnedSlug, CanPost: e.Kind == "part",
				Status: strings.HasPrefix(e.Reason, "its status is")})
		}
		idx := map[string]int{}
		for _, w := range pv.Warnings {
			grp := &d.DocWarn
			if siteWide[w.Code] || w.DocumentID == "" {
				grp = &d.SiteWarn
			}
			key := w.Code
			if grp == &d.SiteWarn {
				key = "s:" + key
			}
			i, ok := idx[key]
			if !ok {
				*grp = append(*grp, siteWarnGroup{Code: w.Code})
				i = len(*grp) - 1
				idx[key] = i
			}
			(*grp)[i].Items = append((*grp)[i].Items, w)
			d.Warnings++
		}
		d.ChangedPublish = d.Status.Publish.At != "" && d.Status.Publish.InputHash != pv.InputHash
		d.ChangedBuild = d.Status.Build.At != "" && d.Status.Build.InputHash != pv.InputHash
	}
	if d.Options.AboutID != "" {
		found := false
		for _, a := range d.About {
			found = found || a.DocumentID == d.Options.AboutID
		}
		if !found {
			d.About = append(d.About, sitePostView{PostInfo: sitegen.PostInfo{DocumentID: d.Options.AboutID, Title: d.Options.AboutID}})
		}
	}
	return d, nil
}

func (s *Server) sitePage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProject(w, r)
	if !ok || !s.siteProject(w, r, proj) {
		return
	}
	d, err := s.siteView(r, proj)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if fl := s.flashTake(r); fl != nil {
		d.Flash, d.Error, d.CanOverwrite, d.Field = fl["msg"], fl["err"], fl["overwrite"] == "1", fl["field"]
		if fl["location"] != "" {
			d.Published = fl
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	pg := s.page(r, "", nil)
	s.render(w, r, http.StatusOK, "site", s.page(r, pg.T("site.title_of", "title", proj.Title), d))
}

// sitePatch is the options form turned into a patch of what changed: a field the form sends with the value it already
// has is left out, so a save changes only what the person changed.
func sitePatch(r *http.Request, cur sitegen.Options) (sitegen.Patch, error) {
	var pt sitegen.Patch
	str := func(name, have string, dst **string) {
		v := strings.TrimSpace(r.PostFormValue(name))
		if v != have {
			*dst = &v
		}
	}
	boo := func(name string, have bool, dst **bool) {
		v := r.PostFormValue(name) == "on"
		if v != have {
			*dst = &v
		}
	}
	num := func(name string, have int, dst **int) error {
		raw := strings.TrimSpace(r.PostFormValue(name))
		n, err := strconv.Atoi(raw)
		if err != nil {
			return ops.Invalid(name, "")
		}
		if n != have {
			*dst = &n
		}
		return nil
	}
	str("title", cur.Title, &pt.Title)
	str("tagline", cur.Tagline, &pt.Tagline)
	str("base_url", cur.BaseURL, &pt.BaseURL)
	str("language", cur.Language, &pt.Language)
	str("dir", cur.Dir, &pt.Dir)
	str("posts", cur.Posts, &pt.Posts)
	str("feed_content", cur.FeedContent, &pt.FeedContent)
	str("footer_text", cur.FooterText, &pt.FooterText)
	str("about_id", cur.AboutID, &pt.AboutID)
	str("accent", cur.Accent, &pt.Accent)
	boo("include_drafts", cur.IncludeDrafts, &pt.IncludeDrafts)
	boo("parts_as_categories", cur.PartsAsCats, &pt.PartsAsCats)
	boo("show_dates", cur.ShowDates, &pt.ShowDates)
	boo("show_author", cur.ShowAuthor, &pt.ShowAuthor)
	boo("show_reading_time", cur.ShowReading, &pt.ShowReading)
	boo("show_cover", cur.ShowCover, &pt.ShowCover)
	boo("noindex", cur.NoIndex, &pt.NoIndex)
	for _, f := range []struct {
		name string
		have int
		dst  **int
	}{{"posts_per_page", cur.PostsPerPage, &pt.PostsPerPage}, {"feed_items", cur.FeedItems, &pt.FeedItems}, {"image_max_width", cur.ImageMaxWidth, &pt.ImageMaxWidth}} {
		if err := num(f.name, f.have, f.dst); err != nil {
			return pt, err
		}
	}
	var nav []sitegen.NavLink
	labels, urls := r.PostForm["nav_label"], r.PostForm["nav_url"]
	for i := range labels {
		l, u := strings.TrimSpace(labels[i]), ""
		if i < len(urls) {
			u = strings.TrimSpace(urls[i])
		}
		if l == "" && u == "" {
			continue
		}
		nav = append(nav, sitegen.NavLink{Label: l, URL: u})
	}
	same := len(nav) == len(cur.Nav)
	for i := 0; same && i < len(nav); i++ {
		same = nav[i] == cur.Nav[i]
	}
	if !same {
		if nav == nil {
			nav = []sitegen.NavLink{}
		}
		pt.Nav = &nav
	}
	return pt, nil
}

func (s *Server) uiSite(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok || !s.siteProject(w, r, proj) {
		return
	}
	ctx, p := r.Context(), s.principal(r)
	T := func(k string, args ...any) string { return i18n.Default.T(s.page(r, "", nil).Lang, k, args...) }
	page := "/p/" + proj.Slug + "/site"
	back := func(vals map[string]string, frag string) {
		s.flashSet(r, vals)
		http.Redirect(w, r, page+frag, http.StatusSeeOther)
	}
	refuse := func(err error, frag string) {
		e := ops.AsError(err)
		if e.Code == ops.CodeInternal || e.Code == ops.CodeUnauthenticated || e.Code == ops.CodeNotFound {
			s.fail(w, r, err)
			return
		}
		vals := map[string]string{"err": e.Message, "field": e.Field}
		if strings.Contains(e.Message, "overwrite_edited") {
			vals["overwrite"] = "1"
		}
		back(vals, frag)
	}
	switch r.PathValue("action") {
	case "options":
		oo, err := s.Reg.Call(ctx, p, "site.options.get", site.OptionsGetIn{ProjectID: proj.ID})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		pt, err := sitePatch(r, oo.(site.OptionsOut).Options)
		if err != nil {
			back(map[string]string{"err": T("site.o_number"), "field": ops.AsError(err).Field}, "#site-options")
			return
		}
		if _, err := s.Reg.Call(ctx, p, "site.options.set", site.OptionsSetIn{ProjectID: proj.ID, Options: pt}); err != nil {
			refuse(err, "#site-options")
			return
		}
		back(map[string]string{"msg": T("site.o_saved")}, "#site-options")
	case "reset":
		if _, err := s.Reg.Call(ctx, p, "site.options.set", site.OptionsSetIn{ProjectID: proj.ID, Options: sitegen.Patch{Clear: sitegen.ClearableFields}}); err != nil {
			refuse(err, "#site-options")
			return
		}
		back(map[string]string{"msg": T("site.o_reset_done")}, "#site-options")
	case "doc", "role":
		id := r.PostFormValue("document_id")
		in := site.DocSetIn{ProjectID: proj.ID, DocumentID: id}
		frag := "#doc-" + id
		if r.PathValue("action") == "role" {
			role := r.PostFormValue("role")
			in.Role = &role
			frag = "#site-out"
		} else {
			var cur site.DocRow
			if lo, err := s.Reg.Call(ctx, p, "site.doc.list", site.DocListIn{ProjectID: proj.ID}); err == nil {
				for _, it := range lo.(site.DocListOut).Items {
					if it.DocumentID == id {
						cur = it
					}
				}
			}
			if cur.Role == "" {
				cur.Role = "auto"
			}
			if v := r.PostFormValue("role"); v != "" && v != cur.Role {
				in.Role = &v
			}
			if v := strings.TrimSpace(r.PostFormValue("slug")); v != cur.Slug {
				in.Slug = &v
			}
			if v := strings.TrimSpace(r.PostFormValue("published_at")); v != dateOnly(cur.PublishedAt) {
				in.PublishedAt = &v
			}
			if v := strings.TrimSpace(r.PostFormValue("excerpt")); v != cur.Excerpt {
				in.Excerpt = &v
			}
			var tags []string
			for _, t := range strings.Split(r.PostFormValue("tags"), ",") {
				if t = strings.TrimSpace(t); t != "" {
					tags = append(tags, t)
				}
			}
			if strings.Join(tags, ",") != strings.Join(cur.Tags, ",") {
				if tags == nil {
					tags = []string{}
				}
				in.Tags = &tags
			}
			if r.PostFormValue("unpin") == "on" {
				in.Clear = append(in.Clear, "pinned_slug")
			}
		}
		if _, err := s.Reg.Call(ctx, p, "site.doc.set", in); err != nil {
			refuse(err, frag)
			return
		}
		back(map[string]string{"msg": T("site.d_saved")}, frag)
	case "unpin":
		id := r.PostFormValue("document_id")
		if _, err := s.Reg.Call(ctx, p, "site.doc.set", site.DocSetIn{ProjectID: proj.ID, DocumentID: id, Clear: []string{"pinned_slug"}}); err != nil {
			refuse(err, "#doc-"+id)
			return
		}
		back(map[string]string{"msg": T("site.d_saved")}, "#doc-"+id)
	case "build":
		out, err := s.Reg.Call(ctx, p, "site.build", site.BuildIn{ProjectID: proj.ID})
		if err != nil {
			refuse(err, "#site-status")
			return
		}
		b := out.(site.BuildOut)
		h := w.Header()
		h.Set("X-Site-Posts", strconv.Itoa(b.Posts))
		h.Set("X-Site-Pages", strconv.Itoa(b.Pages))
		h.Set("X-Site-Files", strconv.Itoa(b.Files))
		h.Set("X-Site-Sha256", b.SHA256)
		writeDownload(w, b)
	case "publish":
		out, err := s.Reg.Call(ctx, p, "site.publish", site.PublishIn{ProjectID: proj.ID, OverwriteEdited: r.PostFormValue("overwrite_edited") == "on"})
		if err != nil {
			refuse(err, "#site-status")
			return
		}
		res := out.(site.PublishOut)
		vals := map[string]string{"location": res.Location, "posts": strconv.Itoa(res.Posts), "pages": strconv.Itoa(res.Pages), "files": strconv.Itoa(res.Files)}
		if res.Replaced {
			vals["replaced"] = "1"
		}
		back(vals, "#site-status")
	default:
		s.fail(w, r, ops.NotFound("page"))
	}
}

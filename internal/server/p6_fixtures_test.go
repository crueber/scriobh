package server

import (
	site "github.com/crueber/scriobh/internal/domain/site"
	sitegen "github.com/crueber/scriobh/internal/site"
	"github.com/crueber/scriobh/internal/webhook"
)

// Fixtures for the Phase 6 publish and connect pages: what the template lint, the structure checks and the stylesheet
// budget render.

func siteFixture(kind string) siteData {
	proj := fixtureProjects()[0]
	d := siteData{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CSRF: "tok_writer", Roles: []string{"auto", "post", "page", "skip"}}
	if kind == "off" {
		return d
	}
	d.Status = site.StatusOut{Enabled: true, PublishEnabled: kind != "zip"}
	d.Options, d.Defaults, d.Accents = sitegen.Defaults(), sitegen.Defaults(), sitegen.AccentNames
	d.Nav = []siteNav{{"Mastodon", "https://social.example/@wren"}, {}}
	if kind == "empty" {
		d.PreviewErr = "There is nothing to publish: no post is selected."
		return d
	}
	posts := []sitegen.PostInfo{
		{DocumentID: "doc_c2", Title: "Low Water", Role: "post", Slug: "low-water", SlugFrom: "pinned", Path: "posts/low-water.html", Date: "2026-03-02", DateFrom: "set", Words: 1800, Minutes: 8, Excerpt: "The tide had gone out.", ExcerptSrc: "set", Tags: []string{"sea"}, Category: "Part One"},
		{DocumentID: "doc_c1", Title: "The Quay", Role: "post", Slug: "the-quay", SlugFrom: "title", Path: "posts/the-quay.html", Date: "2026-03-01", DateFrom: "created", Words: 1200, Minutes: 5, Excerpt: "Nobody counted the boats.", ExcerptSrc: "body"},
	}
	pages := []sitegen.PostInfo{{DocumentID: "doc_ab", Title: "About", Role: "page", Slug: "about", Path: "pages/about.html", Words: 200, Minutes: 1}}
	pv := site.PreviewOut{Posts: posts, Pages: pages, Excluded: []site.Excluded{{ID: "doc_d1", Title: "Notes on the ending", Kind: "chapter", Reason: "status draft"}},
		Warnings: []sitegen.Warning{{Code: "no_base_url", Message: "No base address."}, {Code: "no_date", Message: "No date.", DocumentID: "doc_c1", Title: "The Quay"}},
		Files:    14, Words: 3200, InputHash: "abc"}
	d.Preview = &pv
	for _, p := range posts {
		d.Posts = append(d.Posts, sitePostView{PostInfo: p, Row: site.DocRow{DocumentID: p.DocumentID, Role: "auto", PinnedSlug: "low-water"}})
	}
	for _, p := range pages {
		v := sitePostView{PostInfo: p, Row: site.DocRow{Role: "page"}}
		d.Pages, d.About = append(d.Pages, v), append(d.About, v)
	}
	d.Excluded = []siteExcluded{{Excluded: pv.Excluded[0], Role: "auto", Pinned: "notes-on-the-ending", Status: true}, {Excluded: site.Excluded{ID: "doc_p1", Title: "Part One", Kind: "part", Reason: "a part is a container"}, Role: "auto", CanPost: true}}
	d.SiteWarn = []siteWarnGroup{{Code: "no_base_url", Items: pv.Warnings[:1]}}
	d.DocWarn = []siteWarnGroup{{Code: "no_date", Items: pv.Warnings[1:]}}
	d.Warnings = 2
	if kind == "published" {
		d.Status.Location = "the-salt-road--abc123/public"
		d.Status.Publish = site.Stamp{At: "2026-03-02T10:00:00Z", InputHash: "old", Posts: 2}
		d.PublishedAt, d.ChangedPublish = "2 Mar 2026, 10:00 UTC", true
		d.Published = map[string]string{"location": "the-salt-road--abc123/public", "posts": "2", "pages": "1", "files": "14", "replaced": "1"}
		d.Error, d.CanOverwrite = "files Scríobh published have been changed since (index.html).", true
	}
	return d
}

func webhooksFixture(kind string) webhooksData {
	proj := fixtureProjects()[0]
	d := webhooksData{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CSRF: "tok_writer", Form: map[string]string{}, FormEvents: map[string]bool{},
		List: webhook.List{Events: webhook.EventNames, Enabled: kind != "off", AllowPrivate: kind == "private"}}
	if kind == "off" {
		d.List.Reason = "Webhooks are switched off on this server (SCRIOBH_WEBHOOKS=off)."
	}
	if kind == "empty" || kind == "off" {
		return d
	}
	h := webhook.Webhook{ID: "whk_a", Name: "Ada", URL: "https://hooks.example.org", URLPathSet: true, Events: []string{"document.updated", "comment.created"}, Enabled: true,
		SecretHint: "whsec_ab", LastStatus: 200, LastDeliveryAt: "2026-03-02T10:00:00Z", Pending: 1, Dead: 2, FailureStreak: 2}
	off := webhook.Webhook{ID: "whk_b", Name: "Wren", URL: "https://b.example.org", Events: []string{"export.completed"}, DisabledReason: "Ten deliveries in a row failed.", SecretHint: "whsec_cd", IncludeText: true}
	for _, x := range []webhook.Webhook{h, off} {
		v := whView{Webhook: x, Created: "1 Mar 2026", LastAt: utcWhen(x.LastDeliveryAt), Events: map[string]bool{}}
		for _, e := range x.Events {
			v.Events[e] = true
		}
		d.Hooks = append(d.Hooks, v)
		d.Pending += x.Pending
		d.Dead += x.Dead
	}
	switch kind {
	case "once":
		d.Once = &whOnce{Name: "Ada", Secret: "whsec_abcdefabcdef"}
	case "log":
		d.Test = map[string]string{"test_id": "whk_a", "test_status": "0", "test_error": "refused"}
		d.Log = &whLog{Hook: d.Hooks[0], State: "dead", Pending: 1, Dead: 2, Next: "whd_1",
			Items: []whDelivery{{Delivery: webhook.Delivery{ID: "whd_2", WebhookID: "whk_a", Event: "document.updated", State: "dead", Attempts: 8, Status: 503, Error: "server_error", Excerpt: "Service unavailable"}, Created: "1 Mar 2026", Replay: true},
				{Delivery: webhook.Delivery{ID: "whd_3", WebhookID: "whk_a", Event: "ping", State: "pending", Attempts: 1}, Created: "1 Mar 2026", Next: "2 Mar 2026"}}}
	case "error":
		d.Error, d.Form["field"], d.Form["url"], d.Form["name"] = "That address is inside a private network; the server does not send there.", "url", "https://10.0.0.1/", "Ada"
		d.FormEvents["comment.created"] = true
	}
	return d
}

func mcpFixture(on bool) mcpData {
	d := mcpData{Enabled: on, Origin: "http://127.0.0.1:8080", Set: "core", ReadOnly: true, CanKeys: true}
	if !on {
		return d
	}
	d.URL = d.Origin + "/mcp?groups=core&readonly=1"
	d.Tools = 120
	d.Groups = []mcpGroupRow{{Name: "doc", Read: 5, Write: 7, Core: true}, {Name: "entity", Read: 4, Write: 6, Bible: true}, {Name: "stash", Read: 2, Write: 3}}
	d.Snippets = []mcpSnippet{{ID: "mcp-cli", Title: "Command line", Note: "The proxy.", Text: "scriobh-cli mcp --groups core --read-only"}}
	d.Keys = []mcpKeyRow{{Name: "Ada", Prefix: "scr_ab", Scope: "read", Pin: "only The Salt Road"}}
	return d
}

func p6PageFixtures() []fixture {
	return []fixture{
		{"site", p5With("Static site of The Salt Road", "/p/the-salt-road/site", siteFixture("off"))},
		{"site", p5With("Static site of The Salt Road", "/p/the-salt-road/site", siteFixture("empty"))},
		{"site", p5With("Static site of The Salt Road", "/p/the-salt-road/site", siteFixture("zip"))},
		{"site", p5With("Static site of The Salt Road", "/p/the-salt-road/site", siteFixture("published"))},
		{"webhooks", p5With("Webhooks of The Salt Road", "/p/the-salt-road/webhooks", webhooksFixture("empty"))},
		{"webhooks", p5With("Webhooks of The Salt Road", "/p/the-salt-road/webhooks", webhooksFixture("off"))},
		{"webhooks", p5With("Webhooks of The Salt Road", "/p/the-salt-road/webhooks", webhooksFixture("once"))},
		{"webhooks", p5With("Webhooks of The Salt Road", "/p/the-salt-road/webhooks", webhooksFixture("log"))},
		{"webhooks", p5With("Webhooks of The Salt Road", "/p/the-salt-road/webhooks", webhooksFixture("error"))},
		{"webhooks", p5With("Webhooks of The Salt Road", "/p/the-salt-road/webhooks", webhooksFixture("private"))},
		{"mcp", p5With("Connect an agent", "/settings/mcp", mcpFixture(true))},
		{"mcp", p5With("Connect an agent", "/settings/mcp", mcpFixture(false))},
	}
}

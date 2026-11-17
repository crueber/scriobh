package server

import "github.com/crueber/scriobh/internal/domain/desktop"

// desktopPageFixtures are the desktop pages in each of their states, for the template lint.
func desktopPageFixtures() []fixture {
	signedOut := Page{Path: "/desktop/profile", Desktop: true}
	admin := Page{User: fixtureUser("admin"), CSRF: "tok_admin", Desktop: true}
	with := func(p Page, title, path string, data any) Page { p.Title, p.Path, p.Data = title, path, data; return p }
	ext := &desktop.ExternalOut{Issuer: "https://idp.example.org/realms/writers", ClientID: "scriobh", SecretSet: true, AdminGroup: "admins", Port: 45123}
	who := &desktop.VerifiedOut{Identity: desktop.Identity{Issuer: ext.Issuer, Email: "wren@example.org", Name: "Wren <Writer>"}, Fingerprint: "0123456789abcdef0123456789abcdef"}
	hint := "http://127.0.0.1:<port>/auth/oidc/callback"
	models := []desktop.Model{{Name: "llama3.2:latest", Size: 2019393189, Parameters: "3.2B"}, {Name: "<script>alert(1)</script>"}}
	return []fixture{
		{"desktop-profile", with(signedOut, "Your local profile", "/desktop/profile", desktopProfileData{Token: "tok_form"})},
		{"desktop-profile", with(signedOut, "Your local profile, refused", "/desktop/profile", desktopProfileData{Token: "tok_form", Name: "<script>alert(1)</script>", Email: "nope", Field: "email", Error: "That does not look like an email address."})},
		{"desktop-external", with(signedOut, "Sign in from your browser", "/desktop/start", map[string]any{"Address": "http://127.0.0.1:45123"})},
		{"desktop-identity", with(admin, "Identity, embedded and empty", "/desktop/identity", desktopIdentityData{CSRF: "tok", S: desktop.IdPOut{Running: "embedded", Mode: "embedded", RedirectHint: hint, LocalProfile: "Wren"}})},
		{"desktop-identity", with(admin, "Identity, provider saved", "/desktop/identity", desktopIdentityData{CSRF: "tok", Saved: true, Issuer: ext.Issuer, Client: ext.ClientID, Admin: "admins", Port: "45123",
			S: desktop.IdPOut{Running: "embedded", Mode: "embedded", External: ext, RedirectHint: hint, LocalProfile: "Wren"}})},
		{"desktop-identity", with(admin, "Identity, proved", "/desktop/identity", desktopIdentityData{CSRF: "tok", Verified: true, Issuer: ext.Issuer, Client: ext.ClientID, Ticket: "http://127.0.0.1:45123/desktop/identity/verify?ticket=v1.abc",
			S: desktop.IdPOut{Running: "embedded", Mode: "embedded", External: ext, Verified: who, RedirectHint: hint, LocalProfile: "Wren"}})},
		{"desktop-identity", with(admin, "Identity, refused", "/desktop/identity", desktopIdentityData{CSRF: "tok", Error: "Type the provider's issuer URL exactly to confirm.", Field: "confirm", Issuer: ext.Issuer, Client: ext.ClientID,
			S: desktop.IdPOut{Running: "embedded", Mode: "embedded", External: ext, Verified: who, RedirectHint: hint, Problem: "the local profile's account was not found"}})},
		{"desktop-identity", with(admin, "Identity, switch waiting", "/desktop/identity", desktopIdentityData{CSRF: "tok", Issuer: ext.Issuer, Client: ext.ClientID,
			S: desktop.IdPOut{Running: "embedded", Mode: "embedded", Pending: "external", External: ext, Verified: who, RedirectHint: hint}})},
		{"desktop-identity", with(admin, "Identity, external", "/desktop/identity", desktopIdentityData{CSRF: "tok", S: desktop.IdPOut{Running: "external", Mode: "external", External: ext, RedirectHint: hint, LocalProfile: "Wren <Writer>"}})},
		{"desktop-identity", with(admin, "Identity, going back", "/desktop/identity", desktopIdentityData{CSRF: "tok", S: desktop.IdPOut{Running: "external", Mode: "external", Pending: "embedded", External: ext, RedirectHint: hint}})},
		{"desktop-local-model", with(admin, "A model, before looking", "/desktop/local-model", desktopModelData{CSRF: "tok"})},
		{"desktop-local-model", with(admin, "A model, none found", "/desktop/local-model", desktopModelData{CSRF: "tok", Looked: true})},
		{"desktop-local-model", with(admin, "A model, server with none", "/desktop/local-model", desktopModelData{CSRF: "tok", Looked: true, Found: true})},
		{"desktop-local-model", with(admin, "A model, found", "/desktop/local-model", desktopModelData{CSRF: "tok", Looked: true, Found: true, Models: models})},
		{"desktop-local-model", with(admin, "A model, added", "/desktop/local-model", desktopModelData{CSRF: "tok", Added: true, Error: "Choose one of the models listed."})},
	}
}

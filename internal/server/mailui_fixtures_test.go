package server

import (
	"github.com/crueber/scriobh/internal/mail"
)

// Fixtures for the mail pages: what the template lint, the structure checks and the stylesheet budget render.

func mailAdminFixture(kind string) mailAdminData {
	d := mailAdminData{Tab: "mail", CSRF: "tok_writer", Values: map[string]string{}, Domains: "kindle.com\nfree.kindle.com"}
	d.Cfg = mail.ConfigOut{Host: "smtp.example.org", Port: 587, Security: "starttls", Username: "mailer", PasswordSet: true, FromAddress: "scriobh@example.org", MaxAttachmentMB: 12,
		KindleDomains: []string{"kindle.com", "free.kindle.com"}, SecretKey: true, LinksEnabled: true}
	switch kind {
	case "off":
		d.Cfg = mail.ConfigOut{Port: 587, Security: "starttls", MaxAttachmentMB: 12, KindleDomains: []string{"kindle.com", "free.kindle.com"}}
		d.OffReason = ""
	case "paused":
		d.Cfg.PausedByRestore = true
	case "on":
		d.Cfg.Enabled, d.Cfg.Ready, d.Cfg.HasCA = true, true, true
		d.Cfg.Hold, d.HoldWhy = "auth", "The relay refused the user name and password."
		d.LastTest = "Last test: 2 Mar 2026, 10:00, The relay refused the user name and password."
		d.Out = mail.OutboxOut{Pending: 1, Sent: 3, Dead: 1, Digests: 2}
		d.Rows = []mailOutRow{{Kind: "kindle", State: "pending", Domain: "kindle.com", When: "2 Mar 2026, 10:00"}, {Kind: "digest", State: "dead", Domain: "example.org", Attempts: 6, Error: "The relay took too long.", When: "2 Mar 2026, 09:00"}}
		d.Test = &mailTestView{OK: true, Text: "The relay accepted the test message."}
		d.Flash = "The relay's settings are saved."
		d.PasswordCleared = true
	case "refused":
		d.Error, d.Field, d.Refused, d.PasswordLost = "Give one plain address.", "from_address", true, true
		d.Values = map[string]string{"host": "smtp.example.org", "port": "587", "security": "starttls", "username": "mailer", "from_address": "nope", "from_name": "", "max_attachment_mb": "12", "tls_ca_pem": "", "allow_private": ""}
	}
	return d
}

func kindleFixture(kind string) kindleData {
	proj := fixtureProjects()[0]
	d := kindleData{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CSRF: "tok_writer", K: kindleCompile{State: "ready", Address: "wren_k@kindle.com"}, Sender: "scriobh@example.org", CanSeeAddress: true}
	switch kind {
	case "off":
		d.K = kindleCompile{State: "off"}
	case "noaddr":
		d.K = kindleCompile{State: "noaddr"}
	case "preview":
		d.Preview = &mail.KindlePreviewOut{Recipient: "wren_k@kindle.com", FileName: "The-Salt-Road.epub", Documents: 12}
		d.PreviewSize, d.Token = "1.2 MB", "kpv_fixture_token"
	case "log":
		d.Error = "Earlier deliveries are still waiting to be sent. Try again when they have gone."
		d.Log = []kindleRow{
			{KindleLogEntry: mail.KindleLogEntry{ID: "k1", By: "Wren", FileName: "The-Salt-Road.epub", Recipient: "wren_k@kindle.com", RecipientDomain: "kindle.com", State: "sent"}, When: "2 Mar 2026, 10:00", Size: "1.2 MB"},
			{KindleLogEntry: mail.KindleLogEntry{ID: "k2", By: "Ada", FileName: "The-Salt-Road.epub", RecipientDomain: "kindle.com", State: "unknown"}, When: "1 Mar 2026, 10:00", Size: "1.1 MB"},
		}
		d.CanSeeAddress = false
		d.Flash = "Queued."
	}
	return d
}

func p7MailPageFixtures() []fixture {
	var out []fixture
	for _, k := range []string{"off", "paused", "on", "refused", "default"} {
		out = append(out, fixture{"admin-mail", p5With("Email · Administration", "/admin/mail", mailAdminFixture(k))})
	}
	for _, k := range []string{"off", "noaddr", "preview", "log", "ready"} {
		out = append(out, fixture{"kindle", p5With("Send to Kindle: The Salt Road", "/p/the-salt-road/kindle", kindleFixture(k))})
	}
	return out
}

func p7MailFragmentFixtures() []fragmentFixture {
	pg := p5Base()
	acct := func(on bool, how string) *mailAccount {
		a := &mailAccount{Zone: "Europe/Dublin", DomainsText: "kindle.com, free.kindle.com", How: how}
		a.Kindle = mail.KindleAddressOut{MailAvailable: on, Address: "wren_k@kindle.com", Domains: []string{"kindle.com", "free.kindle.com"}}
		a.Digest = mail.DigestOut{MailAvailable: on, Email: "wren@example.org"}
		if on {
			a.Kindle.Sender = "scriobh@example.org"
			a.Digest.Enabled, a.Digest.Frequency = true, "daily"
		}
		return a
	}
	confirm := acct(true, "")
	confirm.Digest.Enabled, confirm.Digest.NeedsConfirm = false, true
	bad := acct(true, "each morning")
	bad.Error, bad.Field, bad.Where = "Give one plain address, like name@kindle.com: no list, no name, no brackets.", "address", "kindle"
	noEmail := acct(true, "")
	noEmail.Digest.Email = ""
	k := func(state string) map[string]any {
		return map[string]any{"P": pg, "K": kindleCompile{State: state, Address: "wren_k@kindle.com"}, "ID": "prj_a", "Slug": "the-salt-road", "CSRF": "tok"}
	}
	return []fragmentFixture{
		{"admin-tabs", "admin-tabs", map[string]any{"T": pg, "Tab": "mail"}},
		{"settings-mail", "settings-mail", map[string]any{"P": pg, "M": acct(true, "each morning"), "CSRF": "tok"}},
		{"settings-mail", "settings-mail", map[string]any{"P": pg, "M": acct(false, ""), "CSRF": "tok"}},
		{"settings-mail", "settings-mail", map[string]any{"P": pg, "M": confirm, "CSRF": "tok"}},
		{"settings-mail", "settings-mail", map[string]any{"P": pg, "M": bad, "CSRF": "tok"}},
		{"settings-mail", "settings-mail", map[string]any{"P": pg, "M": noEmail, "CSRF": "tok"}},
		{"compile-kindle", "compile-kindle", k("ready")},
		{"compile-kindle", "compile-kindle", k("noaddr")},
		{"compile-kindle", "compile-kindle", k("off")},
	}
}

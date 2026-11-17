package desktop

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
)

// ServiceName is the name the runtime parts are registered under in ops.Env.
const ServiceName = "desktop"

// Service is what the server hands these ops. With none registered, or with On false, every op answers "not found":
// outside desktop mode there is nothing here.
type Service struct {
	// On is desktop mode. Embedded is whether this process signs in through the embedded issuer.
	On, Embedded bool
	DataDir      string
	Version      string
	// Box seals the client secret and the proofs of sign-in.
	Box *secrets.Box
	// Detect looks for a model server on this computer (see DetectOllama); tests replace it.
	Detect func(ctx context.Context) ([]Model, error)
	// Now is the clock; nil is real time.
	Now func() time.Time

	ticketMu    sync.Mutex
	usedTickets map[string]int64

	// Outbound counts the connections this process has opened since it started: all of them, and those to anywhere
	// but this computer.
	Outbound, OutboundElsewhere atomic.Int64
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func svc(c *ops.Ctx) (*Service, error) {
	s, _ := ops.Service[*Service](c.Env, ServiceName)
	if s == nil || !s.On {
		return nil, ops.NotFound("page")
	}
	return s, nil
}

// InfoOut is desktop.info.
type InfoOut struct {
	DataDir string `json:"data_dir" desc:"The data directory, as an absolute path: where the database, the files and the Markdown mirror are."`
	Version string `json:"version"`
	Mode    string `json:"mode" enum:"embedded,external" desc:"How this process signs people in: the embedded issuer (the local profile) or an external provider."`
	// Outbound says what left the machine.
	OutboundConnections int64 `json:"outbound_connections" desc:"Connections this process has opened since it started, to anywhere, its own port included."`
	OutboundElsewhere   int64 `json:"outbound_elsewhere" desc:"Of those, connections to anywhere but this computer. 0 unless you configured something that reaches out (an AI provider, a Git remote, mail, a webhook, an external identity provider)."`
}

// IdPOut is the identity-provider state as an administrator sees it. The client secret is never returned.
type IdPOut struct {
	Running  string       `json:"running" enum:"embedded,external" desc:"What this process is using now."`
	Mode     string       `json:"mode" enum:"embedded,external" desc:"What is stored: the same as running unless a switch waits for a restart."`
	Pending  string       `json:"pending,omitempty" enum:"embedded,external" desc:"A switch that happens at the next start."`
	External *ExternalOut `json:"external,omitempty"`
	Verified *VerifiedOut `json:"verified,omitempty" desc:"Who you signed in as at the external provider, with the settings above. A switch needs it."`
	Problem  string       `json:"problem,omitempty" desc:"Why the last switch was not made."`
	// LocalProfile is the name to type to go back.
	LocalProfile string `json:"local_profile,omitempty" desc:"The local profile's name."`
	RedirectHint string `json:"redirect_hint" desc:"What to register at the provider as the redirect address."`
}

// VerifiedOut is who was proved at the provider, as a page sees it: without the subject, and with the fingerprint a
// switch confirmation must carry.
type VerifiedOut struct {
	Identity
	Fingerprint string `json:"fingerprint" desc:"Names this identity; desktop.idp.switch to the external provider must carry it, so that the switch is made to the identity that was shown and not to one proved since."`
}

// ExternalOut is External without the secret.
type ExternalOut struct {
	Issuer      string `json:"issuer"`
	ClientID    string `json:"client_id"`
	SecretSet   bool   `json:"secret_set"`
	AdminGroup  string `json:"admin_group,omitempty"`
	WriterGroup string `json:"writer_group,omitempty"`
	ReaderGroup string `json:"reader_group,omitempty"`
	Port        int    `json:"port,omitempty"`
}

// SetIn is desktop.idp.set.
type SetIn struct {
	Issuer       string  `json:"issuer" desc:"The provider's issuer URL, exactly as its discovery document names it. https, or http on this computer."`
	ClientID     string  `json:"client_id"`
	ClientSecret *string `json:"client_secret,omitempty" desc:"Write-only: sealed with the instance's secret key and never returned. Leave out to keep the stored one; send an empty string for a public client with none."`
	AdminGroup   string  `json:"admin_group,omitempty" desc:"The provider group whose members administer this instance. Empty: the account that makes the switch stays the administrator."`
	WriterGroup  string  `json:"writer_group,omitempty"`
	ReaderGroup  string  `json:"reader_group,omitempty"`
	Port         int     `json:"port,omitempty" desc:"A fixed loopback port (1024 to 65535) for a provider that wants one exact redirect address; 0 for a port the system picks at every start."`
}

// check checks the provider's settings. It is called by the op, after it knows this is a desktop instance: outside
// desktop mode the answer is "not found" whatever was sent.
func (in SetIn) check() error {
	if len(in.Issuer) > 512 || !validIssuer(in.Issuer) {
		return ops.Invalid("issuer", "Give the provider's issuer URL: https://..., with no query. Plain http is accepted only for a provider on this computer.")
	}
	if in.ClientID == "" || len(in.ClientID) > 256 || strings.ContainsAny(in.ClientID, " \t\r\n") {
		return ops.Invalid("client_id", "Give the client ID the provider made for this application.")
	}
	if in.ClientSecret != nil && len(*in.ClientSecret) > 1024 {
		return ops.Invalid("client_secret", "That client secret is too long.")
	}
	for f, g := range map[string]string{"admin_group": in.AdminGroup, "writer_group": in.WriterGroup, "reader_group": in.ReaderGroup} {
		if len(g) > 128 || strings.ContainsAny(g, "\r\n\t") {
			return ops.Invalid(f, "A group name is at most 128 characters on one line.")
		}
	}
	if in.Port != 0 && (in.Port < 1024 || in.Port > 65535) {
		return ops.Invalid("port", "A port is between 1024 and 65535, or 0 for one the system picks.")
	}
	return nil
}

func validIssuer(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(raw) != raw {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		ip := net.ParseIP(h)
		return h == "localhost" || (ip != nil && ip.IsLoopback())
	}
	return false
}

// VerifyIn is desktop.idp.verify.
type VerifyIn struct {
	Proof string `json:"proof" desc:"What the application made when your sign-in at the provider completed. It cannot be written by hand."`
}

// check checks the proof is there.
func (in VerifyIn) check() error {
	if in.Proof == "" || len(in.Proof) > 8192 {
		return ops.Invalid("proof", "Sign in at the provider from the identity page; this is not called by hand.")
	}
	return nil
}

// SwitchIn is desktop.idp.switch.
type SwitchIn struct {
	To      string `json:"to" enum:"external,embedded"`
	Confirm string `json:"confirm" desc:"Typed to confirm: the provider's issuer URL to switch to it, the local profile's name to go back."`
	// Identity binds the confirmation of a switch to the external provider to the identity that was shown.
	Identity string `json:"identity,omitempty" desc:"To switch to the external provider: the fingerprint of the identity desktop.idp.get showed as verified. Refused (conflict) when another identity has been proved since."`
}

// check checks the direction.
func (in SwitchIn) check() error {
	if in.To != ModeExternal && in.To != ModeEmbedded {
		return ops.Invalid("to", "Switch to external or to embedded.")
	}
	return nil
}

// proofContext binds a sealed proof to its use; ProofTTL is how long one is good for.
const proofContext = "desktop.idp.proof"

// ProofTTL is how long after the sign-in at the provider its proof can be recorded.
const ProofTTL = 5 * time.Minute

type proof struct {
	Identity
	UserID string `json:"user_id"`
	Config string `json:"config"`
	Exp    int64  `json:"exp"`
}

// SealProof is called by the server when a signed-in administrator has completed a sign-in at the candidate
// provider: it seals who they were there, for whom, and for which settings. Only this process can make one (it is
// sealed with the instance key), so desktop.idp.verify cannot be told an identity nobody proved.
func (s *Service) SealProof(ctx context.Context, d *db.DB, userID string, id Identity) (string, error) {
	st, err := LoadState(ctx, d.Read())
	if err != nil {
		return "", err
	}
	if st.External == nil || st.External.Issuer != id.Issuer {
		return "", ops.Conflict("The provider's settings changed while you were signing in. Sign in at the provider again.")
	}
	raw, _ := json.Marshal(proof{Identity: id, UserID: userID, Config: st.External.hash(), Exp: s.now().Add(ProofTTL).Unix()})
	return s.Box.Seal(raw, proofContext)
}

// ticketContext binds a sealed ticket to its use.
const ticketContext = "desktop.idp.ticket"

type ticket struct {
	UserID string `json:"user_id"`
	Nonce  string `json:"n"`
	Exp    int64  `json:"exp"`
}

// SealTicket makes a ticket for one administrator: an address they open in an ordinary web browser to sign in at the
// provider there, because the application's own window does not open another site's page. It lets its holder record,
// once and within ProofTTL, who that administrator is at the provider; it signs nobody in and opens nothing else.
func (s *Service) SealTicket(userID string) (string, error) {
	raw, _ := json.Marshal(ticket{UserID: userID, Nonce: db.NewID("tkt"), Exp: s.now().Add(ProofTTL).Unix()})
	return s.Box.Seal(raw, ticketContext)
}

// OpenTicket returns the administrator a ticket was made for. use marks it used: a ticket records one proof.
func (s *Service) OpenTicket(sealed string, use bool) (userID string, ok bool) {
	if sealed == "" || len(sealed) > 4096 {
		return "", false
	}
	raw, err := s.Box.Open(sealed, ticketContext)
	if err != nil {
		return "", false
	}
	var t ticket
	if json.Unmarshal(raw, &t) != nil || t.UserID == "" || s.now().Unix() > t.Exp {
		return "", false
	}
	s.ticketMu.Lock()
	defer s.ticketMu.Unlock()
	if s.usedTickets == nil {
		s.usedTickets = map[string]int64{}
	}
	for n, exp := range s.usedTickets { // nothing older than its own life needs remembering
		if s.now().Unix() > exp {
			delete(s.usedTickets, n)
		}
	}
	if _, used := s.usedTickets[t.Nonce]; used {
		return "", false
	}
	if use {
		s.usedTickets[t.Nonce] = t.Exp
	}
	return t.UserID, true
}

// Candidate returns the provider an administrator entered, with its secret opened, for the sign-in that proves it.
func (s *Service) Candidate(ctx context.Context, d *db.DB) (issuer, clientID, secret string, x External, err error) {
	st, err := LoadState(ctx, d.Read())
	if err != nil {
		return "", "", "", External{}, err
	}
	if st.External == nil {
		return "", "", "", External{}, ops.Conflict("Enter the provider's settings first.")
	}
	c, err := ExternalConfig(st, s.Box)
	if err != nil {
		return "", "", "", External{}, ops.Conflict("The stored client secret cannot be opened. Enter it again.")
	}
	return c.Issuer, c.ClientID, c.ClientSecret, *st.External, nil
}

// Register adds the desktop ops. They are registered always, so the op schema, the CLI and the documentation are the
// same for every build; outside desktop mode each answers "not found". All are for an administrator's browser
// session, and none is offered to an agent over MCP (the group is not in its catalogue).
func Register(r *ops.Registry) {
	adminSession := ops.Admin().NotFoundForOthers().ForSessions()
	ops.Register(r, ops.Op{Name: "desktop.info", Method: "GET", Path: "/api/v1/desktop/info", Auth: adminSession, NoDryRun: true,
		Desc: "Desktop mode only (SCRIOBH_DESKTOP=1; not found otherwise). Where this instance keeps its data, its version, how it signs people in, and how many connections it has opened to anywhere but this computer. The desktop shell uses the path to show the folder. Administrators, browser session only."},
		func(c *ops.Ctx, _ struct{}) (InfoOut, error) {
			s, err := svc(c)
			if err != nil {
				return InfoOut{}, err
			}
			dir, aerr := filepath.Abs(s.DataDir)
			if aerr != nil {
				dir = s.DataDir
			}
			mode := ModeExternal
			if s.Embedded {
				mode = ModeEmbedded
			}
			return InfoOut{DataDir: dir, Version: s.Version, Mode: mode, OutboundConnections: s.Outbound.Load(), OutboundElsewhere: s.OutboundElsewhere.Load()}, nil
		})
	ops.Register(r, ops.Op{Name: "desktop.idp.get", Method: "GET", Path: "/api/v1/desktop/idp", Auth: adminSession,
		Desc: "Desktop mode only. Which identity provider this instance signs in through: the embedded issuer (the local profile) or an external one, the external provider's settings without its secret, who you proved to be there, and any switch waiting for a restart. Administrators, browser session only."},
		func(c *ops.Ctx, _ struct{}) (IdPOut, error) {
			s, err := svc(c)
			if err != nil {
				return IdPOut{}, err
			}
			return idpOut(c, s)
		})
	ops.Register(r, ops.Op{Name: "desktop.idp.set", SecretInput: true, Method: "PUT", Path: "/api/v1/desktop/idp", Auth: adminSession,
		Desc: "Desktop mode only, while the embedded issuer is in use. Store the external provider to switch to: issuer, client ID, client secret (sealed, never returned), the group names and an optional fixed port. Nothing changes until you have signed in at the provider from the identity page and made the switch. Changing the settings discards an earlier proof of sign-in. Administrators, browser session only."},
		func(c *ops.Ctx, in SetIn) (IdPOut, error) {
			s, err := svc(c)
			if err != nil {
				return IdPOut{}, err
			}
			if err := in.check(); err != nil {
				return IdPOut{}, err
			}
			if !s.Box.Enabled() {
				return IdPOut{}, ops.Conflict("This instance has no secret key, so a client secret cannot be kept.")
			}
			err = c.DB().WithTx(c, func(tx *sql.Tx) error {
				st, err := LoadState(c, tx)
				if err != nil {
					return err
				}
				if st.Mode != ModeEmbedded || !s.Embedded {
					return ops.Conflict("This instance signs in through the external provider. Go back to the local profile before you change the provider.")
				}
				if st.Pending != nil {
					return ops.Conflict("A switch is waiting for a restart. Cancel it before you change the provider.")
				}
				x := External{Issuer: in.Issuer, ClientID: in.ClientID, AdminGroup: strings.TrimSpace(in.AdminGroup), WriterGroup: strings.TrimSpace(in.WriterGroup),
					ReaderGroup: strings.TrimSpace(in.ReaderGroup), Port: in.Port}
				switch {
				case in.ClientSecret == nil:
					// A secret belongs to one provider and one client: it is kept only for the same ones.
					if st.External != nil && st.External.Issuer == in.Issuer && st.External.ClientID == in.ClientID {
						x.Secret = st.External.Secret
					}
				case *in.ClientSecret != "":
					sealed, err := s.Box.Seal([]byte(*in.ClientSecret), secretContext)
					if err != nil {
						return err
					}
					x.Secret = sealed
				}
				st.External, st.Verified, st.LastProblem = &x, nil, ""
				c.Audit(ops.AuditEntry{Action: "desktop.idp.set", TargetType: "settings", TargetID: KeyIdP,
					Detail: map[string]any{"secret_set": x.Secret != "", "port_set": x.Port != 0}})
				if err := putJSON(tx, KeyIdP, st); err != nil {
					return err
				}
				return c.FlushTo(tx)
			})
			if err != nil {
				return IdPOut{}, ops.AsError(err)
			}
			return idpOut(c, s)
		})
	ops.Register(r, ops.Op{Name: "desktop.idp.verify", SecretInput: true, Method: "POST", Path: "/api/v1/desktop/idp/verify", Auth: adminSession,
		Desc: "Desktop mode only. Record that you signed in at the external provider with the stored settings. The proof is made by the application when that sign-in completes (the identity page starts it) and cannot be written by hand; it is good for five minutes, for the administrator who signed in, and for the settings as they were. Administrators, browser session only."},
		func(c *ops.Ctx, in VerifyIn) (IdPOut, error) {
			s, err := svc(c)
			if err != nil {
				return IdPOut{}, err
			}
			if err := in.check(); err != nil {
				return IdPOut{}, err
			}
			bad := ops.Invalid("proof", "That is not a proof of sign-in this instance made, or it is too old. Sign in at the provider again from the identity page.")
			raw, oerr := s.Box.Open(in.Proof, proofContext)
			if oerr != nil {
				return IdPOut{}, bad
			}
			var p proof
			if json.Unmarshal(raw, &p) != nil || p.UserID != c.P.UserID || s.now().Unix() > p.Exp || p.Subject == "" {
				return IdPOut{}, bad
			}
			err = c.DB().WithTx(c, func(tx *sql.Tx) error {
				st, err := LoadState(c, tx)
				if err != nil {
					return err
				}
				if st.Mode != ModeEmbedded || !s.Embedded || st.Pending != nil || st.External == nil || st.External.hash() != p.Config {
					return ops.Conflict("The provider's settings changed after you signed in there. Sign in at the provider again.")
				}
				st.Verified = &Verified{Identity: p.Identity, UserID: p.UserID, Config: p.Config, At: db.Now()}
				c.Audit(ops.AuditEntry{Action: "desktop.idp.verify", TargetType: "settings", TargetID: KeyIdP})
				if err := putJSON(tx, KeyIdP, st); err != nil {
					return err
				}
				return c.FlushTo(tx)
			})
			if err != nil {
				return IdPOut{}, ops.AsError(err)
			}
			return idpOut(c, s)
		})
	ops.Register(r, ops.Op{Name: "desktop.idp.switch", Method: "POST", Path: "/api/v1/desktop/idp/switch", Auth: adminSession,
		Desc: "Desktop mode only. Ask for the switch of identity provider; it is made when the application next starts. To the external provider: you must be signed in as the local profile and must have signed in at the provider with the stored settings (the identity page does both); your account, and only yours, is then moved to that identity. No account is ever matched by email address. Type the provider's issuer URL to confirm. Back to the embedded issuer: the account that was moved becomes the local profile again; type the local profile's name to confirm. Administrators, browser session only."},
		func(c *ops.Ctx, in SwitchIn) (IdPOut, error) {
			s, err := svc(c)
			if err != nil {
				return IdPOut{}, err
			}
			if err := in.check(); err != nil {
				return IdPOut{}, err
			}
			err = c.DB().WithTx(c, func(tx *sql.Tx) error {
				st, err := LoadState(c, tx)
				if err != nil {
					return err
				}
				if st.Pending != nil {
					return ops.Conflict("A switch is already waiting for a restart.")
				}
				running := ModeExternal
				if s.Embedded {
					running = ModeEmbedded
				}
				if st.Mode != running {
					return ops.Conflict("Restart the application first: it has not yet taken up the last change.")
				}
				if in.To == st.Mode {
					return ops.Conflict("This instance already signs in that way.")
				}
				switch in.To {
				case ModeExternal:
					if st.External == nil || st.Verified == nil || st.Verified.Config != st.External.hash() {
						return ops.Conflict("Sign in at the provider first, from the identity page: the switch is only made to an identity you have proved is yours.")
					}
					if st.Verified.UserID != c.P.UserID {
						return ops.Conflict("Another administrator proved the identity at the provider. Sign in there yourself.")
					}
					var bound int
					if err := tx.QueryRowContext(c, `SELECT count(*) FROM users WHERE id = ? AND oidc_issuer = ? AND deleted_at IS NULL`, c.P.UserID, LocalIdentity).Scan(&bound); err != nil {
						return err
					}
					if bound != 1 {
						return ops.Conflict("Only the local profile can make this switch.")
					}
					if in.Confirm != st.External.Issuer {
						return ops.Invalid("confirm", "Type the provider's issuer URL exactly to confirm.")
					}
					if in.Identity != st.Verified.Identity.Fingerprint() {
						return ops.Conflict("The identity proved at the provider is not the one this page showed (another sign-in there was recorded since). Look at who is shown now and confirm again.")
					}
				case ModeEmbedded:
					var p struct {
						Name string `json:"name"`
					}
					_, _ = getJSON(c, tx, KeyProfile, &p)
					want := p.Name
					if want == "" {
						want = "local profile"
					}
					if in.Confirm != want {
						return ops.Invalid("confirm", "Type the local profile's name exactly to confirm.")
					}
				}
				st.Pending = &Pending{To: in.To, By: c.P.UserID, At: db.Now()}
				st.LastProblem = ""
				c.Audit(ops.AuditEntry{Action: "desktop.idp.switch", TargetType: "settings", TargetID: KeyIdP, Detail: map[string]any{"to": in.To}})
				if err := putJSON(tx, KeyIdP, st); err != nil {
					return err
				}
				return c.FlushTo(tx)
			})
			if err != nil {
				return IdPOut{}, ops.AsError(err)
			}
			return idpOut(c, s)
		})
	ops.Register(r, ops.Op{Name: "desktop.idp.cancel", Method: "DELETE", Path: "/api/v1/desktop/idp/switch", Auth: adminSession,
		Desc: "Desktop mode only. Cancel a switch of identity provider that is waiting for a restart. Administrators, browser session only."},
		func(c *ops.Ctx, _ struct{}) (IdPOut, error) {
			s, err := svc(c)
			if err != nil {
				return IdPOut{}, err
			}
			err = c.DB().WithTx(c, func(tx *sql.Tx) error {
				st, err := LoadState(c, tx)
				if err != nil {
					return err
				}
				if st.Pending == nil {
					return nil
				}
				c.Audit(ops.AuditEntry{Action: "desktop.idp.cancel", TargetType: "settings", TargetID: KeyIdP, Detail: map[string]any{"to": st.Pending.To}})
				st.Pending = nil
				if err := putJSON(tx, KeyIdP, st); err != nil {
					return err
				}
				return c.FlushTo(tx)
			})
			if err != nil {
				return IdPOut{}, ops.AsError(err)
			}
			return idpOut(c, s)
		})
	ops.Register(r, ops.Op{Name: "desktop.localmodel.detect", NoDryRun: true, Method: "POST", Path: "/api/v1/desktop/local-model/detect", Auth: adminSession,
		Desc: "Desktop mode only. Look for an Ollama model server on this computer: one request to 127.0.0.1:11434 (the address is fixed; nothing you send chooses it), made only when you ask, never by itself. Answers with the models it has. Nothing is downloaded, started or changed; to use a model, add the provider (ai.provider.create, kind ollama, local). Administrators, browser session only."},
		func(c *ops.Ctx, _ struct{}) (DetectOut, error) {
			s, err := svc(c)
			if err != nil {
				return DetectOut{}, err
			}
			detect := s.Detect
			if detect == nil {
				detect = DetectOllama
			}
			models, derr := detect(c)
			if derr != nil {
				return DetectOut{Found: false, Address: OllamaAddress}, nil
			}
			return DetectOut{Found: true, Address: OllamaAddress, Models: models}, nil
		})
}

func idpOut(c *ops.Ctx, s *Service) (IdPOut, error) {
	st, err := LoadState(c, c.DB().Read())
	if err != nil {
		return IdPOut{}, ops.Internal(err)
	}
	out := IdPOut{Running: ModeExternal, Mode: st.Mode, Problem: st.LastProblem,
		RedirectHint: "http://127.0.0.1:<port>/auth/oidc/callback"}
	if s.Embedded {
		out.Running = ModeEmbedded
	}
	if st.Pending != nil {
		out.Pending = st.Pending.To
	}
	if x := st.External; x != nil {
		out.External = &ExternalOut{Issuer: x.Issuer, ClientID: x.ClientID, SecretSet: x.Secret != "", AdminGroup: x.AdminGroup, WriterGroup: x.WriterGroup, ReaderGroup: x.ReaderGroup, Port: x.Port}
	}
	if st.Verified != nil && st.External != nil && st.Verified.Config == st.External.hash() && st.Verified.UserID == c.P.UserID {
		id := st.Verified.Identity
		fp := id.Fingerprint()
		id.Subject = "" // of no use to a page
		out.Verified = &VerifiedOut{Identity: id, Fingerprint: fp}
	}
	var p struct {
		Name string `json:"name"`
	}
	if _, err := getJSON(c, c.DB().Read(), KeyProfile, &p); err == nil {
		out.LocalProfile = p.Name
	}
	return out, nil
}

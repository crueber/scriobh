package mail

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/secrets"
)

// Config is the operator's relay as stored. PasswordSealed is the sealed password; the open one never leaves
// openRelay.
type Config struct {
	Enabled         bool
	Host            string
	Port            int
	Security        string
	AllowPrivate    bool
	TLSCAPEM        string
	Username        string
	PasswordSealed  string
	FromAddress     string
	FromName        string
	MaxAttachmentMB int
	DisabledReason  string
	RestorePaused   bool
	LastTestAt      string
	LastTestClass   string
	KindleDomains   string // comma separated, lower case; empty is DefaultKindleDomains
	UpdatedAt       string
}

// Limits of what the operator may set.
const (
	DefaultMaxAttachmentMB = 12
	MaxAttachmentMBLimit   = 40
	maxCAPEM               = 16 << 10
	maxCACerts             = 8
)

// PausedByRestore is the reason shown when a restore switched mail off.
const PausedByRestore = "Paused when this copy was restored from a backup, so it does not send the old copy's mail. Switch mail on again here when this is the server that should send it."

const configCols = `enabled, host, port, security, allow_private, tls_ca_pem, username, password_sealed, from_address, from_name, max_attachment_mb, disabled_reason, restore_paused, last_test_at, last_test_class, kindle_domains, updated_at`

type queryer interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func loadConfig(ctx context.Context, q queryer) (Config, error) {
	var c Config
	var en, ap, rp int
	err := q.QueryRowContext(ctx, `SELECT `+configCols+` FROM mail_config WHERE id = 1`).Scan(&en, &c.Host, &c.Port, &c.Security, &ap, &c.TLSCAPEM, &c.Username, &c.PasswordSealed,
		&c.FromAddress, &c.FromName, &c.MaxAttachmentMB, &c.DisabledReason, &rp, &c.LastTestAt, &c.LastTestClass, &c.KindleDomains, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Config{Security: SecStartTLS, MaxAttachmentMB: DefaultMaxAttachmentMB}, nil
	}
	c.Enabled, c.AllowPrivate, c.RestorePaused = en != 0, ap != 0, rp != 0
	return c, err
}

// Ready says whether the settings are complete enough to send with.
func (c Config) Ready() bool {
	return c.Host != "" && c.Port > 0 && c.FromAddress != "" && (c.Username == "" || c.PasswordSealed != "")
}

// MaxAttachmentBytes is the largest attachment this relay setting allows.
func (c Config) MaxAttachmentBytes() int64 {
	mb := c.MaxAttachmentMB
	if mb <= 0 {
		mb = DefaultMaxAttachmentMB
	}
	return int64(mb) << 20
}

func sealContext(host string, port int, user string) string {
	return "mail_config/1/password/" + strings.ToLower(host) + ":" + strconv.Itoa(port) + "/" + user
}

// openRelay opens the password and builds the Relay. The password is in the result only.
func openRelay(box *secrets.Box, c Config) (Relay, error) {
	r := Relay{Host: c.Host, Port: c.Port, Security: c.Security, AllowPrivate: c.AllowPrivate, Username: c.Username, HelloName: DomainOf(c.FromAddress)}
	if c.PasswordSealed != "" {
		p, err := box.Open(c.PasswordSealed, sealContext(c.Host, c.Port, c.Username))
		if err != nil {
			return r, err
		}
		r.Password = string(p)
		clear(p)
	}
	if c.TLSCAPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(c.TLSCAPEM)) {
			return r, errors.New("the relay's certificate authority cannot be read")
		}
		r.RootCAs = pool
	}
	return r, nil
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// CheckHost validates the relay's host: a name or an IP address, nothing else.
func CheckHost(h string) (string, error) {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" || len(h) > 253 {
		return "", errors.New("The relay's host is a name or an IP address, without a scheme, a port or a path.")
	}
	if ip := net.ParseIP(h); ip != nil {
		return h, nil
	}
	if !hostRe.MatchString(h) {
		return "", errors.New("The relay's host is a name or an IP address, without a scheme, a port or a path.")
	}
	return h, nil
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func defaultPort(sec string) int {
	switch sec {
	case SecTLS:
		return 465
	case SecNone:
		return 25
	}
	return 587
}

// checkCA checks an operator-supplied certificate authority: PEM, a few certificates, small.
func checkCA(pemText string) error {
	if len(pemText) > maxCAPEM {
		return errors.New("The certificate file is limited to 16 KB.")
	}
	if strings.Count(pemText, "BEGIN CERTIFICATE") > maxCACerts {
		return errors.New("At most eight certificates.")
	}
	if strings.Contains(pemText, "PRIVATE KEY") {
		return errors.New("That holds a private key. Paste only the certificate authority's certificate.")
	}
	if !x509.NewCertPool().AppendCertsFromPEM([]byte(pemText)) {
		return errors.New("That is not a PEM certificate.")
	}
	return nil
}

func printableLine(s string, max int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	return checkHeaderValue(s) == nil && !strings.ContainsRune(s, '\t')
}

// DefaultKindleDomains are the only domains a Send-to-Kindle address may have until an administrator widens the list.
var DefaultKindleDomains = []string{"kindle.com", "free.kindle.com"}

// AllowedKindleDomains is the list in force: the administrator's, or the default.
func (c Config) AllowedKindleDomains() []string {
	var out []string
	for _, d := range strings.Split(c.KindleDomains, ",") {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), DefaultKindleDomains...)
	}
	return out
}

// KindleAddressAllowed says whether an address is on one of the allowed domains (the domain itself, not a
// subdomain of it).
func (c Config) KindleAddressAllowed(addr string) bool {
	d := DomainOf(addr)
	for _, a := range c.AllowedKindleDomains() {
		if d == a {
			return true
		}
	}
	return false
}

// CheckKindleDomains validates an administrator's list: real domains only, no wildcard, no empty entry, never "any".
// An empty list means the default.
func CheckKindleDomains(in []string) (string, error) {
	if len(in) > 20 {
		return "", errors.New("At most 20 domains.")
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || strings.ContainsAny(d, "*@ ,") || !validDomain(d) {
			return "", errors.New("Each entry is one plain domain, like kindle.com. Wildcards and \"any\" are not allowed.")
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return strings.Join(out, ","), nil
}

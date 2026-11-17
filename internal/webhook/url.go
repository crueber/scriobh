// Package webhook sends signed notifications about a project to an address its
// owner chose: when a document is saved, a comment or suggestion is made, or an
// export finishes. It is an integration surface and nothing else: it never
// carries manuscript text (a comment's words go only when the owner asked, and
// are read at send time, never stored in the outbox), it never reaches an
// address the network guard refuses, and the writer never waits for it.
//
// How it works, in one paragraph. The save path writes the events it always
// wrote and gains nothing. A feeder tails the events table from a persisted
// cursor, turns each event of a subscribed type into one row per webhook in the
// outbox (webhook_deliveries) and moves the cursor in the same transaction. A
// dispatcher sends the due rows through the network guard, signs each body with
// HMAC-SHA256 under the webhook's secret, and records what happened: delivered,
// try again later (exponential backoff with jitter, a bounded number of times),
// or dead. Delivery is at least once; every attempt of a delivery carries the
// same id, so a receiver that stores the ids it has seen applies each event once.
package webhook

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/netguard"
)

// MaxURLLen bounds a webhook address.
const MaxURLLen = 2048

// ErrURL is what a refused address says; the message is for the person who typed it.
type ErrURL struct{ Msg string }

func (e *ErrURL) Error() string { return e.Msg }

func badURL(msg string) error { return &ErrURL{Msg: msg} }

// numericHost matches the spellings of an IPv4 address that are not the
// dotted-decimal one: a single decimal number (2130706433), hexadecimal
// (0x7f000001, 0x7f.0.0.1), octal (017700000001, 0177.0.0.1) and short forms
// (127.1). Some resolvers read them as addresses; here they are refused by shape
// so no resolver is asked, and the address that is finally dialled is checked
// again whatever the name was.
var numericHost = regexp.MustCompile(`^(0[xX][0-9a-fA-F]*|[0-9]+)(\.(0[xX][0-9a-fA-F]*|[0-9]+))*\.?$`)

var hostLabel = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9_])?$`)

// ValidateURL checks a webhook address as typed and returns it in canonical
// form. allowPrivate is the instance setting that lets a webhook reach this
// machine and its own network (and then allows plain http to them).
//
// This is the first of two checks. It reads the address; it cannot know where a
// name leads. The second is the dialer, which checks the address that is
// actually connected to.
func ValidateURL(raw string, allowPrivate bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", badURL("Give the address the webhook should call.")
	}
	if len(raw) > MaxURLLen {
		return "", badURL("The address is longer than 2048 characters.")
	}
	for _, r := range raw {
		if r <= 0x20 || r == 0x7f || r > 0x7e {
			return "", badURL("The address may not contain spaces, control characters or non-ASCII characters (write an international name in its xn-- form).")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", badURL("That is not a web address. Use something like https://example.com/hooks/scriobh.")
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case scheme == "https":
	case scheme == "http" && allowPrivate:
	case scheme == "http":
		return "", badURL("The address must start with https://. (An administrator can allow plain http to the local network with SCRIOBH_WEBHOOK_ALLOW_PRIVATE.)")
	default:
		return "", badURL("The address must start with https://.")
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		return "", badURL("The address may not carry a user name or password. Webhooks are authenticated by their signature.")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return "", badURL("The address may not have a # fragment.")
	}
	if u.Opaque != "" {
		return "", badURL("That is not a web address.")
	}
	host := u.Hostname()
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", badURL("The port must be between 1 and 65535.")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", badURL("The port is empty.")
	}
	if host == "" || len(host) > 253 {
		return "", badURL("That is not a web address.")
	}
	if strings.Contains(u.Host, "[") {
		ip := net.ParseIP(host)
		if ip == nil {
			return "", badURL("That is not a valid IPv6 address.")
		}
		if err := checkIP(ip, scheme, allowPrivate); err != nil {
			return "", err
		}
	} else if ip := net.ParseIP(host); ip != nil {
		if err := checkIP(ip, scheme, allowPrivate); err != nil {
			return "", err
		}
	} else {
		if numericHost.MatchString(host) {
			return "", badURL("Write an IPv4 address as four decimal numbers (203.0.113.7), or use a name.")
		}
		lower := strings.ToLower(strings.TrimSuffix(host, "."))
		if !allowPrivate && (lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal")) {
			return "", badURL("That name is for this machine or its own network. A webhook must be a public address. (An administrator can allow local targets with SCRIOBH_WEBHOOK_ALLOW_PRIVATE.)")
		}
		for _, l := range strings.Split(strings.TrimSuffix(host, "."), ".") {
			if !hostLabel.MatchString(l) {
				return "", badURL("That is not a valid host name.")
			}
		}
		if !strings.Contains(host, ".") && !allowPrivate {
			return "", badURL("Use a full host name such as hooks.example.com.")
		}
	}
	u.Scheme = scheme
	u.Host = strings.ToLower(u.Host)
	return u.String(), nil
}

// checkIP is the address-literal half of the rule: a literal that is internal is
// refused unless private targets are allowed, and one that is never allowed
// (link-local, which holds the cloud metadata address; reserved ranges) is
// refused always. Plain http is only for an address that is on this machine or
// its own network.
func checkIP(ip net.IP, scheme string, allowPrivate bool) error {
	mode := netguard.PublicOnly
	if allowPrivate {
		mode = netguard.AllowLocal
	}
	class := netguard.Classify(ip)
	if !mode.Allowed(class) {
		return badURL("That address is not one a webhook may call: it is internal, reserved or not routable.")
	}
	if scheme == "http" && class != netguard.Local {
		return badURL("Plain http is only for addresses on this machine or its own network. Use https://.")
	}
	return nil
}

// ErrBlocked is the sender's answer for an address the guard refused.
var ErrBlocked = netguard.ErrBlocked

var errNoSecretKey = errors.New("webhook: no secret key")

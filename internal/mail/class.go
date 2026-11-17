package mail

// Class says why a send failed. It is all that is kept: the words a relay used are never stored, logged or
// shown, because a server can say anything, including the credentials it was just sent.
type Class string

// The classes. Permanent ones are never retried.
const (
	ClassConfig      Class = "config"           // mail is not set up, or its password cannot be read
	ClassBlocked     Class = "blocked"          // the network guard refused the relay's address
	ClassDNS         Class = "dns"              // the relay's name did not resolve
	ClassRefused     Class = "refused"          // nothing answered
	ClassNetwork     Class = "network"          // the connection broke
	ClassTimeout     Class = "timeout"          // the relay was too slow
	ClassTLS         Class = "tls"              // the handshake failed
	ClassCert        Class = "tls_cert"         // the certificate was not accepted
	ClassNoStartTLS  Class = "starttls"         // the relay does not offer STARTTLS and plain text is not allowed
	ClassAuth        Class = "auth"             // the relay refused the credentials, or offers no way to sign in safely
	ClassTemporary   Class = "temporary"        // a 4xx: try again later
	ClassRejected    Class = "rejected"         // a 5xx: the relay will not take this message
	ClassTooLarge    Class = "too_large"        // over the relay's size, or ours
	ClassProtocol    Class = "protocol"         // the relay spoke out of turn, or too much
	ClassUnknown     Class = "delivery_unknown" // the relay may have taken it, and we do not know
	ClassExpired     Class = "expired"          // too old to be worth sending
	ClassCancelled   Class = "cancelled"        // the person, the project or the switch changed first
	ClassPaused      Class = "paused"           // mail was switched off
	ClassRateLimited Class = "rate_limited"
	ClassInternal    Class = "internal"
)

// Permanent says whether a message that failed this way is given up on at once.
func (c Class) Permanent() bool {
	switch c {
	case ClassRejected, ClassTooLarge, ClassUnknown, ClassExpired, ClassCancelled, ClassInternal:
		return true
	}
	return false
}

// ConfigLevel says whether the failure is about the relay's settings rather than the message: the dispatcher
// stops for a while instead of spending every waiting message's attempts on it.
func (c Class) ConfigLevel() bool {
	switch c {
	case ClassAuth, ClassCert, ClassTLS, ClassNoStartTLS, ClassBlocked, ClassConfig:
		return true
	}
	return false
}

// Error is a failed send. It carries a class and nothing a server said.
type Error struct {
	Class Class
	// AfterData is true when the message body had been sent and the relay's answer to it never came: the
	// message may have been accepted.
	AfterData bool
}

func (e *Error) Error() string { return "mail: " + string(e.Class) }

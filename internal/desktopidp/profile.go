package desktopidp

import (
	"crypto/rand"
	"encoding/hex"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Profile is the one local profile of a desktop instance: who the embedded issuer says is signing in. There is no
// password and no recovery: it is the person at this computer, under this account.
type Profile struct {
	// Subject is a random id made once and never changed: the "sub" of every ID token.
	Subject string `json:"sub"`
	Name    string `json:"name"`
	// Email is optional. When it is empty the ID token carries no address.
	Email     string `json:"email,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Limits of a profile's fields.
const (
	NameMax  = 80
	EmailMax = 254
)

// NewSubject makes a profile's subject: 128 random bits.
func NewSubject() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("desktopidp: entropy source failed")
	}
	return "local-" + hex.EncodeToString(b)
}

// CleanProfile trims and checks what a person typed. field is the field that is wrong ("name" or "email"), empty
// when both are good; problem is a short word for what is wrong ("required", "long", "invalid").
func CleanProfile(name, email string) (cleanName, cleanEmail, field, problem string) {
	cleanName = strings.Join(strings.Fields(name), " ")
	switch {
	case cleanName == "":
		return "", "", "name", "required"
	case utf8.RuneCountInString(cleanName) > NameMax || len(cleanName) > 4*NameMax:
		return "", "", "name", "long"
	}
	for _, r := range cleanName {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return "", "", "name", "invalid"
		}
	}
	cleanEmail = strings.TrimSpace(email)
	if cleanEmail == "" {
		return cleanName, "", "", ""
	}
	if len(cleanEmail) > EmailMax {
		return "", "", "email", "long"
	}
	a, err := mail.ParseAddress(cleanEmail)
	if err != nil || a.Address != cleanEmail || a.Name != "" || strings.ContainsAny(cleanEmail, " \t\r\n<>") {
		return "", "", "email", "invalid"
	}
	if _, domain, ok := strings.Cut(cleanEmail, "@"); !ok || domain == "" {
		return "", "", "email", "invalid"
	}
	return cleanName, strings.ToLower(cleanEmail), "", ""
}

package server

import (
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/ops"
)

// A credential pasted into an address field (https://user:secret@host/path) is a secret in a place that was never meant to
// hold one. These pieces make sure it is refused in plain words, never echoed back into a refused form, and never repeated in
// an error sentence: a value that carries one is left out of whatever a page shows again.

var (
	// scheme://something@ : any user part in the authority of a web, ssh or git address
	reAuthorityUser = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.\-]*://[^/\s?#]*@`)
	// user:password@host with no scheme
	reBareUserPass = regexp.MustCompile(`^[^/\s@:]+:[^/\s@]*@[^/\s]`)
)

// credentialsIn reports whether v looks like an address that carries a user name or password. ssh://git@host/... and
// git@host:path name the account Git logs in as, not a person's credential, and are not refused; a password in any address is.
func credentialsIn(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || !strings.Contains(v, "@") {
		return false
	}
	if reBareUserPass.MatchString(v) {
		return true
	}
	loc := reAuthorityUser.FindString(v)
	if loc == "" {
		return false
	}
	auth := loc[strings.Index(loc, "://")+3 : len(loc)-1]
	scheme := strings.ToLower(loc[:strings.Index(loc, "://")])
	if strings.Contains(auth, ":") {
		return true // user:password@
	}
	return scheme == "http" || scheme == "https" // a user name in a web address has no use here
}

// scrubValue leaves out a value that carries a credential.
func scrubValue(v string) string {
	if credentialsIn(v) {
		return ""
	}
	return v
}

// scrubValues does it for every value of a map that a page will show again.
func scrubValues(m map[string]string) map[string]string {
	for k, v := range m {
		if credentialsIn(v) {
			m[k] = ""
		}
	}
	return m
}

// refuseCredentials is the refusal for the first of the named fields that holds a credential, in the words of the catalogue,
// or nil. The refusal names the field and never the value.
func (s *Server) refuseCredentials(r *http.Request, fields ...string) error {
	for _, f := range fields {
		if credentialsIn(r.PostFormValue(f)) {
			return ops.Invalid(f, s.tr(r, "git.err.address_credentials"))
		}
	}
	return nil
}

// refuseAnyCredentials is refuseCredentials over every posted field but the ones that are secrets by design.
func (s *Server) refuseAnyCredentials(r *http.Request) error {
	keys := make([]string, 0, len(r.PostForm))
	for k := range r.PostForm {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch k {
		case "_csrf", "token", "password", "trigger_url":
			continue
		}
		if err := s.refuseCredentials(r, k); err != nil {
			return err
		}
	}
	return nil
}

// redactPosted takes anything that was posted with a credential in it out of a sentence an op wrote.
func redactPosted(r *http.Request, msg string) string {
	for _, vs := range r.PostForm {
		for _, v := range vs {
			if credentialsIn(v) {
				msg = strings.ReplaceAll(msg, strings.TrimSpace(v), "(address)")
			}
		}
	}
	return msg
}

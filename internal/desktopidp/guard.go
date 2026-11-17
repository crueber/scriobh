package desktopidp

import (
	"net"
	"net/http"
	"strings"
)

// HostAllowed reports whether a request's Host header names this instance: exactly the address it bound, with the
// port ("127.0.0.1:53124"), and nothing else. A page that reached the port through a name of its own (DNS rebinding)
// sends that name, and is refused. "localhost" is refused too: it can resolve to ::1, where this process does not
// listen, and the sign-in's addresses are all the bound one. base is the instance's own address
// ("http://127.0.0.1:53124").
func HostAllowed(host, base string) bool {
	_, hostport, ok := strings.Cut(base, "://")
	if !ok || hostport == "" || host != hostport {
		return false
	}
	bh, port, err := net.SplitHostPort(hostport)
	if err != nil || port == "" {
		return false
	}
	ip := net.ParseIP(bh)
	return ip != nil && ip.IsLoopback()
}

// OwnNavigation reports whether r can only have been made by this instance's own window or pages: the browser says
// the request comes from this origin (Sec-Fetch-Site: same-origin) or from the person or the shell directly (none),
// and an Origin header, when there is one, is this instance's. A page of another site, and a page on another port of
// this computer (same-site: a site ignores the port, and cookies do too), are refused whatever they try: a fetch, a
// frame, an image, a link. A program that is not a browser sends no such header, and has no cookie either.
//
// The kind of request (Sec-Fetch-Mode, Sec-Fetch-Dest) is not judged. A request from this origin is the application
// asking for itself, however it asks; and a browser's own service worker re-sends a navigation as a fetch, which
// WebKit marks "navigate" with the destination "empty" where Chromium says "document".
func OwnNavigation(r *http.Request, base string) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "none", "same-origin":
	default:
		return false
	}
	return ownOrigin(r, base)
}

// ownOrigin reports whether r has no Origin header or this instance's own.
func ownOrigin(r *http.Request, base string) bool {
	vals := r.Header.Values("Origin")
	if len(vals) == 0 {
		return true
	}
	return len(vals) == 1 && vals[0] == base
}

// OwnRequest is the same check under the name the token endpoint uses it by: a program's call, not a navigation.
func OwnRequest(r *http.Request, base string) bool { return OwnNavigation(r, base) }

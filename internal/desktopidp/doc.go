// Package desktopidp is the OpenID Connect issuer a desktop instance signs in through (SCRIOBH_DESKTOP=1).
//
// It is a real provider, for one client and one person: discovery, a key set, an authorization endpoint that takes
// the authorization-code flow with PKCE (S256), state and nonce, and a token endpoint for a public client. The
// application verifies its ID tokens with the same go-oidc code that verifies an external provider's, over loopback
// HTTP; nothing in the application's middleware knows that the issuer is its own.
//
// What makes it safe to run with no password is the launch gate (launch.go). The desktop shell makes a token for
// every launch, hands it to this process by environment and opens its own window at the start address with it. The
// token is good once; what the window gets for it is a cookie, and the authorization endpoint answers only a request
// that carries that cookie, names this instance's own host and is a navigation of this instance's own pages. Another
// program on the machine, another person's account and a web page in a browser can all reach the port; none of them
// has the cookie.
//
// The package has no dependency on the server: it is compiled into production builds (the development issuer in
// internal/oidcstub is not) and is tested on its own against a go-oidc client.
package desktopidp

//go:build !prod

package server

import (
	"net/http"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/oidcstub"
)

// devIdPAvailable reports whether this build contains the development issuer.
const devIdPAvailable = true

// mountDevIdP serves the development issuer on the app's own origin at
// /dev-idp/, so the issuer URL is identical from the browser and from the app.
// Production builds (-tags prod) compile dev_idp_prod.go instead and the
// issuer is not in the binary at all.
func (s *Server) mountDevIdP() {
	if !s.Cfg.OIDCStub {
		return
	}
	stub, err := oidcstub.New(oidcstub.Config{
		// The issuer URL depends on the bound port, which may not be known
		// until the listener exists, so it is resolved per request.
		IssuerFunc:      s.OIDC.Issuer,
		ClientID:        auth.StubClientID,
		ClientSecret:    auth.StubClientSecret,
		RedirectAllowed: func(u string) bool { return u == s.Cfg.OIDCCallbackURL() },
	})
	if err != nil {
		s.Log.Error("development issuer failed to start", "err", err)
		return
	}
	s.mux.Handle("/dev-idp/", http.StripPrefix(auth.StubIssuerPath, stub.Handler()))
}

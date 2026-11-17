//go:build !prod

package server

import (
	"log/slog"
	"os"

	"github.com/crueber/scriobh/internal/gitmirror"
)

// A development build trusts the certificate in the file SCRIOBH_DEV_TRUST_PEM names when it talks to a code host
// (scripts/fakehost.mjs serves one with a self-signed certificate). Production builds do not contain this file, and
// the setting is not in the configuration: it is read here, in a build that is never shipped.
func init() {
	path := os.Getenv("SCRIOBH_DEV_TRUST_PEM")
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err == nil {
		err = gitmirror.TrustForDevelopment(b)
	}
	if err != nil {
		slog.Error("SCRIOBH_DEV_TRUST_PEM is not usable", "err", err)
	}
}

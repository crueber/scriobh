//go:build !prod

package gitmirror

import (
	"crypto/x509"
	"errors"
)

// TrustForDevelopment makes both outbound HTTPS paths of this package, the host API client and the Git transport,
// trust the certificates in pem besides the system's. It exists so a development build can be pointed at a fake code
// host with a self-signed certificate in the browser tests (scripts/fakehost.mjs). Production builds (-tags prod) do not
// contain it, so a release cannot be told to trust anything the system does not.
func TrustForDevelopment(pem []byte) error {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return errors.New("no certificate found in the PEM")
	}
	providerRootCAs.Store(pool)
	httpTransport.TLSClientConfig.RootCAs = pool
	httpTransport.CloseIdleConnections()
	return nil
}

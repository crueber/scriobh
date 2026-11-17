// Command scriobh-dev-idp runs the development OIDC issuer standalone, for
// local runs and tests that want it on its own port. Development only.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/crueber/scriobh/internal/oidcstub"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9000", "address to listen on (loopback only)")
	issuer := flag.String("issuer", "", "issuer URL; default http://<listen>")
	clientID := flag.String("client-id", "scriobh-dev", "OIDC client id")
	secret := flag.String("client-secret", "scriobh-dev-secret", "OIDC client secret")
	flag.Parse()

	host, _, err := net.SplitHostPort(*listen)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		log.Fatal("scriobh-dev-idp only listens on loopback")
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	iss := *issuer
	if iss == "" {
		iss = "http://" + ln.Addr().String()
	}
	srv, err := oidcstub.New(oidcstub.Config{
		Issuer: strings.TrimRight(iss, "/"), ClientID: *clientID, ClientSecret: *secret,
		RedirectAllowed: func(u string) bool {
			p, err := url.Parse(u)
			if err != nil || p.User != nil || p.Scheme != "http" {
				return false
			}
			h := p.Hostname()
			return (h == "127.0.0.1" || h == "localhost" || h == "::1") && strings.HasSuffix(p.Path, "/auth/oidc/callback")
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("ISSUER %s\n", iss)
	log.Fatal(http.Serve(ln, srv.Handler()))
}

//go:build prod

package server

// devIdPAvailable is false: this build does not contain the development issuer.
const devIdPAvailable = false

// mountDevIdP is a no-op in production builds: the development issuer is not
// compiled in. Config validation refuses SCRIOBH_OIDC_STUB at startup.
func (s *Server) mountDevIdP() {}

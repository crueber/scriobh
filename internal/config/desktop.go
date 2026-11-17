package config

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strings"
)

// Desktop mode (SCRIOBH_DESKTOP=1). The process is started by a desktop shell as its sidecar: it listens on a loopback
// address only, it keeps its data in the operating system's place for application data, and people sign in through
// the embedded issuer (internal/desktopidp) as the one local profile. Nothing here is read when the variable is unset.

// DesktopListen is the listen address desktop mode uses when none is given: loopback, on a port the system picks.
const DesktopListen = "127.0.0.1:0"

// LaunchTokenMinLen is the shortest launch token accepted, in hexadecimal characters (16 bytes); the shell sends 64.
const LaunchTokenMinLen = 32

// launchTokenMaxLen bounds what is compared on every guess.
const launchTokenMaxLen = 256

// desktopDefaults applies the defaults of desktop mode to what Load read. lookup is the raw environment.
func (c *Config) desktopDefaults(lookup func(string) (string, bool), adminGroupSet bool) error {
	if !c.listenSet || c.Listen == "" {
		c.Listen = DesktopListen
	}
	if !c.dataDirSet || c.DataDir == "" {
		dir, err := DesktopDataDir(runtime.GOOS, lookup)
		if err != nil {
			return err
		}
		c.DataDir = dir
	}
	// The local profile is in no group, so the first-user rule decides who administers the instance: that rule only
	// applies when no administrators' group is named. An explicit group would lock the local profile out at its very
	// first sign-in, so Validate refuses it (the group that matters after a switch is the stored provider's own, set
	// in the settings, never the environment's).
	if adminGroupSet && c.AdminGroup != "" {
		c.desktopAdminGroupEnv = true
	}
	c.AdminGroup = ""
	return nil
}

// DesktopDataDir is where a desktop instance keeps its data when SCRIOBH_DATA_DIR is unset: the operating system's
// place for a program's own data. goos and lookup are parameters so that every platform's rule is tested on one.
func DesktopDataDir(goos string, lookup func(string) (string, bool)) (string, error) {
	get := func(k string) string {
		v, _ := lookup(k)
		return strings.TrimSpace(v)
	}
	home := get("HOME")
	switch goos {
	case "darwin":
		if home == "" {
			return "", errors.New("SCRIOBH_DESKTOP: HOME is not set, so the data directory cannot be found; set SCRIOBH_DATA_DIR")
		}
		return filepath.Join(home, "Library", "Application Support", "Scriobh"), nil
	case "windows":
		if d := get("APPDATA"); d != "" {
			return filepath.Join(d, "Scriobh"), nil
		}
		return "", errors.New("SCRIOBH_DESKTOP: APPDATA is not set, so the data directory cannot be found; set SCRIOBH_DATA_DIR")
	default:
		// The XDG rule: a relative XDG_DATA_HOME is invalid and is ignored.
		if d := get("XDG_DATA_HOME"); d != "" && filepath.IsAbs(d) {
			return filepath.Join(d, "scriobh"), nil
		}
		if home == "" {
			return "", errors.New("SCRIOBH_DESKTOP: neither XDG_DATA_HOME nor HOME is set, so the data directory cannot be found; set SCRIOBH_DATA_DIR")
		}
		return filepath.Join(home, ".local", "share", "scriobh"), nil
	}
}

// ValidLaunchToken reports whether t has the shape of a launch token: hexadecimal, at least LaunchTokenMinLen characters.
func ValidLaunchToken(t string) bool {
	if len(t) < LaunchTokenMinLen || len(t) > launchTokenMaxLen {
		return false
	}
	for _, r := range t {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// desktopProblems is Validate for desktop mode. It never repeats the token.
func (c *Config) desktopProblems() []string {
	var probs []string
	if !ValidLaunchToken(c.LaunchToken) {
		probs = append(probs, fmt.Sprintf("SCRIOBH_DESKTOP is on but SCRIOBH_LAUNCH_TOKEN is missing or is not at least %d hexadecimal characters; the desktop shell makes one for every launch, and the app does not start without it", LaunchTokenMinLen))
	}
	if !IsLoopbackIPListen(c.Listen) {
		probs = append(probs, "SCRIOBH_DESKTOP is on but SCRIOBH_LISTEN is not a loopback address (127.0.0.1:<port> or [::1]:<port>); a desktop instance is never reachable from the network")
	}
	if c.BaseURL != "" {
		probs = append(probs, "SCRIOBH_DESKTOP is on and SCRIOBH_BASE_URL is set; a desktop instance takes its address from the port it binds, so leave it unset")
	}
	if c.OIDCStub {
		probs = append(probs, "SCRIOBH_DESKTOP and SCRIOBH_OIDC_STUB are both on; a desktop instance signs in through its embedded issuer, never the development one")
	}
	if c.OIDCIssuer != "" || c.OIDCClientID != "" || c.OIDCClientSecret != "" {
		probs = append(probs, "SCRIOBH_DESKTOP is on and SCRIOBH_OIDC_ISSUER, SCRIOBH_OIDC_CLIENT_ID or SCRIOBH_OIDC_CLIENT_SECRET is set; a desktop instance is switched to an external provider by its administrator in the settings, not by the environment")
	}
	if c.desktopAdminGroupEnv {
		probs = append(probs, "SCRIOBH_DESKTOP is on and SCRIOBH_ADMIN_GROUP names a group; the local profile is in no group, so it could never sign in. Unset it (or set it empty); an administrators' group for an external provider is entered in the settings when the instance is switched")
	}
	if c.TrustProxy {
		probs = append(probs, "SCRIOBH_DESKTOP and SCRIOBH_TRUST_PROXY are both on; a desktop instance has no reverse proxy in front of it")
	}
	return probs
}

// IsLoopbackIPListen reports whether addr binds a literal loopback IP address. A name ("localhost") is not accepted:
// what it resolves to is not this program's to decide.
func IsLoopbackIPListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// DesktopEmbedded reports whether sign-in goes through the embedded issuer: desktop mode, not switched to an
// external provider.
func (c *Config) DesktopEmbedded() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Desktop && !c.DesktopExternal
}

// DesktopExternalIdP is what an administrator stored for a desktop instance that signs in through an external
// provider. The secret is already opened: it lives sealed in the database and only here in memory.
type DesktopExternalIdP struct {
	Issuer, ClientID, ClientSecret       string
	AdminGroup, WriterGroup, ReaderGroup string
}

// ApplyDesktopExternal switches a desktop Config to the external provider an administrator stored. It is called once,
// at start, after the database is open and before the server is built. It refuses outside desktop mode.
func (c *Config) ApplyDesktopExternal(x DesktopExternalIdP) error {
	if !c.Desktop {
		return errors.New("an external provider from the settings applies to desktop mode only")
	}
	if x.Issuer == "" || x.ClientID == "" {
		return errors.New("the stored external provider has no issuer or no client id")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.DesktopExternal = true
	c.OIDCIssuer, c.OIDCClientID, c.OIDCClientSecret = x.Issuer, x.ClientID, x.ClientSecret
	if x.AdminGroup != "" {
		c.AdminGroup = x.AdminGroup
	}
	if x.WriterGroup != "" {
		c.WriterGroup = x.WriterGroup
	}
	if x.ReaderGroup != "" {
		c.ReaderGroup = x.ReaderGroup
	}
	return nil
}

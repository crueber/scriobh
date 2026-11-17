package gitmirror

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"
	"golang.org/x/crypto/ssh"

	"github.com/crueber/scriobh/internal/netguard"
)

func init() {
	// The file:// scheme is served in process. go-git's own file transport runs
	// git-upload-pack and git-receive-pack, which the container does not have;
	// this one needs no binary. Addresses of that scheme are refused by
	// parseURL unless the service was built to allow them.
	client.InstallProtocol("file", server.NewClient(server.DefaultLoader))
	// Never let the server's ~/.ssh/config rewrite a host name or a port.
	gitssh.DefaultSSHConfig = nil
}

const (
	dialTimeout = 15 * time.Second
	scanTimeout = 12 * time.Second
	listTimeout = 30 * time.Second
)

// ---- SSH authentication, with the host key pinned ---------------------------

// pinnedSSH is the only SSH authentication Scríobh uses. Its configuration is
// complete, so go-git never falls back to ~/.ssh/known_hosts, the SSH agent or
// "accept anything": the host key must be the pinned one, offered with the
// pinned key's own algorithm.
type pinnedSSH struct {
	user    string
	signer  ssh.Signer
	host    string // host:port as pinned
	pinned  ssh.PublicKey
	timeout time.Duration
}

func (a *pinnedSSH) Name() string   { return "scriobh-pinned-ssh" }
func (a *pinnedSSH) String() string { return "scriobh-pinned-ssh " + a.user + "@" + a.host }

// ClientConfig implements go-git's ssh.AuthMethod.
func (a *pinnedSSH) ClientConfig() (*ssh.ClientConfig, error) {
	cfg := &ssh.ClientConfig{
		User:              a.user,
		Timeout:           a.timeout,
		HostKeyCallback:   a.check,
		HostKeyAlgorithms: algorithmsFor(a.pinned),
	}
	if a.signer != nil {
		cfg.Auth = []ssh.AuthMethod{ssh.PublicKeys(a.signer)}
	}
	return cfg, nil
}

func (a *pinnedSSH) check(_ string, _ net.Addr, key ssh.PublicKey) error {
	if a.pinned == nil {
		return &HostKeyError{Host: a.host, Presented: ssh.FingerprintSHA256(key), PresentedBy: key.Type()}
	}
	if !bytes.Equal(a.pinned.Marshal(), key.Marshal()) {
		return &HostKeyError{Changed: true, Host: a.host, Presented: ssh.FingerprintSHA256(key), Pinned: ssh.FingerprintSHA256(a.pinned), PresentedBy: key.Type()}
	}
	return nil
}

// algorithmsFor lists the signature algorithms the client offers for a host
// key. Restricting it to the pinned key's type is what stops a server that has
// several keys from looking like a changed key.
func algorithmsFor(k ssh.PublicKey) []string {
	if k == nil {
		return []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	if k.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	return []string{k.Type()}
}

// HostKey is one key a host presented.
type HostKey struct {
	Type        string `json:"type" desc:"The key type: ssh-ed25519, ecdsa-sha2-nistp256 or ssh-rsa."`
	Fingerprint string `json:"fingerprint" desc:"SHA256:... as shown by ssh-keygen -lf and by the host's documentation; compare it before accepting."`
	Key         string `json:"key" desc:"The key in authorized_keys form."`
}

func hostKeyOf(k ssh.PublicKey) HostKey {
	return HostKey{Type: k.Type(), Fingerprint: ssh.FingerprintSHA256(k), Key: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))}
}

// pinLine is how a pinned key is stored: "host:port key-type base64".
func pinLine(hostPort string, k ssh.PublicKey) string {
	return hostPort + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// parsePin reads a stored pin. ok is false for an empty or damaged value.
func parsePin(line string) (hostPort string, key ssh.PublicKey, ok bool) {
	hostPort, rest, found := strings.Cut(strings.TrimSpace(line), " ")
	if !found || hostPort == "" {
		return "", nil, false
	}
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(rest))
	if err != nil {
		return "", nil, false
	}
	return hostPort, k, true
}

var errScanDone = errors.New("host key captured")

// ScanHostKeys connects to host:port and returns the host keys it offers,
// without authenticating and without sending anything else: each handshake
// stops in the host key callback. The server is asked once per key type, since
// a handshake yields only the key of the algorithm it negotiates. Nothing is
// pinned by a scan.
func scanHostKeys(ctx context.Context, hostPort string) ([]HostKey, error) {
	var out []HostKey
	seen := map[string]bool{}
	var firstErr error
	for _, algs := range [][]string{{ssh.KeyAlgoED25519}, {ssh.KeyAlgoECDSA256}, {ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}} {
		var got ssh.PublicKey
		cfg := &ssh.ClientConfig{
			User:              "git",
			Timeout:           scanTimeout,
			HostKeyAlgorithms: algs,
			HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
				got = key
				return errScanDone
			},
		}
		cctx, cancel := context.WithTimeout(ctx, scanTimeout)
		netguard.Note("tcp", hostPort)
		conn, err := newDialer(scanTimeout).DialContext(cctx, "tcp", hostPort)
		if err != nil {
			cancel()
			if firstErr == nil {
				firstErr = err
			}
			// If the host cannot be reached at all, asking again will not help.
			if len(out) == 0 {
				return nil, err
			}
			continue
		}
		stop := context.AfterFunc(cctx, func() { _ = conn.Close() })
		c, chans, reqs, err := ssh.NewClientConn(conn, hostPort, cfg)
		stop()
		if err == nil { // cannot happen: the callback always errors
			_ = c.Close()
			_ = chans
			_ = reqs
		}
		_ = conn.Close()
		cancel()
		if got == nil {
			// The server does not offer this algorithm: normal.
			if firstErr == nil && !strings.Contains(fmt.Sprint(err), "no common algorithm") {
				firstErr = err
			}
			continue
		}
		hk := hostKeyOf(got)
		if !seen[hk.Fingerprint] {
			seen[hk.Fingerprint] = true
			out = append(out, hk)
		}
	}
	if len(out) == 0 {
		if firstErr == nil {
			firstErr = errors.New("the host offered no host key this client can use")
		}
		return nil, firstErr
	}
	return out, nil
}

// ---- deploy keys ------------------------------------------------------------

// generateKey makes an ed25519 deploy key. It returns the private key in
// OpenSSH PEM form and the public key as an authorized_keys line carrying the
// comment.
func generateKey(comment string) (privatePEM []byte, authorized string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	blk, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, "", err
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " " + comment
	return pem.EncodeToMemory(blk), line, nil
}

// ---- building the transport call -------------------------------------------

// credentials is what a transport call needs from a remote, opened for the
// length of one call. Its fields are secret: never log or format it.
type credentials struct {
	url     string // what go-git connects to: the remote's address, with an SSH host replaced by the checked IP
	auth    transport.AuthMethod
	scrub   []string // values to remove from any text that mentions them
	host    string
	keyName string
	token   string // the HTTPS token, for the host's API calls that follow a push
}

// credentialsFor opens the remote's secret and builds the auth for it.
func (s *Service) credentialsFor(ctx context.Context, r *remoteRow, ep endpointInfo) (*credentials, error) {
	cr := &credentials{host: ep.Host, keyName: r.Name, url: r.URL}
	if ep.Proto == "file" {
		return cr, nil
	}
	u, err := dialURL(ctx, r.URL, ep)
	if err != nil {
		return nil, err
	}
	cr.url = u
	if r.SecretCipher == "" {
		return nil, gerr(CodeCredentialsMissing, "No credentials are set for %q yet.", r.Name)
	}
	plain, err := s.o.Box.Open(r.SecretCipher, sealContext(r.ID, ep))
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	switch r.AuthKind {
	case AuthSSH:
		if ep.Proto != "ssh" {
			return nil, gerr(CodeBadURL, "A deploy key needs an ssh:// address.")
		}
		signer, err := ssh.ParsePrivateKey(plain)
		if err != nil {
			return nil, &Error{Code: CodeCredentialsBad, Message: fmt.Sprintf("The deploy key of %q cannot be read. Generate a new one.", r.Name), Host: ep.Host, KeyName: r.Name}
		}
		a := &pinnedSSH{user: ep.User, signer: signer, host: ep.HostPort(), timeout: dialTimeout}
		if _, k, ok := parsePin(r.KnownHosts); ok {
			a.pinned = k
		}
		cr.auth = a
	case AuthHTTPS:
		if ep.Proto != "https" {
			return nil, gerr(CodeBadURL, "A token needs an https:// address.")
		}
		tok := string(plain)
		user := r.Username
		if user == "" {
			user = "git"
		}
		cr.auth = &githttp.BasicAuth{Username: user, Password: tok}
		cr.scrub = append(cr.scrub, tok)
		cr.token = tok
	default:
		return nil, gerr(CodeInternal, "Unknown authentication kind.")
	}
	return cr, nil
}

// remoteHead asks the remote for its refs (ls-remote) and returns the head of
// the branch. exists is false when the branch is not there; empty is true when
// the repository has no refs at all.
func remoteHead(ctx context.Context, url string, cr *credentials, branch string) (head plumbing.Hash, exists, empty bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	rem := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{url}})
	refs, err := rem.ListContext(ctx, &git.ListOptions{Auth: cr.auth})
	if err != nil {
		if errors.Is(err, transport.ErrEmptyRemoteRepository) {
			return plumbing.ZeroHash, false, true, nil
		}
		return plumbing.ZeroHash, false, false, classify(err, cr.host, cr.keyName, cr.scrub...)
	}
	want := plumbing.NewBranchReferenceName(branch)
	for _, ref := range refs {
		if ref.Name() == want && ref.Type() == plumbing.HashReference {
			return ref.Hash(), true, false, nil
		}
	}
	return plumbing.ZeroHash, false, len(refs) == 0, nil
}

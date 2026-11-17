package gitmirror

import (
	"context"
	"crypto/rand"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
)

// Commit signing with the project's SSH key (F-364).
//
// The key is the project's, not a remote's: a deploy key belongs to one repository and is replaced when the
// address changes, while a signing key must stay the same for a reader to verify every commit with one
// allowed-signers line. It is an ed25519 key generated here (git_signing_keys, the private half sealed with the
// instance key and never returned). A remote with sign_commits adds a gpgsig header to each commit it makes,
// in the SSHSIG format git writes for gpg.format=ssh with namespace "git": git verify-commit and
// ssh-keygen -Y verify accept it with an allowed-signers file. Commits made before signing was switched on
// stay as they are; an unchanged tree makes no new commit, so the first signed commit comes with the next change.

const (
	sshsigNamespace = "git"
	sshsigHash      = "sha512"
)

// CodeSigningFailed is the typed failure of a push whose commit could not be signed.
const CodeSigningFailed = "signing_failed"

// SigningKey is the project's signing key as ops return it.
type SigningKey struct {
	PublicKey      string `json:"public_key" desc:"An authorized_keys line. Add it at the host as a signing key (GitHub: Settings, SSH and GPG keys, key type Signing key)."`
	Fingerprint    string `json:"fingerprint" desc:"SHA256:... as ssh-keygen shows it."`
	AllowedSigners string `json:"allowed_signers" desc:"A line for an allowed-signers file (git config gpg.ssh.allowedSignersFile) so that git verify-commit accepts the project's commits. The principal is * : any author email."`
	CreatedAt      string `json:"created_at"`
}

// SigningOut is the answer of the signing ops: the key, or none yet.
type SigningOut struct {
	Exists bool        `json:"exists" desc:"The project has a signing key."`
	Key    *SigningKey `json:"key,omitempty"`
}

// SigningGenerateIn makes the key.
type SigningGenerateIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	Replace   bool   `json:"replace,omitempty" desc:"Required when the project already has a key: commits signed with the old key no longer verify against the new allowed-signers line."`
}

func (in SigningGenerateIn) ProjectRef() string { return in.ProjectID }

func signingContext(projectID string) string { return "git_signing_keys/" + projectID + "/key" }

func registerSigning(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "git.signing.get", Method: "GET", Path: "/api/v1/projects/{id}/git/signing",
		Desc: "The project's commit-signing key: its public half, fingerprint and an allowed-signers line. Owner only. The private half is never returned.",
		Auth: ops.RequireProject(ops.ProjectOwner)}, getSigning)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "git.signing.generate", Method: "POST", Path: "/api/v1/projects/{id}/git/signing",
		Desc: "Owner only. Generate the project's ed25519 signing key (the private half is stored encrypted and never returned), or replace it with replace=true. A remote signs its commits only when sign_commits is on. Needs the server's secret key.",
		Auth: ops.RequireProject(ops.ProjectOwner)}, generateSigning)
}

func getSigning(c *ops.Ctx, in ProjectIn) (SigningOut, error) {
	s, err := service(c)
	if err != nil {
		return SigningOut{}, err
	}
	k, ok := s.signingKey(c, in.ProjectID)
	if !ok {
		return SigningOut{}, nil
	}
	return SigningOut{Exists: true, Key: &k}, nil
}

func generateSigning(c *ops.Ctx, in SigningGenerateIn) (SigningOut, error) {
	s, err := service(c)
	if err != nil {
		return SigningOut{}, err
	}
	if !s.Enabled() {
		return SigningOut{}, ops.Conflict(secrets.ErrNoKey.Error())
	}
	if _, ok := s.signingKey(c, in.ProjectID); ok && !in.Replace {
		return SigningOut{}, ops.Conflict("This project already has a signing key. Replacing it means commits signed with the old one no longer verify against the new allowed-signers line; send replace=true to do that.")
	}
	pem, pub, err := generateKey("scriobh-signing")
	if err != nil {
		return SigningOut{}, ops.Internal(err)
	}
	sealed, err := s.o.Box.Seal(pem, signingContext(in.ProjectID))
	clear(pem)
	if err != nil {
		return SigningOut{}, ops.Internal(err)
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		return SigningOut{}, ops.Internal(err)
	}
	now := stamp(s.o.Clock.Now())
	_, err = s.o.DB.Writer().ExecContext(c, `INSERT INTO git_signing_keys(project_id, public_key, fingerprint, key_sealed, created_by, created_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(project_id) DO UPDATE SET public_key = excluded.public_key, fingerprint = excluded.fingerprint, key_sealed = excluded.key_sealed, created_by = excluded.created_by, created_at = excluded.created_at`,
		in.ProjectID, pub, ssh.FingerprintSHA256(pk), sealed, nullIfEmpty(c.P.UserID), now)
	if err != nil {
		return SigningOut{}, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "git.signing.generate", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"replaced": in.Replace, "fingerprint": ssh.FingerprintSHA256(pk)}})
	k, _ := s.signingKey(c, in.ProjectID)
	return SigningOut{Exists: true, Key: &k}, nil
}

// signingKey reads the project's key (public parts only).
func (s *Service) signingKey(ctx context.Context, projectID string) (SigningKey, bool) {
	var k SigningKey
	err := s.o.DB.Read().QueryRowContext(ctx, `SELECT public_key, fingerprint, created_at FROM git_signing_keys WHERE project_id = ?`, projectID).Scan(&k.PublicKey, &k.Fingerprint, &k.CreatedAt)
	if err != nil {
		return SigningKey{}, false
	}
	pub := strings.Join(strings.Fields(k.PublicKey)[:2], " ")
	k.AllowedSigners = "* namespaces=\"" + sshsigNamespace + "\" " + pub
	return k, true
}

// signerFor opens the project's key for one push. The key is held in memory only for that push.
func (s *Service) signerFor(ctx context.Context, projectID string) (ssh.Signer, *Error) {
	var sealed string
	err := s.o.DB.Read().QueryRowContext(ctx, `SELECT key_sealed FROM git_signing_keys WHERE project_id = ?`, projectID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gerr(CodeSigningFailed, "This remote signs its commits but the project has no signing key. Generate one, or switch signing off.")
	}
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: "The signing key could not be read.", cause: err}
	}
	plain, err := s.o.Box.Open(sealed, signingContext(projectID))
	if err != nil {
		return nil, gerr(CodeSigningFailed, "The project's signing key cannot be read with this installation's secret key. Generate a new one.")
	}
	defer clear(plain)
	signer, err := ssh.ParsePrivateKey(plain)
	if err != nil {
		return nil, gerr(CodeSigningFailed, "The project's signing key cannot be read. Generate a new one.")
	}
	return signer, nil
}

func appendString(b []byte, s []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

// signSSH returns the armored SSH signature (the PROTOCOL.sshsig format) of data in the namespace "git".
func signSSH(signer ssh.Signer, data []byte) (string, error) {
	h := sha512.Sum512(data)
	// What is signed: the preamble, then namespace, reserved and hash algorithm as strings, then the hash.
	signed := []byte("SSHSIG")
	signed = appendString(signed, []byte(sshsigNamespace))
	signed = appendString(signed, nil)
	signed = appendString(signed, []byte(sshsigHash))
	signed = appendString(signed, h[:])
	sig, err := signer.Sign(rand.Reader, signed)
	if err != nil {
		return "", err
	}
	var sigBlob []byte
	sigBlob = appendString(sigBlob, []byte(sig.Format))
	sigBlob = appendString(sigBlob, sig.Blob)

	blob := []byte("SSHSIG")
	blob = binary.BigEndian.AppendUint32(blob, 1)
	blob = appendString(blob, signer.PublicKey().Marshal())
	blob = appendString(blob, []byte(sshsigNamespace))
	blob = appendString(blob, nil)
	blob = appendString(blob, []byte(sshsigHash))
	blob = appendString(blob, sigBlob)

	enc := base64.StdEncoding.EncodeToString(blob)
	var b strings.Builder
	b.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(enc) > 70 {
		b.WriteString(enc[:70])
		b.WriteByte('\n')
		enc = enc[70:]
	}
	b.WriteString(enc)
	b.WriteString("\n-----END SSH SIGNATURE-----")
	return b.String(), nil
}

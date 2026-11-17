package desktopidp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// KeyFormat is the version of the key file. A file of another format is replaced.
const KeyFormat = 1

// KeyDir and KeyFile say where the signing key lives under the data directory.
const (
	KeyDir  = "desktop"
	KeyFile = "issuer-key.json"
)

// keyFileMax bounds what is read of a key file: the real one is under a kilobyte.
const keyFileMax = 16 << 10

// Key is the issuer's signing key (ECDSA P-256, ES256).
type Key struct {
	Private *ecdsa.PrivateKey
	// ID is the key id in the token header and the key set.
	ID string
	// Rotated says that LoadKey made a new key (a first start, a new version, or a file it could not use).
	Rotated bool
}

type keyFile struct {
	Format     int    `json:"format"`
	AppVersion string `json:"app_version"`
	KeyID      string `json:"kid"`
	PKCS8      string `json:"pkcs8"`
	CreatedAt  string `json:"created_at"`
}

// LoadKey reads the signing key from dataDir/desktop/issuer-key.json, or makes one. The key is replaced when the
// file was written by another version of the application (appVersion) or in another format, or cannot be read as a
// key: an ID token lives for two minutes and is used at once, so nothing depends on an old key. The file and its
// folder are private to the account (0600 in 0700), and a file that has become readable by others is closed again.
func LoadKey(dataDir, appVersion string, now time.Time) (*Key, error) {
	dir := filepath.Join(dataDir, KeyDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("desktop issuer key: %w", err)
	}
	path := filepath.Join(dir, KeyFile)
	if k, err := readKey(path, appVersion); err == nil {
		return k, nil
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("desktop issuer key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("desktop issuer key: %w", err)
	}
	k := &Key{Private: priv, ID: keyID(priv), Rotated: true}
	raw, _ := json.Marshal(keyFile{Format: KeyFormat, AppVersion: appVersion, KeyID: k.ID,
		PKCS8: base64.StdEncoding.EncodeToString(der), CreatedAt: now.UTC().Format(time.RFC3339)})
	// Written whole to a private temporary file and renamed, so a crash never leaves half a key.
	tmp, err := os.CreateTemp(dir, ".issuer-key-*")
	if err != nil {
		return nil, fmt.Errorf("desktop issuer key: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("desktop issuer key: %w", err)
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("desktop issuer key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("desktop issuer key: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, fmt.Errorf("desktop issuer key: %w", err)
	}
	return k, nil
}

func readKey(path, appVersion string) (*Key, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, keyFileMax+1))
	if err != nil || len(raw) > keyFileMax {
		return nil, errors.New("unreadable or too large")
	}
	var kf keyFile
	if err := json.Unmarshal(raw, &kf); err != nil {
		return nil, err
	}
	if kf.Format != KeyFormat || kf.AppVersion != appVersion {
		return nil, errors.New("written by another version")
	}
	der, err := base64.StdEncoding.DecodeString(kf.PKCS8)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	priv, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || priv.Curve != elliptic.P256() {
		return nil, errors.New("not a P-256 key")
	}
	if st.Mode().Perm()&0o077 != 0 {
		_ = os.Chmod(path, 0o600) // no effect where the file system has no such modes
	}
	return &Key{Private: priv, ID: keyID(priv)}, nil
}

// keyID names a key by its public half, so a replaced key has another id.
func keyID(k *ecdsa.PrivateKey) string {
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return "desktop"
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:8])
}

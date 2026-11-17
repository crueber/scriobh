package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// KeyPrefix starts every API key, so a leaked one is recognisable by secret
// scanners and by eye.
const KeyPrefix = "scr_"

// keyTouchEvery limits how often a request writes last_used_at.
const keyTouchEvery = time.Minute

// NewKey makes a key secret: KeyPrefix and 32 random bytes. Only HashKey of it
// is ever stored, and display is the first twelve characters.
func NewKey() (secret, display, hash string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	secret = KeyPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return secret, secret[:12], HashKey(secret), nil
}

// HashKey is the stored form of a key secret.
func HashKey(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// ErrNoKey means the secret does not name a live key. Unknown, revoked and
// expired keys, and keys of removed users, all fail the same way.
var ErrNoKey = errors.New("no such key")

// KeyInfo describes the key behind a principal.
type KeyInfo struct {
	ID        string
	Prefix    string
	Name      string
	ExpiresAt string
}

// Keys finds API keys.
type Keys struct {
	DB *db.DB

	mu      sync.Mutex
	touched map[string]time.Time
}

// KeyFromRequest extracts a key secret from Authorization: Bearer or
// X-API-Key. present says the caller offered one, valid or not: a request that
// offers a key is judged by it and never falls back to a cookie.
func KeyFromRequest(authz, xkey string) (secret string, present bool) {
	if v := strings.TrimSpace(xkey); v != "" {
		return v, true
	}
	if len(authz) > 7 && strings.EqualFold(authz[:7], "Bearer ") {
		return strings.TrimSpace(authz[7:]), true
	}
	return "", false
}

// Lookup resolves a key secret to the principal it acts as: the key's owner,
// narrowed by the key's scope and pins.
func (k *Keys) Lookup(ctx context.Context, secret string) (ops.Principal, KeyInfo, error) {
	if !strings.HasPrefix(secret, KeyPrefix) || len(secret) > 128 {
		return ops.Principal{}, KeyInfo{}, ErrNoKey
	}
	now := db.Now()
	var (
		info                        KeyInfo
		uid, email, name, role      string
		scope                       string
		project, compendium, expiry sql.NullString
		isAgent                     int
	)
	err := k.DB.Read().QueryRowContext(ctx, `SELECT k.id, k.prefix, k.name, k.user_id, k.scope, k.project_id, k.compendium_id, k.is_agent, k.expires_at,
			COALESCE(u.email,''), u.display_name, u.role
		FROM api_keys k JOIN users u ON u.id = k.user_id
		WHERE k.secret_hash = ? AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > ?) AND u.deleted_at IS NULL AND u.disabled_at IS NULL`,
		HashKey(secret), now).Scan(&info.ID, &info.Prefix, &info.Name, &uid, &scope, &project, &compendium, &isAgent, &expiry, &email, &name, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return ops.Principal{}, KeyInfo{}, ErrNoKey
	}
	if err != nil {
		return ops.Principal{}, KeyInfo{}, err
	}
	info.ExpiresAt = expiry.String
	k.touch(ctx, info.ID)
	return ops.Principal{
		UserID: uid, Email: email, Name: name, Role: role, IsAgent: isAgent == 1, Via: "apikey",
		Scope: scope, ProjectID: project.String, CompendiumID: compendium.String, KeyID: info.ID,
	}, info, nil
}

// touch records that a key was just used, at most once a minute per key.
func (k *Keys) touch(ctx context.Context, id string) {
	k.mu.Lock()
	if k.touched == nil {
		k.touched = map[string]time.Time{}
	}
	last, seen := k.touched[id]
	now := time.Now()
	if seen && now.Sub(last) < keyTouchEvery {
		k.mu.Unlock()
		return
	}
	k.touched[id] = now
	k.mu.Unlock()
	_ = k.DB.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE api_keys SET last_used_at = ? WHERE id = ?`, db.Now(), id)
		return err
	})
}

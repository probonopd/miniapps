/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// Token is a long-lived access token; only its hash is stored.
type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Hash       string     `json:"hash"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type authFile struct {
	PasswordHash string  `json:"password_hash"`
	Tokens       []Token `json:"tokens"`
}

// AuthStore holds the single admin password and the API tokens.
type AuthStore struct {
	path string
	mu   sync.Mutex
	data authFile
}

func LoadAuth(path string) (*AuthStore, error) {
	a := &AuthStore{path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &a.data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return a, nil
}

func (a *AuthStore) save() error {
	raw, err := json.MarshalIndent(a.data, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(a.path, raw, 0o600)
}

const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
)

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func verifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (a *AuthStore) HasPassword() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.data.PasswordHash != ""
}

// SetPassword stores the admin password; an empty one clears it, which
// puts the API and UI into open mode (no login required).
func (a *AuthStore) SetPassword(pw string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if pw == "" {
		a.data.PasswordHash = ""
	} else {
		a.data.PasswordHash = hashPassword(pw)
	}
	return a.save()
}

func (a *AuthStore) CheckPassword(pw string) bool {
	a.mu.Lock()
	h := a.data.PasswordHash
	a.mu.Unlock()
	return h != "" && verifyPassword(h, pw)
}

func tokenHash(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// CreateToken returns the plain token exactly once.
func (a *AuthStore) CreateToken(name string) (string, Token, error) {
	if strings.TrimSpace(name) == "" {
		return "", Token{}, errors.New("token name must not be empty")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Token{}, err
	}
	plain := "miniha_" + base64.RawURLEncoding.EncodeToString(raw)
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return "", Token{}, err
	}
	t := Token{ID: hex.EncodeToString(id), Name: name, Hash: tokenHash(plain), CreatedAt: time.Now().UTC()}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.data.Tokens = append(a.data.Tokens, t)
	if err := a.save(); err != nil {
		return "", Token{}, err
	}
	return plain, t, nil
}

// ValidateToken looks a plain token up and records its use.
func (a *AuthStore) ValidateToken(plain string) (*Token, bool) {
	h := tokenHash(plain)
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.data.Tokens {
		if subtle.ConstantTimeCompare([]byte(a.data.Tokens[i].Hash), []byte(h)) == 1 {
			now := time.Now().UTC()
			// Last use is tracked in memory only; writing it on every
			// request would wear the SD card for nothing.
			a.data.Tokens[i].LastUsedAt = &now
			t := a.data.Tokens[i]
			return &t, true
		}
	}
	return nil, false
}

func (a *AuthStore) ListTokens() []Token {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Token, len(a.data.Tokens))
	copy(out, a.data.Tokens)
	for i := range out {
		out[i].Hash = ""
	}
	return out
}

func (a *AuthStore) DeleteToken(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, t := range a.data.Tokens {
		if t.ID == id {
			a.data.Tokens = append(a.data.Tokens[:i], a.data.Tokens[i+1:]...)
			return a.save()
		}
	}
	return fmt.Errorf("token %s not found", id)
}

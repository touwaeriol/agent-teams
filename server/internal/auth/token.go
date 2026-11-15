package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// NewToken returns a fresh token (secrets.token_urlsafe(32)) and the hash to store.
func NewToken() (token, hash string) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, HashToken(token)
}

// HashToken is sha256 hex, the v1 format of at_sessions.token_hash and at_nodes.token_hash.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RandomPassword is a readable one-time password for admin resets.
func RandomPassword() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

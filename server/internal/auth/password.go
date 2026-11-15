// Package auth holds the v1-compatible credential primitives: scrypt password
// hashes, opaque session/node tokens stored as sha256, the Fernet SecretBox and
// the signed OAuth state used by the GitHub redirects.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/scrypt"
)

// v1 parameters (agent_teams/server/security.py).
const (
	scryptN           = 1 << 14
	scryptR           = 8
	scryptP           = 1
	scryptKeyLen      = 64
	MinPasswordLength = 8
)

var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// HashPassword returns "scrypt$N$r$p$salt$digest" with unpadded urlsafe base64,
// exactly the v1 format, so Python could still verify it.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	digest, err := scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{"scrypt", strconv.Itoa(scryptN), strconv.Itoa(scryptR),
		strconv.Itoa(scryptP), b64(salt), b64(digest)}, "$"), nil
}

// VerifyPassword checks a password against a stored v1/v2 hash.
func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[0] != "scrypt" {
		return false
	}
	n, r, p, ok := scryptParams(parts[1:4])
	salt, err1 := unb64(parts[4])
	want, err2 := unb64(parts[5])
	if !ok || err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got, err := scrypt.Key([]byte(password), salt, n, r, p, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

func scryptParams(raw []string) (n, r, p int, ok bool) {
	vals := make([]int, 3)
	for i, s := range raw {
		v, err := strconv.Atoi(s)
		if err != nil || v <= 0 {
			return 0, 0, 0, false
		}
		vals[i] = v
	}
	return vals[0], vals[1], vals[2], true
}

// NormalizeEmail trims and lowercases, as v1 does.
func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// CheckCredentials returns why the credentials are unacceptable, or "".
func CheckCredentials(email, password string) string {
	if !emailPattern.MatchString(email) {
		return "请输入有效的邮箱地址"
	}
	return CheckPassword(password)
}

// CheckPassword returns why a new password is unacceptable, or "".
// Python's len() counts code points, so this does too.
func CheckPassword(password string) string {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return fmt.Sprintf("密码至少 %d 位", MinPasswordLength)
	}
	return ""
}

func b64(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }

func unb64(text string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(text, "="))
}

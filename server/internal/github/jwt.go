package github

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

// AppJWT signs a GitHub App JWT (RS256, valid ~9 minutes, backdated 60 s), like v1.
func AppJWT(appID int64, privateKey string, now time.Time) (string, error) {
	key, err := parseRSA(privateKey)
	if err != nil {
		return "", err
	}
	issued := now.Unix() - 60
	header := seg(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims := seg(map[string]any{"iat": issued, "exp": issued + 540, "iss": fmt.Sprint(appID)})
	input := header + "." + claims
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func seg(v any) string {
	raw, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func parseRSA(text string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(text))
	if block == nil {
		return nil, errors.New("GitHub App 私钥不是 PEM 格式")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("GitHub App 私钥无法解析：%w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GitHub App 私钥不是 RSA 密钥")
	}
	return key, nil
}

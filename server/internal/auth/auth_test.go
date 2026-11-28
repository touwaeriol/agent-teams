package auth

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type v1Vectors struct {
	Secret       string `json:"secret"`
	Plaintext    string `json:"plaintext"`
	Ciphertext   string `json:"ciphertext"`
	PemCipher    string `json:"pem_ciphertext"`
	Password     string `json:"password"`
	PasswordHash string `json:"password_hash"`
	Token        string `json:"token"`
	TokenHash    string `json:"token_hash"`
	StatePurpose string `json:"state_purpose"`
	StateUser    string `json:"state_user"`
	StateIssued  int64  `json:"state_issued"`
	State        string `json:"state"`
}

// testdata/v1_vectors.json was produced by the v1 Python modules
// (secrets.py, security.py, credentials.py); see README "v1 vectors".
func vectors(t *testing.T) v1Vectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/v1_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v v1Vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestOpensV1FernetCiphertext(t *testing.T) {
	v := vectors(t)
	box := NewSecretBox(v.Secret)
	got, err := box.Open(v.Ciphertext)
	if err != nil || got != v.Plaintext {
		t.Fatalf("open = %q, %v", got, err)
	}
	pem, err := box.Open(v.PemCipher)
	if err != nil || !strings.HasPrefix(pem, "-----BEGIN RSA PRIVATE KEY-----") {
		t.Fatalf("pem = %q, %v", pem, err)
	}
}

func TestWrongKeyFailsAndRotationOpens(t *testing.T) {
	v := vectors(t)
	if _, err := NewSecretBox("other").Open(v.Ciphertext); err != ErrUnseal {
		t.Fatalf("want ErrUnseal, got %v", err)
	}
	rotated := NewSecretBox("new-key", v.Secret)
	got, err := rotated.Open(v.Ciphertext)
	if err != nil || got != v.Plaintext {
		t.Fatalf("rotated open = %q, %v", got, err)
	}
	resealed, err := rotated.Reseal(v.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSecretBox("new-key").Open(resealed); err != nil {
		t.Fatalf("resealed value not readable with the new key: %v", err)
	}
}

func TestSealRoundTripAndNoSecret(t *testing.T) {
	box := NewSecretBox("k")
	sealed, err := box.Seal("hello")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := box.Open(sealed); got != "hello" {
		t.Fatalf("round trip %q", got)
	}
	if _, err := NewSecretBox().Seal("x"); err != ErrNoSecret {
		t.Fatalf("want ErrNoSecret, got %v", err)
	}
}

// Go-sealed values must open in v1 Python too (rollback safety).
func TestPythonOpensGoCiphertext(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	sealed, _ := NewSecretBox("go-secret").Seal("sealed-by-go")
	script := `import sys,base64,hashlib
from cryptography.fernet import Fernet
key=base64.urlsafe_b64encode(hashlib.sha256(sys.argv[1].encode()).digest())
print(Fernet(key).decrypt(sys.argv[2].encode()).decode(), end="")`
	out, err := exec.Command("python3", "-I", "-c", script, "go-secret", sealed).CombinedOutput()
	if strings.Contains(string(out), "No module named 'cryptography'") {
		t.Skip("python cryptography not installed")
	}
	if err != nil || string(out) != "sealed-by-go" {
		t.Fatalf("python decrypt: %q %v", out, err)
	}
}

func TestVerifiesV1PasswordHash(t *testing.T) {
	v := vectors(t)
	if !VerifyPassword(v.Password, v.PasswordHash) {
		t.Fatal("v1 hash did not verify")
	}
	if VerifyPassword("wrong password", v.PasswordHash) {
		t.Fatal("wrong password verified")
	}
}

func TestHashPasswordFormat(t *testing.T) {
	h, err := HashPassword("s3cret-pass")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(h, "$")
	if len(parts) != 6 || parts[0] != "scrypt" || parts[1] != "16384" || strings.Contains(h, "=") {
		t.Fatalf("format %q", h)
	}
	if !VerifyPassword("s3cret-pass", h) || VerifyPassword("x", h) {
		t.Fatal("verify mismatch")
	}
	for _, bad := range []string{"", "bcrypt$1$2$3$4$5", "scrypt$x$8$1$a$b", "scrypt$16384$8$1$!!$b"} {
		if VerifyPassword("x", bad) {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestTokenHashMatchesV1(t *testing.T) {
	v := vectors(t)
	if HashToken(v.Token) != v.TokenHash {
		t.Fatal("token hash differs from v1")
	}
	tok, hash := NewToken()
	if len(tok) != 43 || HashToken(tok) != hash {
		t.Fatalf("token %q", tok)
	}
}

func TestStateCompatibleWithV1(t *testing.T) {
	v := vectors(t)
	s := NewStateSigner(NewSecretBox(v.Secret))
	s.now = func() time.Time { return time.Unix(v.StateIssued+60, 0) }
	if got := s.Verify(v.StatePurpose, v.State); got != v.StateUser {
		t.Fatalf("verify v1 state = %q", got)
	}
	s.now = func() time.Time { return time.Unix(v.StateIssued, 0) }
	if s.Sign(v.StatePurpose, v.StateUser) != v.State {
		t.Fatal("signature differs from v1")
	}
	if s.Verify(PurposeCreate, v.State) != "" {
		t.Fatal("other purpose accepted")
	}
	s.now = func() time.Time { return time.Unix(v.StateIssued+3600, 0) }
	if s.Verify(v.StatePurpose, v.State) != "" {
		t.Fatal("expired state accepted")
	}
	for _, bad := range []string{"", "a.b", "u.-1.x", "u.+17.x"} {
		if s.Verify(v.StatePurpose, bad) != "" {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestCheckCredentials(t *testing.T) {
	if CheckCredentials("bad", "12345678") == nil || CheckCredentials("a@b.io", "1234567") == nil {
		t.Fatal("bad credentials accepted")
	}
	if CheckCredentials("a@b.io", "密码密码密码密码") != nil {
		t.Fatal("8 code points rejected")
	}
	if NormalizeEmail(" A@B.io ") != "a@b.io" {
		t.Fatal("normalize")
	}
}

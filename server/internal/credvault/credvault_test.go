package credvault

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

func vault(t *testing.T, secrets ...string) *Vault {
	t.Helper()
	v, err := New(secrets...)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSealOpen(t *testing.T) {
	v := vault(t, "k1")
	aad := ServerAAD("u1", "srv_1", AuthPassword, "example.com", 22, "deploy")
	s, err := v.Seal(aad, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(s.Ciphertext, []byte("hunter2")) || s.KEKID != v.CurrentKEKID() || len(s.KEKID) != 16 {
		t.Fatalf("sealed %+v", s)
	}
	got, err := v.Open(aad, s)
	if err != nil || string(got) != "hunter2" {
		t.Fatalf("open %q %v", got, err)
	}
	// Two seals of the same value differ (fresh DEK and nonces).
	s2, _ := v.Seal(aad, []byte("hunter2"))
	if bytes.Equal(s.Ciphertext, s2.Ciphertext) || bytes.Equal(s.WrappedDEK, s2.WrappedDEK) {
		t.Fatal("seal is deterministic")
	}
}

func TestOpenFailsInAnotherContext(t *testing.T) {
	v := vault(t, "k1")
	aad := ServerAAD("u1", "srv_1", AuthPassword, "example.com", 22, "deploy")
	s, err := v.Seal(aad, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	others := map[string][]byte{
		"moved to another row":  ServerAAD("u1", "srv_2", AuthPassword, "example.com", 22, "deploy"),
		"another user":          ServerAAD("u2", "srv_1", AuthPassword, "example.com", 22, "deploy"),
		"host changed":          ServerAAD("u1", "srv_1", AuthPassword, "evil.example", 22, "deploy"),
		"port changed":          ServerAAD("u1", "srv_1", AuthPassword, "example.com", 2222, "deploy"),
		"username changed":      ServerAAD("u1", "srv_1", AuthPassword, "example.com", 22, "root"),
		"auth kind changed":     ServerAAD("u1", "srv_1", AuthPrivateKey, "example.com", 22, "deploy"),
		"field boundary shifts": ServerAAD("u1", "srv_1", AuthPassword, "example.com|22", 0, "deploy"),
		"setting":               SettingAAD("ssh_session_ca"),
	}
	for why, aad := range others {
		if _, err := v.Open(aad, s); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: %v", why, err)
		}
	}
	tamper := func(f func(*Sealed)) error {
		c := s
		c.Ciphertext = bytes.Clone(s.Ciphertext)
		c.WrappedDEK = bytes.Clone(s.WrappedDEK)
		f(&c)
		_, err := v.Open(aad, c)
		return err
	}
	for why, f := range map[string]func(*Sealed){
		"ciphertext": func(c *Sealed) { c.Ciphertext[0] ^= 1 },
		"wrapped":    func(c *Sealed) { c.WrappedDEK[0] ^= 1 },
		"kek id":     func(c *Sealed) { c.KEKID = "0000000000000000" },
		"nonce":      func(c *Sealed) { c.Nonce = c.Nonce[:4] },
	} {
		if err := tamper(f); !errors.Is(err, ErrOpen) {
			t.Errorf("tampered %s: %v", why, err)
		}
	}
	// A different master key does not open it.
	if _, err := vault(t, "other").Open(aad, s); !errors.Is(err, ErrOpen) {
		t.Fatalf("other key: %v", err)
	}
}

func TestRotation(t *testing.T) {
	old := vault(t, "old")
	aad := ServerAAD("u1", "srv_1", AuthPassword, "h", 22, "u")
	s, err := old.Seal(aad, []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	both := vault(t, "new", "old")
	if got, err := both.Open(aad, s); err != nil || string(got) != "pw" {
		t.Fatalf("new,old opens old: %q %v", got, err)
	}
	fresh, changed, err := both.Rewrap(aad, s)
	if err != nil || !changed || fresh.KEKID != both.CurrentKEKID() || fresh.KEKID == s.KEKID {
		t.Fatalf("rewrap %v %v %+v", changed, err, fresh)
	}
	if !bytes.Equal(fresh.Ciphertext, s.Ciphertext) {
		t.Fatal("rewrap touched the ciphertext")
	}
	if _, changed, err := both.Rewrap(aad, fresh); err != nil || changed {
		t.Fatalf("second rewrap %v %v", changed, err)
	}
	// After the old key is dropped, the rewrapped value still opens, the original does not.
	newOnly := vault(t, "new")
	if got, err := newOnly.Open(aad, fresh); err != nil || string(got) != "pw" {
		t.Fatalf("new opens rewrapped: %q %v", got, err)
	}
	if _, err := newOnly.Open(aad, s); !errors.Is(err, ErrOpen) {
		t.Fatalf("new opens original: %v", err)
	}
	// Rewrap with the wrong AAD fails instead of re-wrapping garbage.
	if _, _, err := both.Rewrap(ServerAAD("u1", "srv_2", AuthPassword, "h", 22, "u"), s); !errors.Is(err, ErrOpen) {
		t.Fatalf("rewrap wrong aad: %v", err)
	}
}

func TestNoKey(t *testing.T) {
	v := vault(t, "", "")
	if v.Ready() {
		t.Fatal("ready without keys")
	}
	if _, err := v.Seal(nil, []byte("x")); !errors.Is(err, ErrNoKey) {
		t.Fatal(err)
	}
	if _, err := v.Open(nil, Sealed{}); !errors.Is(err, ErrNoKey) {
		t.Fatal(err)
	}
}

func TestMAC(t *testing.T) {
	data := HostKeyMACData("srv_1", "h", 22, "ssh-ed25519 AAAA")
	old := vault(t, "old")
	sum, err := old.MAC(data)
	if err != nil {
		t.Fatal(err)
	}
	if ok, cur := old.VerifyMAC(data, sum); !ok || !cur {
		t.Fatal("verify")
	}
	if ok, cur := vault(t, "new", "old").VerifyMAC(data, sum); !ok || cur {
		t.Fatal("rotated verify")
	}
	if ok, _ := old.VerifyMAC(HostKeyMACData("srv_1", "evil", 22, "ssh-ed25519 AAAA"), sum); ok {
		t.Fatal("mac accepted for another host")
	}
	if ok, _ := old.VerifyMAC(data, nil); ok {
		t.Fatal("empty mac accepted")
	}
}

func testKey(t *testing.T, passphrase string) string {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	var (
		block *pem.Block
		err   error
	)
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(priv, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

func TestCredential(t *testing.T) {
	v := vault(t, "k")
	aad := ServerAAD("u1", "srv_1", AuthPrivateKey, "h", 22, "u")
	key := testKey(t, "pp")
	if _, _, err := v.SealCredential(aad, AuthPrivateKey, Credential{PrivateKey: key}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("missing passphrase: %v", err)
	}
	if _, _, err := v.SealCredential(aad, AuthPrivateKey, Credential{PrivateKey: key, Passphrase: "wrong"}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if _, _, err := v.SealCredential(aad, AuthPrivateKey, Credential{PrivateKey: "not a key"}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("garbage key: %v", err)
	}
	if _, _, err := v.SealCredential(aad, AuthPassword, Credential{}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("empty password: %v", err)
	}
	if _, _, err := v.SealCredential(aad, "ca", Credential{Password: "x"}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("unknown kind: %v", err)
	}
	s, info, err := v.SealCredential(aad, AuthPrivateKey, Credential{PrivateKey: key, Passphrase: "pp"})
	if err != nil || !strings.HasPrefix(info.Fingerprint, "SHA256:") || !strings.HasPrefix(info.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("seal key: %+v %v", info, err)
	}
	c, err := v.OpenCredential(aad, s)
	if err != nil || c.PrivateKey != key || c.Passphrase != "pp" {
		t.Fatalf("open key: %v", err)
	}
	if _, err := c.Signer(); err != nil {
		t.Fatal(err)
	}
}

// memSettings is an in-memory SettingStore.
type memSettings struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (m *memSettings) Setting(_ context.Context, key string, dest any) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.m[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, dest)
}

func (m *memSettings) SaveSetting(_ context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.m[key] = raw
	return err
}

func (m *memSettings) InsertSettingIfAbsent(_ context.Context, key string, value any) (bool, error) {
	raw, err := json.Marshal(value)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.m[key]; ok {
		return false, err
	}
	m.m[key] = raw
	return true, err
}

func TestEnsureKey(t *testing.T) {
	ctx := context.Background()
	st := &memSettings{m: map[string][]byte{}}
	v := vault(t, "k")
	var wg sync.WaitGroup
	fps := make([]string, 8)
	for i := range fps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			signer, sk, err := EnsureKey(ctx, st, v, SessionCAKey)
			if err != nil {
				t.Error(err)
				return
			}
			if ssh.FingerprintSHA256(signer.PublicKey()) != sk.Fingerprint {
				t.Error("fingerprint")
			}
			fps[i] = sk.Fingerprint
		}()
	}
	wg.Wait()
	for _, fp := range fps {
		if fp != fps[0] {
			t.Fatalf("racing replicas got different keys %v", fps)
		}
	}
	if bytes.Contains(st.m[SessionCAKey], []byte("PRIVATE KEY")) {
		t.Fatal("private key stored in clear")
	}
	// The host key is a different key; the CA's sealed value does not open as the host key.
	_, hk, err := EnsureKey(ctx, st, v, GatewayHostKeyKey)
	if err != nil || hk.Fingerprint == fps[0] {
		t.Fatalf("host key %v %v", hk.Fingerprint, err)
	}
	st.m[GatewayHostKeyKey] = st.m[SessionCAKey]
	if _, _, err := EnsureKey(ctx, st, v, GatewayHostKeyKey); !errors.Is(err, ErrOpen) {
		t.Fatalf("swapped setting opened: %v", err)
	}
	// Reseal after a key rotation, then the old key can go.
	rotated := vault(t, "k2", "k")
	if changed, err := ResealKey(ctx, st, rotated, SessionCAKey); err != nil || !changed {
		t.Fatalf("reseal %v %v", changed, err)
	}
	if _, sk, err := EnsureKey(ctx, st, vault(t, "k2"), SessionCAKey); err != nil || sk.Fingerprint != fps[0] {
		t.Fatalf("after reseal %v %v", sk.Fingerprint, err)
	}
	if changed, err := ResealKey(ctx, st, rotated, "absent"); err != nil || changed {
		t.Fatalf("absent %v %v", changed, err)
	}
}

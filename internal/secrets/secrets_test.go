package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestFileStoreRoundTripAnd0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "credentials.json")
	s := NewFile(path)
	if _, err := s.Get("default", "api_token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	if err := s.Set("default", "api_token", "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("work", "oauth", `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	got, err := NewFile(path).Get("default", "api_token")
	if err != nil || got != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("round trip: %q %v", got, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", fi.Mode().Perm())
		}
	}
	if err := s.Delete("default", "api_token"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("default", "api_token"); err != nil {
		t.Fatalf("deleting a missing key is not an error: %v", err)
	}
	if _, err := s.Get("default", "api_token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if v, _ := s.Get("work", "oauth"); v != `{"a":1}` {
		t.Fatalf("other profile lost: %q", v)
	}
}

type failingKeyring struct{ calls int }

func (f *failingKeyring) Get(service, user string) (string, error) {
	f.calls++
	return "", errors.New("dbus: no session bus")
}
func (f *failingKeyring) Set(service, user, password string) error {
	f.calls++
	return errors.New("dbus: no session bus")
}
func (f *failingKeyring) Delete(service, user string) error {
	f.calls++
	return errors.New("dbus: no session bus")
}

type mapKeyring map[string]string

func (m mapKeyring) Get(service, user string) (string, error) {
	v, ok := m[service+"|"+user]
	if !ok {
		return "", errKeyringNotFound
	}
	return v, nil
}
func (m mapKeyring) Set(service, user, password string) error {
	m[service+"|"+user] = password
	return nil
}
func (m mapKeyring) Delete(service, user string) error {
	if _, ok := m[service+"|"+user]; !ok {
		return errKeyringNotFound
	}
	delete(m, service+"|"+user)
	return nil
}

func TestKeyringFailureFallsBackSilently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	kr := &failingKeyring{}
	s := newFallback(kr, NewFile(path))
	if err := s.Set("default", "login_api_token", "tok"); err != nil {
		t.Fatalf("set should fall back: %v", err)
	}
	if v, err := s.Get("default", "login_api_token"); err != nil || v != "tok" {
		t.Fatalf("get should fall back: %q %v", v, err)
	}
	if v, err := NewFile(path).Get("default", "login_api_token"); err != nil || v != "tok" {
		t.Fatalf("file holds the secret: %q %v", v, err)
	}
	if err := s.Delete("default", "login_api_token"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("default", "login_api_token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestKeyringUsedWhenAvailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	kr := mapKeyring{}
	s := newFallback(kr, NewFile(path))
	if err := s.Set("default", "api_token", "tok"); err != nil {
		t.Fatal(err)
	}
	if len(kr) != 1 {
		t.Fatalf("keyring not used: %v", kr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file must not be written when the keyring works")
	}
	if v, err := s.Get("default", "api_token"); err != nil || v != "tok" {
		t.Fatalf("get: %q %v", v, err)
	}
	// A secret written to the file earlier (keyring was down then) is still found.
	if err := NewFile(path).Set("default", "oauth", "x"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get("default", "oauth"); err != nil || v != "x" {
		t.Fatalf("file fallback read: %q %v", v, err)
	}
	if err := s.Delete("default", "oauth"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("default", "oauth"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete must clear the file copy too: %v", err)
	}
}

func TestMemoryStore(t *testing.T) {
	s := NewMemory()
	if _, err := s.Get("p", "k"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	_ = s.Set("p", "k", "v")
	if v, _ := s.Get("p", "k"); v != "v" {
		t.Fatal(v)
	}
	_ = s.Delete("p", "k")
	if _, err := s.Get("p", "k"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestDefaultHonorsNoKeyring(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUDD_CONFIG_DIR", dir)
	t.Setenv("AUDD_NO_KEYRING", "1")
	s := Default()
	if err := s.Set("default", "api_token", "tok"); err != nil {
		t.Fatal(err)
	}
	if v, err := NewFile(filepath.Join(dir, "credentials.json")).Get("default", "api_token"); err != nil || v != "tok" {
		t.Fatalf("AUDD_NO_KEYRING should use the file: %q %v", v, err)
	}
}

// sizeLimitedKeyring rejects values over a size limit, like Windows
// Credential Manager (2560 bytes), and otherwise works.
type sizeLimitedKeyring struct {
	mapKeyring
	limit int
}

func (s sizeLimitedKeyring) Set(service, user, password string) error {
	if len(password) > s.limit {
		return errors.New("The stub received bad data.")
	}
	return s.mapKeyring.Set(service, user, password)
}

func TestOversizedSetDoesNotDisableKeyring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	kr := sizeLimitedKeyring{mapKeyring: mapKeyring{}, limit: 16}
	s := newFallback(kr, NewFile(path))
	if err := s.Set("default", "api_token", "tok"); err != nil {
		t.Fatal(err)
	}
	// An old copy of oauth in the keyring must not shadow the new file copy.
	if err := s.Set("default", "oauth", "old"); err != nil {
		t.Fatal(err)
	}
	big := string(make([]byte, 100))
	if err := s.Set("default", "oauth", big); err != nil {
		t.Fatalf("oversized value falls back to the file: %v", err)
	}
	if v, err := s.Get("default", "oauth"); err != nil || v != big {
		t.Fatalf("oauth: %q %v", v, err)
	}
	if v, err := s.Get("default", "api_token"); err != nil || v != "tok" {
		t.Fatalf("keyring value still readable: %q %v", v, err)
	}
	if err := s.Delete("default", "api_token"); err != nil {
		t.Fatal(err)
	}
	if len(kr.mapKeyring) != 0 {
		t.Fatalf("delete must clear the keyring copy: %v", kr.mapKeyring)
	}
	if err := s.Delete("default", "oauth"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("default", "oauth"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

// Two processes writing different keys at the same time both keep their
// value: each File stands in for a separate process.
func TestFileConcurrentWritersMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	for round := 0; round < 100; round++ {
		a, b := NewFile(path), NewFile(path)
		ka, kb := fmt.Sprintf("a%d", round), fmt.Sprintf("b%d", round)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = a.Set("default", ka, "1") }()
		go func() { defer wg.Done(); _ = b.Set("other", kb, "2") }()
		wg.Wait()
		check := NewFile(path)
		if v, err := check.Get("default", ka); err != nil || v != "1" {
			t.Fatalf("round %d: lost %s: %q %v", round, ka, v, err)
		}
		if v, err := check.Get("other", kb); err != nil || v != "2" {
			t.Fatalf("round %d: lost %s: %q %v", round, kb, v, err)
		}
		wg.Add(2)
		go func() { defer wg.Done(); _ = a.Delete("default", ka) }()
		go func() { defer wg.Done(); _ = b.Set("default", "c", "3") }()
		wg.Wait()
		if _, err := check.Get("default", ka); !errors.Is(err, ErrNotFound) {
			t.Fatalf("round %d: delete lost: %v", round, err)
		}
		if v, _ := check.Get("default", "c"); v != "3" {
			t.Fatalf("round %d: set lost next to a delete", round)
		}
	}
}

// lockedDelete is a keyring that reads and writes but refuses to delete,
// like a locked keychain.
type lockedDelete struct{ mapKeyring }

func (lockedDelete) Delete(service, user string) error { return errors.New("keychain is locked") }

func TestDeleteReportsASecretLeftInTheKeyring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	kr := lockedDelete{mapKeyring{}}
	s := newFallback(kr, NewFile(path))
	if err := s.Set("default", "oauth", "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("default", "oauth"); !errors.Is(err, ErrKeyringDelete) {
		t.Fatalf("delete must say the secret is still there: %v", err)
	}
	if v, _ := kr.Get(service, user("default", "oauth")); v != "x" {
		t.Fatal("the keyring still has it")
	}
	// A keyring that never worked here holds nothing: deleting is quiet.
	f := newFallback(&failingKeyring{}, NewFile(path))
	if err := f.Delete("default", "oauth"); err != nil {
		t.Fatalf("unavailable keyring: %v", err)
	}
}

// Package secrets stores credentials in the OS credential store (macOS
// Keychain, Windows Credential Manager, Secret Service on Linux) and falls
// back silently to a 0600 JSON file in the config dir when that store is
// unavailable, as on headless servers and in containers.
//
// Keys in use: "api_token" (set with `audd config set token`),
// "login_api_token" (fetched by `audd login`), "oauth" (JSON of oauth.Tokens),
// "oauth_pending" (an unfinished login).
package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"

	"github.com/AudDMusic/audd-cli/internal/paths"
)

// ErrNotFound is returned by Get when no value is stored.
var ErrNotFound = errors.New("secret not found")

// Store keeps secrets per profile.
type Store interface {
	Get(profile, key string) (string, error) // ErrNotFound when absent
	Set(profile, key, value string) error
	Delete(profile, key string) error // deleting a missing key is not an error
}

const service = "audd-cli"

// Default returns the OS credential store with a silent fallback to
// <config dir>/credentials.json. AUDD_NO_KEYRING=1 uses the file only.
func Default() Store {
	file := NewFile(filepath.Join(paths.ConfigDir(), "credentials.json"))
	if os.Getenv("AUDD_NO_KEYRING") != "" {
		return file
	}
	return newFallback(osKeyring{}, file)
}

// keyringAPI is the subset of go-keyring the store needs (swappable in tests).
type keyringAPI interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

var errKeyringNotFound = keyring.ErrNotFound

type osKeyring struct{}

func (osKeyring) Get(s, u string) (string, error) { return keyring.Get(s, u) }
func (osKeyring) Set(s, u, p string) error        { return keyring.Set(s, u, p) }
func (osKeyring) Delete(s, u string) error        { return keyring.Delete(s, u) }

// fallback uses the keyring and, once reading or deleting from it fails, the
// file for the rest of the process. A failed Set (Windows Credential Manager
// rejects values over 2560 bytes) stores only that value in the file and
// keeps the keyring in use for the rest. Reads also consult the file so
// secrets written while the keyring was unavailable are still found.
type fallback struct {
	mu     sync.Mutex
	kr     keyringAPI
	file   *File
	broken bool
	// worked is set once the keyring answered a call in this process.
	worked bool
}

func newFallback(kr keyringAPI, file *File) *fallback {
	return &fallback{kr: kr, file: file}
}

func user(profile, key string) string { return profile + "/" + key }

func (f *fallback) useKeyring() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.broken
}

func (f *fallback) markWorked() {
	f.mu.Lock()
	f.worked = true
	f.mu.Unlock()
}

func (f *fallback) hasWorked() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.worked
}

func (f *fallback) markBroken() {
	f.mu.Lock()
	f.broken = true
	f.mu.Unlock()
}

func (f *fallback) Get(profile, key string) (string, error) {
	if f.useKeyring() {
		v, err := f.kr.Get(service, user(profile, key))
		switch {
		case err == nil:
			f.markWorked()
			return v, nil
		case errors.Is(err, errKeyringNotFound):
			f.markWorked()
		default:
			f.markBroken()
		}
	}
	return f.file.Get(profile, key)
}

func (f *fallback) Set(profile, key, value string) error {
	if f.useKeyring() {
		if err := f.kr.Set(service, user(profile, key), value); err == nil {
			f.markWorked()
			// Drop any stale copy left in the file by an earlier fallback.
			_ = f.file.Delete(profile, key)
			return nil
		}
		// Drop any older keyring copy so it cannot shadow the file copy.
		if err := f.kr.Delete(service, user(profile, key)); err != nil && !errors.Is(err, errKeyringNotFound) {
			f.markBroken()
		}
	}
	return f.file.Set(profile, key, value)
}

// ErrKeyringDelete means a secret is still in the system credential store
// because deleting it there failed (a locked keychain, a dismissed
// prompt). The file copy, if any, was deleted.
var ErrKeyringDelete = errors.New("could not remove it from the system credential store")

func (f *fallback) Delete(profile, key string) error {
	var krErr error
	if f.useKeyring() {
		if err := f.kr.Delete(service, user(profile, key)); err != nil && !errors.Is(err, errKeyringNotFound) {
			// A keyring that cannot be read either (no Secret Service on
			// this machine) never got the secret: use the file from now
			// on. One that still has the secret must not be reported as
			// cleared.
			_, gerr := f.kr.Get(service, user(profile, key))
			switch {
			case gerr == nil || f.hasWorked():
				krErr = fmt.Errorf("%w: %v", ErrKeyringDelete, err)
			case !errors.Is(gerr, errKeyringNotFound):
				f.markBroken()
			}
		}
	}
	if err := f.file.Delete(profile, key); err != nil {
		return err
	}
	return krErr
}

// File stores secrets in a JSON file readable only by the user (0600).
type File struct {
	mu   sync.Mutex
	path string
}

// NewFile returns a file-backed Store at path.
func NewFile(path string) *File { return &File{path: path} }

func (f *File) load() (map[string]map[string]string, error) {
	data := map[string]map[string]string{}
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return data, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return data, nil
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func (f *File) save(data map[string]map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(f.path, append(b, '\n'), 0o600)
}

func (f *File) Get(profile, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := f.load()
	if err != nil {
		return "", err
	}
	v, ok := data[profile][key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (f *File) Set(profile, key, value string) error {
	return f.update(func(data map[string]map[string]string) bool {
		if data[profile] == nil {
			data[profile] = map[string]string{}
		}
		data[profile][key] = value
		return true
	})
}

func (f *File) Delete(profile, key string) error {
	return f.update(func(data map[string]map[string]string) bool {
		if _, ok := data[profile][key]; !ok {
			return false
		}
		delete(data[profile], key)
		if len(data[profile]) == 0 {
			delete(data, profile)
		}
		return true
	})
}

// fileLockWait is how long a write waits for another audd process that
// is writing the credentials file.
const fileLockWait = 30 * time.Second

// update re-reads the file and applies change while holding a lock on
// <path>.lock that other audd processes also take, so writers that run at
// the same time merge their changes instead of overwriting each other.
func (f *File) update(change func(data map[string]map[string]string) bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	fl := flock.New(f.path + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), fileLockWait)
	defer cancel()
	ok, err := fl.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return fmt.Errorf("waiting for the lock on %s: %w", f.path, err)
	}
	if !ok {
		return fmt.Errorf("could not lock %s", f.path)
	}
	defer func() { _ = fl.Unlock() }()
	data, err := f.load()
	if err != nil {
		return err
	}
	if !change(data) {
		return nil
	}
	return f.save(data)
}

// WriteFileAtomic writes data to a temp file in the same dir and renames it
// over path, so readers never see a partial file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Chmod(perm) // CreateTemp already uses 0600; Chmod is a no-op on Windows
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Memory is an in-memory Store for tests.
type Memory struct {
	mu sync.Mutex
	m  map[string]string
}

// NewMemory returns an empty in-memory Store.
func NewMemory() *Memory { return &Memory{m: map[string]string{}} }

func (m *Memory) Get(profile, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.m[user(profile, key)]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (m *Memory) Set(profile, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.m[user(profile, key)] = value
	return nil
}

func (m *Memory) Delete(profile, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.m, user(profile, key))
	return nil
}

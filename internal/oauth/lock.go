package oauth

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// lockFile takes an exclusive lock on path, waiting until ctx ends.
func lockFile(ctx context.Context, path string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	fl := flock.New(path)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ok, err := fl.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("waiting for the sign-in lock %s: %w", path, err)
	}
	if !ok {
		return nil, fmt.Errorf("could not lock %s", path)
	}
	return func() { _ = fl.Unlock() }, nil
}

func listenLoopback() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

func portOf(ln net.Listener) int {
	return ln.Addr().(*net.TCPAddr).Port
}

// tryLockFile takes an exclusive lock on path without waiting; ok is false
// when another process holds it. unlock also removes the file.
func tryLockFile(path string) (unlock func(), ok bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	fl := flock.New(path)
	ok, err = fl.TryLock()
	if err != nil {
		return nil, false, fmt.Errorf("locking %s: %w", path, err)
	}
	if !ok {
		return nil, false, nil
	}
	return func() {
		_ = fl.Unlock()
		_ = os.Remove(path)
	}, true, nil
}

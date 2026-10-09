package testutil

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/update"
)

// ReleaseArchive builds the release archive for this system holding bin as
// the audd binary, and a checksums.txt that lists it.
func ReleaseArchive(t testing.TB, version string, bin []byte) (name string, archive, checksums []byte) {
	t.Helper()
	name = update.ArchiveName(version, runtime.GOOS, runtime.GOARCH)
	exe := update.BinaryName(runtime.GOOS)
	var b bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&b)
		w, err := zw.Create(exe)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(bin)
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(&b)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: exe, Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(bin)
		tw.Close()
		gz.Close()
	}
	sum := sha256.Sum256(b.Bytes())
	return name, b.Bytes(), []byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name))
}

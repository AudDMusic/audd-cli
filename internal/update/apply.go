package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ChecksumsName is the release file with the SHA-256 of every archive.
const ChecksumsName = "checksums.txt"

// maxArchiveBytes bounds a downloaded archive.
const maxArchiveBytes = 200 << 20

// ArchiveName is the release archive for a system:
// audd_<version>_<os>_<arch>.tar.gz, or .zip on Windows.
func ArchiveName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("audd_%s_%s_%s%s", strings.TrimPrefix(version, "v"), goos, goarch, ext)
}

// BinaryName is the executable inside the archive.
func BinaryName(goos string) string {
	if goos == "windows" {
		return "audd.exe"
	}
	return "audd"
}

// Apply downloads the release's archive for goos/goarch, checks it against
// checksums.txt, and replaces the binary at exe with the one inside.
func Apply(ctx context.Context, client *http.Client, rel *Release, exe, goos, goarch string) error {
	if client == nil {
		client = http.DefaultClient
	}
	name := ArchiveName(rel.Version, goos, goarch)
	archive, ok := rel.Asset(name)
	if !ok {
		return fmt.Errorf("%w (%s/%s, looked for %s)", ErrNoAsset, goos, goarch, name)
	}
	sums, ok := rel.Asset(ChecksumsName)
	if !ok {
		return fmt.Errorf("the release has no %s, so the download cannot be verified", ChecksumsName)
	}
	sumData, err := download(ctx, client, sums.URL, 1<<20)
	if err != nil {
		return err
	}
	want, err := checksumFor(sumData, name)
	if err != nil {
		return err
	}
	data, err := download(ctx, client, archive.URL, maxArchiveBytes)
	if err != nil {
		return err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("%s does not match its checksum; nothing was changed", name)
	}
	bin, err := extract(data, name, BinaryName(goos))
	if err != nil {
		return err
	}
	return replace(exe, bin, goos)
}

func download(ctx context.Context, client *http.Client, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: HTTP %d", path.Base(u), resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("downloading %s: the file is larger than expected", path.Base(u))
	}
	return b, nil
}

// checksumFor finds name's SHA-256 in a checksums file ("<hex>  <name>").
func checksumFor(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%s has no checksum for %s", ChecksumsName, name)
}

func extract(data []byte, archiveName, binName string) ([]byte, error) {
	if strings.HasSuffix(archiveName, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == binName && !f.FileInfo().IsDir() {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, maxArchiveBytes))
			}
		}
		return nil, fmt.Errorf("%s has no %s", archiveName, binName)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s has no %s", archiveName, binName)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == binName {
			return io.ReadAll(io.LimitReader(tr, maxArchiveBytes))
		}
	}
}

// replace swaps the binary at exe for bin. The new file is written next to
// it and renamed into place, so a failure leaves the old binary working.
func replace(exe string, bin []byte, goos string) error {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	dir := filepath.Dir(exe)
	mode := os.FileMode(0o755)
	if st, err := os.Stat(exe); err == nil {
		mode = st.Mode().Perm() | 0o111
	}
	tmp, err := os.CreateTemp(dir, ".audd-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if goos == "windows" {
		// A running .exe cannot be overwritten, but it can be renamed.
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(tmpName, exe); err != nil {
			_ = os.Rename(old, exe)
			return err
		}
		return nil
	}
	return os.Rename(tmpName, exe)
}

// RemoveOld deletes the previous binary that an update on Windows leaves at
// exe+".old" (a running .exe can be renamed but not deleted). Errors are
// ignored: the file may not exist, or may still be in use.
func RemoveOld(exe string) {
	_ = os.Remove(exe + ".old")
}

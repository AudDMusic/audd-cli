package art

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // decoders for cover art formats
	_ "image/jpeg" //
	_ "image/png"  //
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "golang.org/x/image/webp" // some artwork hosts serve webp

	"github.com/AudDMusic/audd-cli/internal/paths"
)

// ErrNoArt means the result has no cover art source (no Apple Music
// artwork and a song_link that is not on lis.tn).
var ErrNoArt = errors.New("no cover art for this result")

// HTTPClient fetches cover art. Tests replace it.
var HTTPClient = &http.Client{Timeout: 10 * time.Second}

// CacheDir is where fetched images are kept (CacheDir()/art by default).
var CacheDir = func() string { return filepath.Join(paths.CacheDir(), "art") }

// maxImageBytes caps a downloaded image.
const maxImageBytes = 8 << 20

// SourceURL returns the image URL for a result: the Apple Music artwork
// template with {w}x{h} set to size, else song_link with ?thumb when the
// link is on lis.tn, else "".
func SourceURL(songLink, appleArtworkURL string, size int) string {
	if appleArtworkURL != "" {
		s := strconv.Itoa(size)
		return strings.NewReplacer("{w}", s, "{h}", s, "{f}", "jpg", "{c}", "bb").Replace(appleArtworkURL)
	}
	u, err := url.Parse(songLink)
	if songLink == "" || err != nil || u.Hostname() != "lis.tn" {
		return ""
	}
	if u.RawQuery != "" {
		return songLink + "&thumb"
	}
	return songLink + "?thumb"
}

// Fetch downloads (or reads from the disk cache) the cover for a result
// and decodes it. size is the requested edge in pixels for Apple artwork.
func Fetch(ctx context.Context, songLink, appleArtworkURL string, size int) (image.Image, error) {
	src := SourceURL(songLink, appleArtworkURL, size)
	if src == "" {
		return nil, ErrNoArt
	}
	sum := sha256.Sum256([]byte(src))
	cached := filepath.Join(CacheDir(), hex.EncodeToString(sum[:16]))
	if b, err := os.ReadFile(cached); err == nil {
		if img, err := decode(b); err == nil {
			return img, nil
		}
	}
	b, err := download(ctx, src)
	if err != nil {
		return nil, err
	}
	img, err := decode(b)
	if err != nil {
		return nil, err
	}
	writeCache(cached, b)
	return img, nil
}

// maxImageEdge is the largest cover width or height decoded. A small file
// can declare huge dimensions; checking them first keeps the decoder from
// allocating gigabytes.
const maxImageEdge = 4096

func decode(b []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("cover art is not an image: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxImageEdge || cfg.Height > maxImageEdge {
		return nil, fmt.Errorf("cover art is %dx%d pixels, larger than %dx%d", cfg.Width, cfg.Height, maxImageEdge, maxImageEdge)
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("cover art is not an image: %w", err)
	}
	return img, nil
}

func download(ctx context.Context, src string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/*")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("cover art: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxImageBytes {
		return nil, errors.New("cover art image is too large")
	}
	return b, nil
}

// writeCache stores b atomically; failures only cost a later re-download.
func writeCache(path string, b []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".art-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
	}
}

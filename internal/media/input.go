// Package media turns command-line arguments into inputs and wraps the local
// audio tools (ffmpeg, ffprobe, sox).
//
// This file (inputs) belongs to the foundation; tools, trimming, capture,
// and durations live in the other files of the package.
package media

import (
	"bufio"
	"bytes"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// Input is one thing to recognize: a local file, a URL, or audio read from
// stdin (spooled to a temporary file at Path, with IsStdin set).
type Input struct {
	Path    string `json:"path,omitempty"`
	URL     string `json:"url,omitempty"`
	IsStdin bool   `json:"stdin,omitempty"`
}

// Name is how the input is shown to people: the URL, the path, or "stdin".
func (in Input) Name() string {
	switch {
	case in.URL != "":
		return in.URL
	case in.IsStdin:
		return "stdin"
	}
	return in.Path
}

// Cleanup removes the temporary file holding stdin audio. It does nothing
// for other inputs.
func (in Input) Cleanup() {
	if in.IsStdin && in.Path != "" {
		_ = os.Remove(in.Path)
	}
}

// mediaExtensions are the audio and video file types picked up when
// expanding directories and globs.
var mediaExtensions = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`mp3 wav wave flac ogg oga opus m4a m4b aac wma aif aiff aifc alac amr ape mka caf ac3 dts wv mpc spx
		mp4 m4v mov mkv webm avi wmv flv 3gp 3g2 mpeg mpg ts mts m2ts vob ogv`) {
		mediaExtensions["."+e] = true
	}
}

// IsMediaFile reports whether name has an audio or video extension.
func IsMediaFile(name string) bool {
	return mediaExtensions[strings.ToLower(filepath.Ext(name))]
}

func isURL(s string) bool {
	l := strings.ToLower(s)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

func usageErr(hint, format string, args ...any) error {
	return output.Errf(output.ExitUsage, "invalid_argument", hint, format, args...)
}

// ExpandInputs resolves recognize-style arguments:
//
//   - a URL (http/https) or an existing file: a single input;
//   - a directory: every audio/video file under it, recursively (hidden
//     files and dirs skipped);
//   - a glob ("*.mp3", "music/**/*.flac"): matching audio/video files;
//   - "-": stdin, either a list of files/URLs (one per line, # comments) or
//     audio bytes, which are spooled to a temporary file (call Cleanup);
//   - several arguments: all of the above, combined.
//
// batch is true for directories, globs, lists, and multiple arguments,
// even when they resolve to a single file. Duplicate paths are dropped.
func ExpandInputs(args []string, stdin io.Reader) (inputs []Input, batch bool, err error) {
	if len(args) == 0 {
		return nil, false, usageErr("audd recognize <file|url|dir|glob|->", "nothing to recognize: give a file, URL, folder, glob, or - for stdin")
	}
	stdinUses := 0
	for _, a := range args {
		if a == "-" {
			stdinUses++
		}
	}
	if stdinUses > 1 {
		return nil, false, usageErr("", "- (stdin) can be given only once")
	}
	batch = len(args) > 1
	// Audio read from stdin is spooled to a temporary file; when a later
	// argument fails, nobody gets the input to clean up, so remove it here.
	var spooled *Input
	defer func() {
		if err != nil && spooled != nil {
			spooled.Cleanup()
		}
	}()
	seen := map[string]bool{}
	add := func(in Input) {
		key := in.URL
		if key == "" {
			key = "file:" + filepath.Clean(in.Path)
			if abs, err := filepath.Abs(in.Path); err == nil {
				key = "file:" + abs
			}
		}
		if in.IsStdin || !seen[key] {
			seen[key] = true
			inputs = append(inputs, in)
		}
	}
	for _, a := range args {
		if a == "-" {
			list, audio, err := readStdin(stdin)
			if err != nil {
				return nil, false, err
			}
			if audio != nil {
				spooled = audio
				add(*audio)
				continue
			}
			batch = true
			for _, line := range list {
				got, _, err := expandOne(line)
				if err != nil {
					return nil, false, err
				}
				for _, in := range got {
					add(in)
				}
			}
			continue
		}
		got, multi, err := expandOne(a)
		if err != nil {
			return nil, false, err
		}
		if multi {
			batch = true
		}
		for _, in := range got {
			add(in)
		}
	}
	if len(inputs) == 0 {
		return nil, false, usageErr("", "no audio or video files found in %s", strings.Join(args, " "))
	}
	return inputs, batch, nil
}

// expandOne resolves one argument. multi reports a directory or glob.
func expandOne(a string) (inputs []Input, multi bool, err error) {
	if isURL(a) {
		return []Input{{URL: a}}, false, nil
	}
	if strings.Contains(a, "://") {
		return nil, false, usageErr("use an http:// or https:// URL", "unsupported URL %q", a)
	}
	fi, statErr := os.Stat(a)
	if statErr == nil {
		if fi.IsDir() {
			files, err := walkDir(a)
			if err != nil {
				return nil, true, err
			}
			if len(files) == 0 {
				return nil, true, usageErr("", "no audio or video files found in %s", a)
			}
			return files, true, nil
		}
		return []Input{{Path: a}}, false, nil
	}
	if hasMeta(a) {
		files, err := glob(a)
		if err != nil {
			return nil, true, err
		}
		if len(files) == 0 {
			return nil, true, usageErr("check the pattern; quote it so your shell does not expand it", "no audio or video files match %s", a)
		}
		return files, true, nil
	}
	return nil, false, usageErr("check the path", "no such file or folder: %s", a)
}

func walkDir(root string) ([]Input, error) {
	var out []Input
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil // unreadable entries are skipped
		}
		if p != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && IsMediaFile(p) {
			out = append(out, Input{Path: p})
		}
		return nil
	})
	if err != nil {
		return nil, usageErr("", "cannot read folder %s: %v", root, err)
	}
	return out, nil
}

func hasMeta(s string) bool { return strings.ContainsAny(s, "*?[") }

// glob matches pattern against files, with "**" matching any number of
// directories. Directories that match are expanded recursively.
func glob(pattern string) ([]Input, error) {
	slashed := filepath.ToSlash(pattern)
	segs := strings.Split(slashed, "/")
	// The static prefix (segments without glob characters) is where the walk starts.
	i := 0
	for i < len(segs) && !hasMeta(segs[i]) {
		i++
	}
	base := strings.Join(segs[:i], "/")
	switch {
	case base == "" && strings.HasPrefix(slashed, "/"):
		base = "/"
	case base == "":
		base = "."
	}
	rest := segs[i:]
	if _, err := path.Match(strings.Join(rest, "/"), ""); err != nil {
		return nil, usageErr("", "bad pattern %q: %v", pattern, err)
	}
	root := filepath.FromSlash(base)
	allowHidden := false
	for _, r := range rest {
		if strings.HasPrefix(r, ".") {
			allowHidden = true
		}
	}
	var out []Input
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		relp, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(relp), "/")
		if !allowHidden && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if matchSegs(rest, parts) {
			if d.IsDir() {
				files, _ := walkDir(p)
				out = append(out, files...)
				return filepath.SkipDir
			}
			if d.Type().IsRegular() && IsMediaFile(p) {
				out = append(out, Input{Path: p})
			}
			return nil
		}
		if d.IsDir() && !canDescend(rest, parts) {
			return filepath.SkipDir
		}
		return nil
	})
	sort.SliceStable(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out, nil
}

// matchSegs matches path segments against pattern segments ("**" = any depth).
func matchSegs(pat, parts []string) bool {
	if len(pat) == 0 {
		return len(parts) == 0
	}
	if pat[0] == "**" {
		for k := 0; k <= len(parts); k++ {
			if matchSegs(pat[1:], parts[k:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	ok, _ := path.Match(pat[0], parts[0])
	return ok && matchSegs(pat[1:], parts[1:])
}

// canDescend reports whether a directory at parts could contain a match.
func canDescend(pat, parts []string) bool {
	for i, p := range parts {
		if i >= len(pat) {
			return false
		}
		if pat[i] == "**" {
			return true
		}
		if ok, _ := path.Match(pat[i], p); !ok {
			return false
		}
	}
	return true
}

// readStdin decides whether stdin is a list of paths/URLs or audio data.
// Lists are UTF-8 text without NUL bytes; anything else is audio, spooled
// to a temporary file.
func readStdin(r io.Reader) (list []string, audio *Input, err error) {
	if r == nil {
		r = os.Stdin
	}
	br := bufio.NewReaderSize(r, 64*1024)
	head, _ := br.Peek(8192)
	if len(head) == 0 {
		return nil, nil, usageErr("pipe audio or a list of files into audd, e.g. ls *.mp3 | audd recognize - --max-files 50 --yes", "stdin is empty")
	}
	if looksLikeText(head) {
		sc := bufio.NewScanner(br)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			list = append(list, line)
		}
		if err := sc.Err(); err != nil {
			return nil, nil, err
		}
		if len(list) == 0 {
			return nil, nil, usageErr("", "the list on stdin has no files or URLs")
		}
		return list, nil, nil
	}
	f, err := os.CreateTemp("", "audd-stdin-*")
	if err != nil {
		return nil, nil, err
	}
	if _, err := io.Copy(f, br); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return nil, nil, err
	}
	return nil, &Input{Path: f.Name(), IsStdin: true}, nil
}

func looksLikeText(b []byte) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	// Allow a multi-byte rune cut off at the end of the peeked window.
	for i := 0; i < 3 && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	if !utf8.Valid(b) {
		return false
	}
	for _, c := range b {
		if c < 0x20 && c != '\n' && c != '\r' && c != '\t' {
			return false
		}
	}
	return true
}

package media

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/output"
)

func touch(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(root, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func paths(in []Input) []string {
	var out []string
	for _, i := range in {
		switch {
		case i.URL != "":
			out = append(out, i.URL)
		default:
			out = append(out, filepath.ToSlash(i.Path))
		}
	}
	return out
}

func rel(root string, ps []string) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		r, err := filepath.Rel(root, filepath.FromSlash(p))
		if err != nil || strings.HasPrefix(r, "..") {
			out[i] = p
			continue
		}
		out[i] = filepath.ToSlash(r)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func wantUsage(t *testing.T, err error) {
	t.Helper()
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Exit != output.ExitUsage {
		t.Fatalf("want usage error, got %v", err)
	}
}

func TestExpandSingleFileAndURL(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "song.mp3", "notes.txt")

	in, batch, err := ExpandInputs([]string{filepath.Join(root, "song.mp3")}, nil)
	if err != nil || batch || len(in) != 1 || in[0].Path != filepath.Join(root, "song.mp3") {
		t.Fatalf("single file: %+v %v %v", in, batch, err)
	}
	// An explicitly named file is accepted whatever its extension.
	if in, _, err := ExpandInputs([]string{filepath.Join(root, "notes.txt")}, nil); err != nil || len(in) != 1 {
		t.Fatalf("explicit non-audio file: %v", err)
	}
	in, batch, err = ExpandInputs([]string{"https://audd.tech/example.mp3"}, nil)
	if err != nil || batch || len(in) != 1 || in[0].URL != "https://audd.tech/example.mp3" || in[0].Path != "" {
		t.Fatalf("url: %+v %v %v", in, batch, err)
	}
	if in[0].Name() != "https://audd.tech/example.mp3" {
		t.Fatalf("name %q", in[0].Name())
	}
}

func TestExpandDirectoryRecursive(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "b.mp3", "a.FLAC", "sub/c.m4a", "sub/deeper/d.mp4", "cover.jpg", "readme.txt", ".hidden/e.mp3", "sub/.f.mp3")
	in, batch, err := ExpandInputs([]string{root}, nil)
	if err != nil || !batch {
		t.Fatalf("dir: %v %v", batch, err)
	}
	got := rel(root, paths(in))
	want := []string{"a.FLAC", "b.mp3", "sub/c.m4a", "sub/deeper/d.mp4"}
	if !equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestExpandGlob(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "x/1.mp3", "x/2.wav", "x/3.txt", "x/y/4.mp3", "z/5.mp3")
	in, batch, err := ExpandInputs([]string{filepath.Join(root, "x", "*.mp3")}, nil)
	if err != nil || !batch || !equal(rel(root, paths(in)), []string{"x/1.mp3"}) {
		t.Fatalf("glob: %v %v %v", paths(in), batch, err)
	}
	in, _, err = ExpandInputs([]string{filepath.Join(root, "**", "*.mp3")}, nil)
	if err != nil || !equal(rel(root, paths(in)), []string{"x/1.mp3", "x/y/4.mp3", "z/5.mp3"}) {
		t.Fatalf("doublestar: %v %v", paths(in), err)
	}
	// A glob that matches nothing is an error, not an empty batch.
	_, _, err = ExpandInputs([]string{filepath.Join(root, "*.ogg")}, nil)
	wantUsage(t, err)
}

func TestExpandMultipleArgsIsBatchAndDeduped(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "a.mp3", "b.mp3")
	a := filepath.Join(root, "a.mp3")
	in, batch, err := ExpandInputs([]string{a, filepath.Join(root, "b.mp3"), a, "https://audd.tech/x.mp3"}, nil)
	if err != nil || !batch || len(in) != 3 {
		t.Fatalf("multi: %v %v %v", paths(in), batch, err)
	}
}

func TestExpandStdinList(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "a.mp3", "dir/b.ogg")
	list := strings.Join([]string{
		"# my files",
		filepath.Join(root, "a.mp3"),
		"",
		"  https://audd.tech/x.mp3  ",
		filepath.Join(root, "dir"),
	}, "\n") + "\n"
	in, batch, err := ExpandInputs([]string{"-"}, strings.NewReader(list))
	if err != nil || !batch {
		t.Fatalf("list: %v %v", batch, err)
	}
	got := rel(root, paths(in))
	if !equal(got, []string{"a.mp3", "https://audd.tech/x.mp3", "dir/b.ogg"}) {
		t.Fatalf("got %v", got)
	}
}

func TestExpandStdinAudio(t *testing.T) {
	audio := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 64)...)
	in, batch, err := ExpandInputs([]string{"-"}, strings.NewReader(string(audio)))
	if err != nil || batch || len(in) != 1 || !in[0].IsStdin || in[0].Path == "" {
		t.Fatalf("stdin audio: %+v %v %v", in, batch, err)
	}
	defer in[0].Cleanup()
	b, err := os.ReadFile(in[0].Path)
	if err != nil || string(b) != string(audio) {
		t.Fatalf("spooled bytes differ: %v", err)
	}
	if in[0].Name() != "stdin" {
		t.Fatalf("name %q", in[0].Name())
	}
	in[0].Cleanup()
	if _, err := os.Stat(in[0].Path); !os.IsNotExist(err) {
		t.Fatal("cleanup should remove the spooled file")
	}
}

func TestExpandStdinAudioRemovedWhenALaterArgumentFails(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	audio := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 64)...)
	_, _, err := ExpandInputs([]string{"-", filepath.Join(t.TempDir(), "missing.mp3")}, strings.NewReader(string(audio)))
	wantUsage(t, err)
	left, _ := os.ReadDir(tmp)
	if len(left) != 0 {
		t.Fatalf("the spooled stdin audio was left behind: %v", left)
	}
}

func TestExpandErrors(t *testing.T) {
	_, _, err := ExpandInputs(nil, nil)
	wantUsage(t, err)
	_, _, err = ExpandInputs([]string{filepath.Join(t.TempDir(), "missing.mp3")}, nil)
	wantUsage(t, err)
	_, _, err = ExpandInputs([]string{t.TempDir()}, nil) // empty dir
	wantUsage(t, err)
	_, _, err = ExpandInputs([]string{"-"}, strings.NewReader(""))
	wantUsage(t, err)
	_, _, err = ExpandInputs([]string{"-", "-"}, strings.NewReader("x"))
	wantUsage(t, err)
	_, _, err = ExpandInputs([]string{"ftp://example.com/a.mp3"}, nil)
	wantUsage(t, err)
}

func TestIsMediaFile(t *testing.T) {
	for name, want := range map[string]bool{"a.mp3": true, "a.OPUS": true, "a.mkv": true, "a.txt": false, "mp3": false, "a.jpg": false} {
		if got := IsMediaFile(name); got != want {
			t.Errorf("IsMediaFile(%q)=%v", name, got)
		}
	}
}

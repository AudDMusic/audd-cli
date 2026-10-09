package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// fakeCapture records a stand-in WAV and remembers the request.
type fakeCapture struct {
	seconds int
	device  string
	path    string
	n       int
}

func (f *fakeCapture) install(t *testing.T) {
	t.Helper()
	oldFind, oldCap := listenFindTools, listenCapture
	listenFindTools = func() media.Tools { return media.Tools{FFmpeg: "/usr/bin/ffmpeg"} }
	listenCapture = func(ctx context.Context, tools media.Tools, seconds int, device string) (string, func(), error) {
		f.seconds, f.device = seconds, device
		f.path = filepath.Join(t.TempDir(), "audd-listen-test.wav")
		// Different bytes each time, so the results cache never answers.
		f.n++
		if err := os.WriteFile(f.path, []byte(fmt.Sprintf("RIFF%d", f.n)), 0o600); err != nil {
			return "", nil, err
		}
		return f.path, func() { os.Remove(f.path) }, nil
	}
	t.Cleanup(func() { listenFindTools, listenCapture = oldFind, oldCap })
}

func listenApp(t *testing.T, format output.Format) (*app.App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := testutil.Isolate(t)
	a := app.New()
	a.Cfg = config.Defaults(filepath.Join(dir, "config.toml"))
	a.Profile = a.Cfg.Profile(config.DefaultProfile)
	var out, errb bytes.Buffer
	a.Out = output.NewPrinter(&out, &errb, output.PrinterOptions{Format: format, NoColor: true})
	return a, &out, &errb
}

func TestListenDelegatesToRecognize(t *testing.T) {
	fc := &fakeCapture{}
	fc.install(t)
	a, _, errb := listenApp(t, output.FormatJSON)

	var gotArgs []string
	var gotReturn string
	var gotCtx context.Context
	root := &cobra.Command{Use: "audd"}
	root.AddGroup(&cobra.Group{ID: GroupRecognize, Title: "Recognition:"})
	rc := &cobra.Command{Use: "recognize", RunE: func(cmd *cobra.Command, args []string) error {
		gotArgs = args
		gotReturn, _ = cmd.Flags().GetString("return")
		gotCtx = cmd.Context()
		if _, err := os.Stat(args[0]); err != nil {
			t.Errorf("the recording should exist while recognize runs: %v", err)
		}
		return nil
	}}
	rc.Flags().String("return", "", "")
	listen := newListenCmd(a)
	root.AddCommand(rc, listen)
	root.SetArgs([]string{"listen", "--seconds", "20", "--device", "hw:1", "--return", "spotify", "--no-art"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errb.String(), "first 12 seconds") {
		t.Fatalf("recognize prints the 12-second note; listen should not repeat it: %q", errb)
	}
	if fc.seconds != 20 || fc.device != "hw:1" {
		t.Fatalf("capture %+v", fc)
	}
	if len(gotArgs) != 1 || gotArgs[0] != fc.path || gotReturn != "spotify" || gotCtx == nil {
		t.Fatalf("recognize got %v return=%q ctx=%v", gotArgs, gotReturn, gotCtx)
	}
	if _, err := os.Stat(fc.path); !os.IsNotExist(err) {
		t.Fatal("the recording should be deleted afterwards")
	}
}

func TestListenRecognizesAsMicrophone(t *testing.T) {
	fc := &fakeCapture{}
	fc.install(t)
	testutil.Isolate(t)
	t.Setenv("AUDD_API_TOKEN", testutil.PlaceholderToken)
	api := testutil.NewFakeAPI(t)
	api.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))

	r := testutil.Exec(t, Main, "", "listen", "--return", "apple_music", "--format", "json")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(r.Stdout), &doc); err != nil || doc["schema_version"] != float64(1) || doc["input"] != "microphone" {
		t.Fatalf("%v %q", err, r.Stdout)
	}
	if doc["result"].(map[string]any)["title"] != "Warriors" {
		t.Fatalf("%v", doc)
	}
	reqs := api.Requests()
	if len(reqs) != 1 || reqs[0].Form["return"] != "apple_music" {
		t.Fatalf("requests %+v", reqs)
	}
	if !strings.Contains(r.Stderr, "Listening for 10 seconds") {
		t.Fatalf("stderr %q", r.Stderr)
	}
	if _, err := os.Stat(fc.path); !os.IsNotExist(err) {
		t.Fatalf("the recording should be deleted: %v", err)
	}

	// No match: exit 0; --fail-on-no-match exits 1.
	api.Reply(testutil.EndpointRecognize, testutil.Success(nil))
	if r := testutil.Exec(t, Main, "", "listen"); r.Code != 0 || !strings.Contains(r.Stdout, `"result":null`) {
		t.Fatalf("exit %d: %q %s", r.Code, r.Stdout, r.Stderr)
	}
	if r := testutil.Exec(t, Main, "", "listen", "--fail-on-no-match"); r.Code != 1 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestListenWithoutTokenRecordsNothing(t *testing.T) {
	fc := &fakeCapture{}
	fc.install(t)
	testutil.Isolate(t)
	r := testutil.Exec(t, Main, "", "listen", "--format", "json")
	if r.Code != output.ExitAuth || !strings.Contains(r.Stderr, "no_token") || fc.n != 0 {
		t.Fatalf("exit %d, %d recordings: %s", r.Code, fc.n, r.Stderr)
	}
}

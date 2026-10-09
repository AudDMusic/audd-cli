package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/cli"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

func run(t *testing.T, args ...string) testutil.Result {
	t.Helper()
	return testutil.Exec(t, cli.Main, "", args...)
}

func TestVersionJSONGolden(t *testing.T) {
	testutil.Isolate(t)
	r := run(t, "version", "--format", "json")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	got := strings.NewReplacer(runtime.Version(), "GOVERSION", `"`+runtime.GOOS+`"`, `"OS"`, `"`+runtime.GOARCH+`"`, `"ARCH"`).Replace(r.Stdout)
	got = regexp.MustCompile(`"version":"[^"]+"`).ReplaceAllString(got, `"version":"VERSION"`)
	testutil.Golden(t, "version_json", []byte(got))
}

func TestVersionPipedDefaultsToJSONAndHumanLine(t *testing.T) {
	testutil.Isolate(t)
	r := run(t, "version")
	var v map[string]any
	if err := json.Unmarshal([]byte(r.Stdout), &v); err != nil || v["schema_version"].(float64) != 1 {
		t.Fatalf("piped output should be JSON: %q %v", r.Stdout, err)
	}
	r = run(t, "version", "--format", "table")
	if !strings.HasPrefix(r.Stdout, "audd "+app.Version+" (commit ") {
		t.Fatalf("human: %q", r.Stdout)
	}
	r = run(t, "--version")
	if !strings.HasPrefix(r.Stdout, "audd "+app.Version) {
		t.Fatalf("--version: %q", r.Stdout)
	}
}

func TestFormatFromEnvAndConfig(t *testing.T) {
	testutil.Isolate(t)
	if r := run(t, "config", "set", "format", "csv"); r.Code != 0 {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	r := run(t, "version")
	if !strings.HasPrefix(r.Stdout, "version,commit,date,go,os,arch\n") {
		t.Fatalf("config default format csv: %q", r.Stdout)
	}
	t.Setenv("AUDD_FORMAT", "jsonl")
	r = run(t, "version")
	if !strings.HasPrefix(r.Stdout, `{"schema_version":1,"type":"result",`) {
		t.Fatalf("env beats config: %q", r.Stdout)
	}
	r = run(t, "version", "--format", "json")
	if !strings.HasPrefix(r.Stdout, `{"schema_version":1,"version"`) {
		t.Fatalf("flag beats env: %q", r.Stdout)
	}
	r = run(t, "version", "--format", "yaml")
	if r.Code != output.ExitUsage {
		t.Fatalf("bad format exit %d", r.Code)
	}
}

func TestFieldsTrim(t *testing.T) {
	testutil.Isolate(t)
	r := run(t, "version", "--fields", "version,os")
	want := `{"schema_version":1,"version":"` + app.Version + `","os":"` + runtime.GOOS + `"}` + "\n"
	if r.Stdout != want {
		t.Fatalf("got %q want %q", r.Stdout, want)
	}
}

func TestConfigSetTokenThenListMasks(t *testing.T) {
	cfgDir := testutil.Isolate(t)
	r := run(t, "config", "set", "token", testutil.PlaceholderToken)
	if r.Code != 0 {
		t.Fatalf("set: %d %s", r.Code, r.Stderr)
	}
	if strings.Contains(r.Stdout+r.Stderr, testutil.PlaceholderToken) {
		t.Fatal("set must not echo the token")
	}
	r = run(t, "config", "list", "--format", "json")
	if r.Code != 0 {
		t.Fatalf("list: %d %s", r.Code, r.Stderr)
	}
	if strings.Contains(r.Stdout, testutil.PlaceholderToken) {
		t.Fatalf("list leaked the token: %s", r.Stdout)
	}
	var doc struct {
		SchemaVersion int                `json:"schema_version"`
		Profile       string             `json:"profile"`
		Settings      map[string]*string `json:"settings"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != 1 || doc.Profile != "default" || doc.Settings["token"] == nil || *doc.Settings["token"] != "0123…cdef" {
		t.Fatalf("list: %s", r.Stdout)
	}
	if doc.Settings["format"] != nil {
		t.Fatalf("unset keys are null: %s", r.Stdout)
	}
	// The token went to the secrets store, not config.toml.
	if b, _ := os.ReadFile(filepath.Join(cfgDir, "config.toml")); bytes.Contains(b, []byte(testutil.PlaceholderToken)) {
		t.Fatal("token written to config.toml")
	}
	r = run(t, "config", "list", "--format", "table")
	if !strings.Contains(r.Stdout, "0123…cdef") || strings.Contains(r.Stdout, testutil.PlaceholderToken) {
		t.Fatalf("table list: %q", r.Stdout)
	}
	r = run(t, "config", "get", "token", "--format", "table")
	if strings.TrimSpace(r.Stdout) != "0123…cdef" {
		t.Fatalf("get token: %q", r.Stdout)
	}
	if r := run(t, "config", "unset", "token"); r.Code != 0 {
		t.Fatal(r.Stderr)
	}
	r = run(t, "config", "get", "token")
	if !strings.Contains(r.Stdout, `"value":null`) {
		t.Fatalf("after unset: %q", r.Stdout)
	}
}

func TestConfigProfilesAndValidation(t *testing.T) {
	testutil.Isolate(t)
	if r := run(t, "config", "set", "max_requests", "250", "--profile", "work"); r.Code != 0 {
		t.Fatal(r.Stderr)
	}
	if r := run(t, "config", "get", "max_requests", "--profile", "work", "--format", "table"); strings.TrimSpace(r.Stdout) != "250" {
		t.Fatalf("work profile: %q", r.Stdout)
	}
	if r := run(t, "config", "get", "max_requests", "--format", "table"); strings.TrimSpace(r.Stdout) != "" {
		t.Fatalf("default profile must not see work settings: %q", r.Stdout)
	}
	for _, args := range [][]string{
		{"config", "set", "concurrency", "zero"},
		{"config", "set", "nope", "1"},
		{"config", "get", "nope"},
		{"config", "set", "format"},
		{"nosuchcommand"},
		{"version", "--nosuchflag"},
		{"completion", "tcsh"},
	} {
		r := run(t, args...)
		if r.Code != output.ExitUsage {
			t.Fatalf("%v: exit %d (%s)", args, r.Code, r.Stderr)
		}
		var doc struct {
			SchemaVersion int `json:"schema_version"`
			Error         struct{ Code string }
		}
		if err := json.Unmarshal([]byte(r.Stderr), &doc); err != nil || doc.SchemaVersion != 1 || doc.Error.Code == "" {
			t.Fatalf("%v: piped errors are JSON on stderr: %q", args, r.Stderr)
		}
	}
}

func TestHumanErrorOnTTY(t *testing.T) {
	testutil.Isolate(t)
	var out, errb bytes.Buffer
	code := cli.Run(context.Background(), []string{"config", "get", "nope"}, cli.IO{In: strings.NewReader(""), Out: &out, Err: &errb, StdoutTTY: true, StderrTTY: true})
	if code != output.ExitUsage {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(errb.String(), "Error: unknown config key \"nope\"\nTry: keys: token, format") {
		t.Fatalf("human error: %q", errb.String())
	}
}

func TestCompletion(t *testing.T) {
	testutil.Isolate(t)
	for _, sh := range []string{"bash", "zsh", "fish", "powershell"} {
		r := run(t, "completion", sh)
		if r.Code != 0 || !strings.Contains(r.Stdout, "audd") {
			t.Fatalf("%s: %d %q", sh, r.Code, r.Stderr)
		}
	}
}

func TestHelpListsGroups(t *testing.T) {
	testutil.Isolate(t)
	r := run(t, "--help")
	for _, s := range []string{"History, jobs, and settings:", "Other:", "config", "version", "dashboard.audd.io", "--max-requests"} {
		if !strings.Contains(r.Stdout, s) {
			t.Fatalf("help lacks %q:\n%s", s, r.Stdout)
		}
	}
	if strings.Contains(r.Stdout, "-v, --version") {
		t.Fatal("-v is reserved for --details in recognize")
	}
	if strings.Contains(strings.ToLower(r.Stdout), "lyrics") {
		t.Fatal("help must not mention lyrics")
	}
}

var registered bool

func init() {
	cli.Register(func(root *cobra.Command, a *app.App) {
		registered = true
		cmd := &cobra.Command{
			Use:         "test-stream",
			Hidden:      true,
			Annotations: map[string]string{cli.AnnotationStreaming: "true"},
			RunE: func(cmd *cobra.Command, args []string) error {
				if a.Profile == nil || a.Secrets == nil || a.Out == nil || a.Cfg == nil {
					return output.Errf(output.ExitUnexpected, "unset", "", "app not filled in")
				}
				if err := a.Out.Event("result", map[string]any{"n": 1, "max": a.Flags.MaxRequests}); err != nil {
					return err
				}
				return a.Out.Confirm("Spend requests?", a.Flags.Yes)
			},
		}
		root.AddCommand(cmd, &cobra.Command{
			Use:    "test-boom",
			Hidden: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				return errors.New("boom")
			},
		})
	})
}

func TestUnexpectedErrorExit1(t *testing.T) {
	testutil.Isolate(t)
	r := run(t, "test-boom")
	if r.Code != output.ExitUnexpected || !strings.Contains(r.Stderr, `"code":"unexpected"`) || !strings.Contains(r.Stderr, "boom") {
		t.Fatalf("plain error from RunE: %d %q", r.Code, r.Stderr)
	}
}

func TestRegisterStreamingAndConfirm(t *testing.T) {
	testutil.Isolate(t)
	if r := run(t, "config", "set", "max_requests", "7"); r.Code != 0 {
		t.Fatal(r.Stderr)
	}
	r := run(t, "test-stream", "--yes")
	if !registered || r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if r.Stdout != `{"schema_version":1,"type":"result","max":7,"n":1}`+"\n" {
		t.Fatalf("streaming command piped should be jsonl with config max_requests: %q", r.Stdout)
	}
	r = run(t, "test-stream", "--max-requests", "3", "--yes")
	if !strings.Contains(r.Stdout, `"max":3`) {
		t.Fatalf("flag beats config: %q", r.Stdout)
	}
	r = run(t, "test-stream")
	if r.Code != output.ExitSafety || !strings.Contains(r.Stderr, "confirmation_required") {
		t.Fatalf("no tty, no --yes: %d %q", r.Code, r.Stderr)
	}
}

func TestBrokenConfigIsUsageError(t *testing.T) {
	cfg := testutil.Isolate(t)
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("= broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run(t, "config", "list")
	if r.Code != output.ExitUsage || !strings.Contains(r.Stderr, "invalid_config") || !strings.Contains(r.Stderr, "config.toml") {
		t.Fatalf("broken config: %d %q", r.Code, r.Stderr)
	}
	// version and completion do not need the config.
	if r := run(t, "version"); r.Code != 0 || !strings.Contains(r.Stdout, `"version"`) {
		t.Fatalf("version with a broken config: %d %q", r.Code, r.Stderr)
	}
	if r := run(t, "completion", "bash"); r.Code != 0 {
		t.Fatalf("completion with a broken config: %d %q", r.Code, r.Stderr)
	}
}

func TestUnreadableConfigIsUnexpected(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file permissions do not block reads here")
	}
	cfg := testutil.Isolate(t)
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("format = \"json\"\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	r := run(t, "config", "list")
	if r.Code != output.ExitUnexpected || !strings.Contains(r.Stderr, `"code":"unexpected"`) {
		t.Fatalf("unreadable config: %d %q", r.Code, r.Stderr)
	}
}

func TestConfigSetTokenFromStdin(t *testing.T) {
	testutil.Isolate(t)
	r := testutil.Exec(t, cli.Main, testutil.PlaceholderToken+"\n", "config", "set", "token", "-")
	if r.Code != 0 {
		t.Fatalf("set from stdin: %d %s", r.Code, r.Stderr)
	}
	if strings.Contains(r.Stdout+r.Stderr, testutil.PlaceholderToken) {
		t.Fatal("set must not echo the token")
	}
	r = run(t, "config", "get", "token", "--format", "table")
	if strings.TrimSpace(r.Stdout) != "0123…cdef" {
		t.Fatalf("get token: %q", r.Stdout)
	}
	r = testutil.Exec(t, cli.Main, "  \n", "config", "set", "token", "-")
	if r.Code != output.ExitUsage {
		t.Fatalf("empty stdin: %d %s", r.Code, r.Stderr)
	}
}

func TestUsageHintNamesTheCommand(t *testing.T) {
	testutil.Isolate(t)
	r := run(t, "config", "set", "format")
	if !strings.Contains(r.Stderr, `"hint":"audd config set --help"`) {
		t.Fatalf("hint: %s", r.Stderr)
	}
}

// A mistyped subcommand of a group is a usage error, not the group's help.
func TestUnknownSubcommandIsUsageError(t *testing.T) {
	testutil.Isolate(t)
	for _, args := range [][]string{
		{"streams", "nonsense"}, {"jobs", "nonsense"}, {"billing", "nonsense"},
		{"auth", "nonsense"}, {"config", "nonsense"}, {"token", "nonsense"},
		{"cache", "nonsense"}, {"catalog", "nonsense"},
		{"streams", "recorder", "nonsense"}, {"streams", "callback", "nonsense"},
		{"streams", "histroy", "--since", "24h"},
		{"streams", "--format", "json", "histroy"},
	} {
		r := run(t, args...)
		if r.Code != output.ExitUsage || r.Stdout != "" || errCode(t, r) != "invalid_argument" {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, r.Code, r.Stdout, r.Stderr)
		}
	}
	r := run(t, "streams", "histroy", "--since", "24h")
	if !strings.Contains(r.Stderr, `unknown command \"histroy\"`) || !strings.Contains(r.Stderr, "audd streams history") {
		t.Errorf("names the command and suggests the right one: %s", r.Stderr)
	}
	// A group with no arguments, or asked for help, still prints its help.
	for _, args := range [][]string{{"streams"}, {"streams", "--help"}, {"streams", "nonsense", "--help"}, {"jobs", "--format", "json"}} {
		if r := run(t, args...); r.Code != 0 || !strings.Contains(r.Stdout, "Available Commands") {
			t.Errorf("%v: exit %d %q %q", args, r.Code, r.Stdout, r.Stderr)
		}
	}
}

func TestCobraErrorsAreOneLineWithANextStep(t *testing.T) {
	testutil.Isolate(t)
	cases := []struct {
		args      []string
		msg, hint string
	}{
		{[]string{"recognize"}, "audd recognize takes at least 1 argument and got 0", "audd recognize --help"},
		{[]string{"jobs", "show", "a", "b"}, "audd jobs show takes 1 argument and got 2", "audd jobs show --help"},
		{[]string{"recognise", "x"}, `unknown command \"recognise\" for \"audd\"`, "audd recognize"},
	}
	for _, c := range cases {
		r := run(t, append(c.args, "--format", "json")...)
		if r.Code != 2 || !strings.Contains(r.Stderr, c.msg) || !strings.Contains(r.Stderr, `"hint":"`+c.hint+`"`) {
			t.Errorf("%v: %d %s", c.args, r.Code, r.Stderr)
		}
		if strings.Contains(r.Stderr, "(s)") || strings.Contains(r.Stderr, `\n`) {
			t.Errorf("%v: %s", c.args, r.Stderr)
		}
	}
}

func TestProfileNameMustBeFileSafe(t *testing.T) {
	testutil.Isolate(t)
	r := run(t, "--profile", "x/../../escaped", "config", "list", "--format", "json")
	if r.Code != 2 || !strings.Contains(r.Stderr, "profile names use letters") {
		t.Fatalf("--profile: %d %s", r.Code, r.Stderr)
	}
	t.Setenv("AUDD_PROFILE", "../x")
	if r := run(t, "config", "list", "--format", "json"); r.Code != 2 {
		t.Fatalf("AUDD_PROFILE: %d %s", r.Code, r.Stderr)
	}
}

func TestConfigSetBadNumberHasAHint(t *testing.T) {
	testutil.Isolate(t)
	r := run(t, "config", "set", "concurrency", "0", "--format", "json")
	if r.Code != 2 || !strings.Contains(r.Stderr, `"hint":"audd config set concurrency 4"`) {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
}

func TestFlagErrorsReadAsSentences(t *testing.T) {
	testutil.Isolate(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"streams", "add", "https://radio.example/s.mp3"}, `audd streams add needs --id N: the stream's ID (a number you choose)`},
		{[]string{"catalog", "add", "song.mp3"}, `audd catalog add needs --id N`},
		{[]string{"streams", "add", "https://radio.example/s.mp3", "--id", "abc"}, `--id must be a whole number, got \"abc\"`},
		{[]string{"recognize", "a.mp3", "--concurrency", "abc"}, `--concurrency must be a whole number, got \"abc\"`},
	} {
		r := run(t, c.args...)
		if r.Code != output.ExitUsage || !strings.Contains(r.Stderr, c.want) || strings.Contains(r.Stderr, "(s)") || strings.Contains(r.Stderr, "strconv") {
			t.Errorf("%v: %d %s", c.args, r.Code, r.Stderr)
		}
	}
	r := run(t, "agent-setup", "--dir", filepath.Join(t.TempDir(), "nope"))
	if r.Code != output.ExitUsage || !strings.Contains(r.Stderr, `"hint":"create the folder`) {
		t.Errorf("agent-setup --dir: %s", r.Stderr)
	}
	r = run(t, "recognize", "a.mp3", "--max-requests", "-1")
	if r.Code != output.ExitUsage || !strings.Contains(r.Stderr, `"hint":"--max-requests 500"`) {
		t.Errorf("--max-requests -1: %s", r.Stderr)
	}
}

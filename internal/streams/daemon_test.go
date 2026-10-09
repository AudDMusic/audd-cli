package streams

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

func TestLockAllowsOneRecorderPerProfile(t *testing.T) {
	testutil.Isolate(t)
	running, _, _, err := Status("default")
	if err != nil || running {
		t.Fatalf("nothing runs yet: %v %v", running, err)
	}
	l, err := Acquire("default")
	if err != nil {
		t.Fatal(err)
	}
	running, pid, since, err := Status("default")
	if err != nil || !running || pid != os.Getpid() || since.IsZero() {
		t.Fatalf("status while locked: %v %d %v %v", running, pid, since, err)
	}
	if _, err := Acquire("default"); output.AsError(err).Code != "recorder_running" {
		t.Fatalf("second recorder must be refused: %v", err)
	}
	// Other profiles have their own recorder.
	other, err := Acquire("work")
	if err != nil {
		t.Fatal(err)
	}
	other.Release()
	l.Release()
	if running, _, _, _ := Status("default"); running {
		t.Fatal("released")
	}
	if _, err := os.Stat(PIDPath("default")); !os.IsNotExist(err) {
		t.Fatal("PID file removed on release")
	}
	if err := Stop("default"); err != ErrNotRunning {
		t.Fatalf("stop without a recorder: %v", err)
	}
}

type spawnCall struct {
	exe     string
	args    []string
	env     []string
	logPath string
}

func fakeSpawn(t *testing.T) *[]spawnCall {
	t.Helper()
	var calls []spawnCall
	oldSpawn, oldTest := spawnProcess, isTestBinary
	spawnProcess = func(exe string, args, env []string, logPath string) (int, error) {
		calls = append(calls, spawnCall{exe, args, env, logPath})
		return 4242, nil
	}
	isTestBinary = func() bool { return false }
	t.Cleanup(func() { spawnProcess, isTestBinary = oldSpawn, oldTest })
	return &calls
}

func testApp(t *testing.T) *app.App {
	t.Helper()
	a := app.New()
	a.Cfg = config.Defaults(filepath.Join(config.Dir(), "config.toml"))
	a.Profile = a.Cfg.Profile("default")
	a.Secrets = secrets.Default()
	return a
}

func TestEnsureBackground(t *testing.T) {
	testutil.Isolate(t)
	calls := fakeSpawn(t)
	a := testApp(t)

	// No token: nothing to record with.
	if started, err := EnsureBackground(a); started || err != nil {
		t.Fatalf("no token: %v %v", started, err)
	}
	// Turned off in config.
	t.Setenv("AUDD_API_TOKEN", testutil.PlaceholderToken)
	off := false
	a.Profile.BackgroundRecorder = &off
	if started, _ := EnsureBackground(a); started || len(*calls) != 0 {
		t.Fatal("streams.background_recorder false must not start it")
	}
	a.Profile.BackgroundRecorder = nil
	t.Setenv("AUDD_NO_BACKGROUND_RECORDER", "1")
	if started, _ := EnsureBackground(a); started {
		t.Fatal("AUDD_NO_BACKGROUND_RECORDER")
	}
	t.Setenv("AUDD_NO_BACKGROUND_RECORDER", "")

	started, err := EnsureBackground(a)
	if err != nil || !started || len(*calls) != 1 {
		t.Fatalf("start: %v %v %d", started, err, len(*calls))
	}
	c := (*calls)[0]
	if strings.Join(c.args, " ") != "streams record --profile default --background-child" || c.logPath != LogPath("default") {
		t.Fatalf("spawn %+v", c)
	}

	// Already running: no second start.
	l, err := Acquire("default")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if started, _ := EnsureBackground(a); started || len(*calls) != 1 {
		t.Fatal("must not start a second recorder")
	}
}

func TestEnsureBackgroundPassesFlagTokenInEnvironment(t *testing.T) {
	testutil.Isolate(t)
	calls := fakeSpawn(t)
	a := testApp(t)
	a.Flags.Token = testutil.PlaceholderToken
	if started, err := EnsureBackground(a); !started || err != nil {
		t.Fatal(started, err)
	}
	c := (*calls)[0]
	if strings.Contains(strings.Join(c.args, " "), testutil.PlaceholderToken) {
		t.Fatal("the token must never be on the command line")
	}
	found := false
	for _, kv := range c.env {
		if kv == "AUDD_API_TOKEN="+testutil.PlaceholderToken {
			found = true
		}
	}
	if !found {
		t.Fatal("token passed in the environment")
	}
}

func TestEnsureBackgroundSkipsTestBinaries(t *testing.T) {
	testutil.Isolate(t)
	t.Setenv("AUDD_API_TOKEN", testutil.PlaceholderToken)
	called := false
	old := spawnProcess
	spawnProcess = func(string, []string, []string, string) (int, error) { called = true; return 1, nil }
	defer func() { spawnProcess = old }()
	if started, _ := EnsureBackground(testApp(t)); started || called {
		t.Fatal("go test binaries never start background recorders")
	}
}

func TestHookAssigned(t *testing.T) {
	testutil.Isolate(t)
	if started, err := app.EnsureRecorder(testApp(t)); err != nil || started {
		t.Fatalf("hook: %v %v", started, err)
	}
}

func TestServiceFiles(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		e := serviceEnv{goos: goos, exe: "/usr/local/bin/audd", profile: "default", home: "/home/me",
			configHome: "/home/me/.config", dataDir: "/home/me/.local/share/audd"}
		if goos == "windows" {
			e.exe, e.home, e.dataDir = `C:\Program Files\audd\audd.exe`, `C:\Users\me`, `C:\Users\me\AppData\Local\audd`
		}
		svc := buildService(e)
		golden := "Path: " + svc.Path + "\nActivate: " + svc.Activate + "\n\n" + strings.ReplaceAll(svc.Content, "\r\n", "\n")
		testutil.Golden(t, "service_"+goos, []byte(golden))
	}
	e := serviceEnv{goos: "linux", exe: "/opt/my apps/audd", profile: "work", configHome: "/c", dataDir: "/d"}
	if svc := buildService(e); !strings.Contains(svc.Content, `ExecStart="/opt/my apps/audd" streams record --profile work`) {
		t.Fatalf("quoting: %s", svc.Content)
	}
}

func TestInstallServiceWritesFile(t *testing.T) {
	testutil.Isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path, err := InstallService("default")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		t.Fatalf("%s: %v", path, err)
	}
}

func TestRestartBackgroundAfterATokenChange(t *testing.T) {
	testutil.Isolate(t)
	t.Setenv("AUDD_API_TOKEN", testutil.PlaceholderToken)
	calls := fakeSpawn(t)
	a := testApp(t)
	var held *Lock
	oldTerm := terminateProcess
	terminateProcess = func(pid int) error {
		if held != nil {
			held.Release()
			held = nil
		}
		return nil
	}
	t.Cleanup(func() {
		terminateProcess = oldTerm
		if held != nil {
			held.Release()
		}
	})

	// Nothing runs: nothing to restart, and none is started.
	if restarted, err := RestartBackground(a); restarted || err != nil || len(*calls) != 0 {
		t.Fatalf("nothing running: %v %v %d", restarted, err, len(*calls))
	}
	// A foreground recorder (audd streams record) is left alone.
	l, err := Acquire("default")
	if err != nil {
		t.Fatal(err)
	}
	held = l
	if restarted, err := RestartBackground(a); restarted || err != nil || held == nil {
		t.Fatalf("foreground recorder: %v %v", restarted, err)
	}
	// The background recorder is stopped and started again.
	if err := held.MarkBackground(); err != nil {
		t.Fatal(err)
	}
	restarted, err := RestartBackground(a)
	if err != nil || !restarted || held != nil || len(*calls) != 1 {
		t.Fatalf("background recorder: %v %v %d", restarted, err, len(*calls))
	}
}

func TestStopBackgroundLeavesAForegroundRecorder(t *testing.T) {
	testutil.Isolate(t)
	if stopped, err := StopBackground("default"); stopped || err != nil {
		t.Fatalf("nothing running: %v %v", stopped, err)
	}
	l, err := Acquire("default")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Release)
	defer SetTerminateForTesting(func(int) error { l.Release(); return nil })()
	if stopped, err := StopBackground("default"); stopped || err != nil {
		t.Fatalf("foreground recorder: %v %v", stopped, err)
	}
	if err := l.MarkBackground(); err != nil {
		t.Fatal(err)
	}
	if stopped, err := StopBackground("default"); !stopped || err != nil {
		t.Fatalf("background recorder: %v %v", stopped, err)
	}
}

func TestWithStopFileCancelsWhenTheFileAppears(t *testing.T) {
	testutil.Isolate(t)
	old := stopPollInterval
	stopPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { stopPollInterval = old })
	if err := os.MkdirAll(filepath.Dir(StopPath("default")), 0o700); err != nil {
		t.Fatal(err)
	}
	// A stale stop file from an earlier run does not stop a new recorder.
	if err := os.WriteFile(StopPath("default"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, stop := WithStopFile(context.Background(), "default")
	defer stop()
	select {
	case <-ctx.Done():
		t.Fatal("a stale stop file cancelled the new recorder")
	case <-time.After(50 * time.Millisecond):
	}
	if err := os.WriteFile(StopPath("default"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the stop file did not cancel the recorder")
	}
	stop()
	if _, err := os.Stat(StopPath("default")); !os.IsNotExist(err) {
		t.Fatalf("stop file left behind: %v", err)
	}
}

// stopTestRecorder holds the recorder lock like a running recorder and
// releases it when ctx ends.
func stopTestRecorder(t *testing.T, honorStopFile bool) (release func()) {
	t.Helper()
	l, err := Acquire("default")
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release = func() { once.Do(func() { l.Release() }) }
	t.Cleanup(release)
	if honorStopFile {
		ctx, stop := WithStopFile(context.Background(), "default")
		go func() { <-ctx.Done(); stop(); release() }()
	}
	return release
}

func TestStopUsesTheStopFileWhenNoSignalArrives(t *testing.T) {
	testutil.Isolate(t)
	oldPoll, oldTerm, oldKill := stopPollInterval, terminateProcess, killProcess
	stopPollInterval = 10 * time.Millisecond
	terminateProcess = func(int) error { return nil } // like Windows: no signal
	killed := false
	killProcess = func(int) error { killed = true; return nil }
	t.Cleanup(func() { stopPollInterval, terminateProcess, killProcess = oldPoll, oldTerm, oldKill })

	stopTestRecorder(t, true)
	if err := Stop("default"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if killed {
		t.Fatal("a recorder that honored the stop file was killed")
	}
}

func TestStopKillsARecorderThatIgnoresTheRequest(t *testing.T) {
	testutil.Isolate(t)
	oldWait, oldTerm, oldKill := gracefulStopWait, terminateProcess, killProcess
	gracefulStopWait = 200 * time.Millisecond
	terminateProcess = func(int) error { return nil }
	var release func()
	killProcess = func(int) error { release(); return nil }
	t.Cleanup(func() { gracefulStopWait, terminateProcess, killProcess = oldWait, oldTerm, oldKill })

	release = stopTestRecorder(t, false)
	if err := Stop("default"); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// A recorder whose lock or PID file was removed learns it from Check.
func TestLockCheckNoticesRemovedFiles(t *testing.T) {
	for _, c := range []struct {
		what string
		path func(string) string
	}{{"PID file", PIDPath}, {"lock file", lockPath}} {
		testutil.Isolate(t)
		l, err := Acquire("default")
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Check(); err != nil {
			l.Release()
			t.Fatalf("fresh lock: %v", err)
		}
		if err := os.Remove(c.path("default")); err != nil {
			// Windows does not let anyone remove the lock file while the
			// recorder holds it.
			l.Release()
			t.Logf("cannot remove the %s here: %v", c.what, err)
			continue
		}
		err = l.Check()
		l.Release()
		if err == nil || !strings.Contains(err.Error(), c.what) {
			t.Fatalf("after removing the %s: %v", c.what, err)
		}
	}
}

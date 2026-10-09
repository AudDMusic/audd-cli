package streams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/AudDMusic/audd-cli/internal/api"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
)

func init() {
	app.EnsureRecorder = EnsureBackground
	app.RestartRecorder = RestartBackground
}

// BackgroundChildFlag is the hidden flag `audd streams record` gets when it
// runs as the background recorder.
const BackgroundChildFlag = "background-child"

// ErrNotRunning is returned by Stop when no recorder runs for the profile.
var ErrNotRunning = errors.New("no stream recorder is running")

func profileName(p string) string {
	if p == "" {
		return config.DefaultProfile
	}
	return p
}

func lockPath(profile string) string {
	return filepath.Join(config.DataDir(), "recorder-"+profileName(profile)+".lock")
}

// PIDPath is the file holding the running recorder's PID and start time.
func PIDPath(profile string) string {
	return filepath.Join(config.DataDir(), "recorder-"+profileName(profile)+".pid")
}

// StopPath is the file `audd streams recorder stop` writes to ask a running
// recorder to shut down. Windows has no SIGTERM for detached processes, so
// this file is how every platform asks for a clean exit.
func StopPath(profile string) string {
	return filepath.Join(config.DataDir(), "recorder-"+profileName(profile)+".stop")
}

// WithStopFile returns a context that is cancelled when the profile's stop
// file appears. It removes any stale stop file first, and removes the file
// again when the returned cancel function runs.
func WithStopFile(ctx context.Context, profile string) (context.Context, context.CancelFunc) {
	path := StopPath(profile)
	_ = os.Remove(path)
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(stopPollInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := os.Stat(path); err == nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(); _ = os.Remove(path) }
}

// stopPollInterval is how often a recorder checks for its stop file.
var stopPollInterval = 500 * time.Millisecond

// LogPath is the background recorder's log file.
func LogPath(profile string) string {
	return filepath.Join(config.DataDir(), "recorder-"+profileName(profile)+".log")
}

type pidInfo struct {
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	Version string    `json:"version"`
	// Background is set for the background recorder (not one run with
	// audd streams record in the foreground).
	Background bool `json:"background,omitempty"`
}

// Lock is the single-recorder-per-profile lock.
type Lock struct {
	fl      *flock.Flock
	profile string
	file    os.FileInfo // the lock file when it was taken
}

// Acquire takes the recorder lock for a profile and writes the PID file. It
// fails with recorder_running when another recorder holds it.
func Acquire(profile string) (*Lock, error) {
	if err := os.MkdirAll(config.DataDir(), 0o700); err != nil {
		return nil, err
	}
	fl := flock.New(lockPath(profile))
	// Retry briefly: a status check may hold the lock for a moment.
	deadline := time.Now().Add(2 * time.Second)
	for {
		ok, err := fl.TryLock()
		if err != nil {
			return nil, err
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			pid := 0
			if info, err := readPID(profile); err == nil {
				pid = info.PID
			}
			return nil, &output.Error{
				Code:    "recorder_running",
				Message: fmt.Sprintf("a stream recorder is already running for profile %q (pid %d)", profileName(profile), pid),
				Hint:    "audd streams recorder status, or audd streams recorder stop",
				Exit:    output.ExitUsage,
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	b, _ := json.Marshal(pidInfo{PID: os.Getpid(), Started: time.Now().UTC().Truncate(time.Second), Version: app.Version})
	if err := secrets.WriteFileAtomic(PIDPath(profile), b, 0o600); err != nil {
		fl.Unlock()
		return nil, err
	}
	fi, err := os.Stat(lockPath(profile))
	if err != nil {
		fl.Unlock()
		return nil, err
	}
	return &Lock{fl: fl, profile: profile, file: fi}, nil
}

// Check reports why this recorder no longer owns the profile: its lock file
// or PID file was removed or replaced (by hand, or by cleaning the data
// directory). Without them, status cannot see the recorder, stop cannot
// stop it, and a second recorder could start, so a recorder that gets an
// error here should exit. It returns nil while both are in place.
func (l *Lock) Check() error {
	if l == nil || l.fl == nil {
		return nil
	}
	fi, err := os.Stat(lockPath(l.profile))
	if err != nil {
		return fmt.Errorf("its lock file %s was removed", lockPath(l.profile))
	}
	if l.file != nil && !os.SameFile(l.file, fi) {
		return fmt.Errorf("its lock file %s was replaced", lockPath(l.profile))
	}
	info, err := readPID(l.profile)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("its PID file %s was removed", PIDPath(l.profile))
	case err == nil && info.PID != os.Getpid():
		return fmt.Errorf("its PID file %s names another process (pid %d)", PIDPath(l.profile), info.PID)
	}
	return nil
}

// MarkBackground records in the PID file that this is the background
// recorder, which RestartBackground may restart.
func (l *Lock) MarkBackground() error {
	info, err := readPID(l.profile)
	if err != nil {
		return err
	}
	info.Background = true
	b, _ := json.Marshal(info)
	return secrets.WriteFileAtomic(PIDPath(l.profile), b, 0o600)
}

// Release removes the PID file and releases the lock.
func (l *Lock) Release() {
	if l == nil || l.fl == nil {
		return
	}
	if info, err := readPID(l.profile); err == nil && info.PID == os.Getpid() {
		os.Remove(PIDPath(l.profile))
	}
	l.fl.Unlock()
	l.fl = nil
}

func readPID(profile string) (pidInfo, error) {
	var info pidInfo
	b, err := os.ReadFile(PIDPath(profile))
	if err != nil {
		return info, err
	}
	err = json.Unmarshal(b, &info)
	return info, err
}

// Status reports whether a recorder runs for the profile.
func Status(profile string) (running bool, pid int, since time.Time, err error) {
	path := lockPath(profile)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, 0, time.Time{}, nil
	}
	fl := flock.New(path)
	ok, err := fl.TryLock()
	if err != nil {
		return false, 0, time.Time{}, err
	}
	if ok {
		fl.Unlock()
		return false, 0, time.Time{}, nil
	}
	info, _ := readPID(profile)
	return true, info.PID, info.Started, nil
}

// Stop asks the profile's recorder to exit and waits up to 10 seconds.
func Stop(profile string) error {
	running, pid, _, err := Status(profile)
	if err != nil {
		return err
	}
	if !running {
		return ErrNotRunning
	}
	if pid <= 0 {
		return output.Errf(output.ExitUnexpected, "recorder_unknown_pid", "remove "+PIDPath(profile)+" after stopping the process yourself",
			"a recorder is running but its PID file is missing")
	}
	// Ask for a clean exit: the stop file works everywhere, and on Unix a
	// SIGTERM wakes the recorder immediately.
	if err := os.WriteFile(StopPath(profile), []byte(strconv.Itoa(pid)), 0o600); err != nil {
		return fmt.Errorf("asking the recorder to stop: %w", err)
	}
	if err := terminateProcess(pid); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stopping the recorder (pid %d): %w", pid, err)
	}
	if waitStopped(profile, gracefulStopWait) {
		return nil
	}
	// It did not exit on request (it may be hung): end the process.
	if err := killProcess(pid); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stopping the recorder (pid %d): %w", pid, err)
	}
	if waitStopped(profile, 2*time.Second) {
		_ = os.Remove(StopPath(profile))
		return nil
	}
	return output.Errf(output.ExitUnexpected, "recorder_still_running", "",
		"the recorder (pid %d) did not stop within %d seconds", pid, int((gracefulStopWait+2*time.Second)/time.Second))
}

// gracefulStopWait is how long Stop waits for a clean exit before ending
// the process.
var gracefulStopWait = 8 * time.Second

func waitStopped(profile string, d time.Duration) bool {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if running, _, _, _ := Status(profile); !running {
			return true
		}
	}
	return false
}

// killProcess ends a process that did not exit on request. Tests replace it.
var killProcess = kill

// terminateProcess asks a process to exit. Tests replace it.
var terminateProcess = terminate

// spawnProcess starts a detached process with stdout and stderr appended to
// logPath, and returns its PID. Tests replace it.
var spawnProcess = func(exe string, args, env []string, logPath string) (int, error) {
	logf, err := openLog(logPath)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = detachAttrs()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	// Reap the child if it exits while this process is still running (a
	// long-running command such as the explorer or audd mcp), so it does
	// not linger as a zombie.
	go cmd.Wait()
	return pid, nil
}

// isTestBinary keeps `go test` binaries from launching copies of
// themselves as background recorders.
var isTestBinary = func() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.TrimSuffix(filepath.Base(exe), ".exe"), ".test")
}

// openLog opens the recorder log for appending, starting a new file when it
// has grown past 5 MB (the previous one is kept as .1).
func openLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > 5<<20 {
		os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// OpenLog opens the profile's recorder log for appending.
func OpenLog(profile string) (*os.File, error) { return openLog(LogPath(profile)) }

// Executable is the path services and the background recorder run: the
// audd on PATH when it is this binary (so package-manager upgrades keep
// working), else this binary.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := exec.LookPath("audd"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			a, errA := os.Stat(abs)
			b, errB := os.Stat(exe)
			if errA == nil && errB == nil && os.SameFile(a, b) {
				return abs, nil
			}
		}
	}
	return exe, nil
}

// EnsureBackground starts the background recorder for the active profile
// unless it is running, turned off (streams.background_recorder false or
// AUDD_NO_BACKGROUND_RECORDER set), or there is no API token. started
// reports whether this call started it. It is assigned to
// app.EnsureRecorder. AUDD_NO_BACKGROUND_RECORDER is intentionally supported
// but left out of the user docs: tests (internal/e2e) and throwaway
// environments such as CI use it so that only an explicit
// `audd streams recorder start` starts a recorder.
func EnsureBackground(a *app.App) (started bool, err error) {
	if a == nil || a.Profile == nil {
		return false, nil
	}
	if !a.Profile.RecorderEnabled() || os.Getenv("AUDD_NO_BACKGROUND_RECORDER") != "" || isTestBinary() {
		return false, nil
	}
	started, err = startBackground(a)
	if e, ok := err.(*output.Error); ok && e.Code == "no_token" {
		return false, nil
	}
	return started, err
}

// StartBackground starts the background recorder for the active profile
// even when automatic starting is turned off. started is false when one is
// already running.
func StartBackground(a *app.App) (started bool, err error) {
	if isTestBinary() {
		return false, output.Errf(output.ExitUnexpected, "not_available", "", "test builds do not start background recorders")
	}
	return startBackground(a)
}

func startBackground(a *app.App) (bool, error) {
	profile := profileName(a.Profile.Name)
	if running, _, _, err := Status(profile); err != nil || running {
		return false, err
	}
	token, src, err := config.ResolveToken(a.Flags.Token, profile, a.Secrets)
	if err != nil {
		return false, err
	}
	if token == "" {
		return false, output.Errf(output.ExitAuth, "no_token", api.NoTokenHint,
			"the stream recorder needs an API token")
	}
	exe, err := Executable()
	if err != nil {
		return false, err
	}
	env := os.Environ()
	if src == config.SourceFlag {
		// Pass a --token value through the environment, never the command line.
		env = append(withoutEnv(env, "AUDD_API_TOKEN"), "AUDD_API_TOKEN="+token)
	}
	args := []string{"streams", "record", "--profile", profile, "--" + BackgroundChildFlag}
	if _, err := spawnProcess(exe, args, env, LogPath(profile)); err != nil {
		return false, err
	}
	return true, nil
}

// RestartBackground restarts the profile's background recorder, if one
// runs, so it reads the API token again (after audd token rotate, say). A
// recorder run in the foreground with audd streams record is left alone;
// it picks up a stored token change within five minutes.
func RestartBackground(a *app.App) (restarted bool, err error) {
	if a == nil || a.Profile == nil || isTestBinary() {
		return false, nil
	}
	profile := profileName(a.Profile.Name)
	running, _, _, err := Status(profile)
	if err != nil || !running {
		return false, err
	}
	if info, err := readPID(profile); err != nil || !info.Background {
		return false, nil
	}
	if err := Stop(profile); err != nil && err != ErrNotRunning {
		return false, err
	}
	return startBackground(a)
}

// StopBackground stops the profile's background recorder, if one runs (after
// audd logout, say, when it used the token from the sign-in). A recorder run
// in the foreground with audd streams record is left alone. stopped reports
// whether one was stopped.
func StopBackground(profile string) (stopped bool, err error) {
	profile = profileName(profile)
	running, _, _, err := Status(profile)
	if err != nil || !running {
		return false, err
	}
	if info, err := readPID(profile); err != nil || !info.Background {
		return false, nil
	}
	if err := Stop(profile); err != nil {
		if err == ErrNotRunning {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// SetTerminateForTesting replaces how a recorder process is asked to exit
// and returns a function that restores the default.
func SetTerminateForTesting(f func(pid int) error) (restore func()) {
	old := terminateProcess
	terminateProcess = f
	return func() { terminateProcess = old }
}

// SetSpawnForTesting replaces how the background recorder process is
// started and returns a function that restores the default.
func SetSpawnForTesting(f func(exe string, args, env []string, logPath string) (pid int, err error)) (restore func()) {
	oldSpawn, oldTest := spawnProcess, isTestBinary
	spawnProcess, isTestBinary = f, func() bool { return false }
	return func() { spawnProcess, isTestBinary = oldSpawn, oldTest }
}

func withoutEnv(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

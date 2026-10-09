package streams

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"html"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"

	"github.com/AudDMusic/audd-cli/internal/config"
)

// Service describes the login service file for a profile's recorder.
type Service struct {
	Path     string // where the file goes
	Content  string // file contents (UTF-8; Windows task XML is written as UTF-16)
	Activate string // the command that turns it on
	// StartsNow reports whether Activate also starts the recorder right
	// away (systemd and launchd); a Windows scheduled task first runs at
	// the next logon.
	StartsNow bool
}

type serviceEnv struct {
	goos       string
	exe        string
	profile    string
	home       string
	configHome string // XDG_CONFIG_HOME or ~/.config
	dataDir    string
}

func currentServiceEnv(profile string) (serviceEnv, error) {
	exe, err := Executable()
	if err != nil {
		return serviceEnv{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return serviceEnv{}, err
	}
	ch := os.Getenv("XDG_CONFIG_HOME")
	if ch == "" {
		ch = filepath.Join(home, ".config")
	}
	return serviceEnv{goos: runtime.GOOS, exe: exe, profile: profileName(profile), home: home, configHome: ch, dataDir: config.DataDir()}, nil
}

// ServiceFor returns the service definition for this OS: a systemd user
// unit on Linux and other Unix systems, a launchd agent on macOS, and a
// Task Scheduler task on Windows. Each runs `audd streams record` at login.
func ServiceFor(profile string) (Service, error) {
	env, err := currentServiceEnv(profile)
	if err != nil {
		return Service{}, err
	}
	return buildService(env), nil
}

// InstallService writes the service file for this OS and returns its path.
// It does not activate it; ServiceFor(profile).Activate is the command that does.
func InstallService(profile string) (path string, err error) {
	svc, err := ServiceFor(profile)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(svc.Path), 0o755); err != nil {
		return "", err
	}
	data := []byte(svc.Content)
	if strings.HasSuffix(svc.Path, ".xml") {
		data = utf16LE(svc.Content)
	}
	if err := os.WriteFile(svc.Path, data, 0o644); err != nil {
		return "", err
	}
	return svc.Path, nil
}

func buildService(e serviceEnv) Service {
	args := []string{"streams", "record", "--profile", e.profile, "--" + BackgroundChildFlag}
	switch e.goos {
	case "darwin":
		label := "io.audd.recorder." + e.profile
		path := joinFor(e.goos, e.home, "Library", "LaunchAgents", label+".plist")
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + html.EscapeString(label) + `</string>
	<key>ProgramArguments</key>
	<array>
`)
		for _, a := range append([]string{e.exe}, args...) {
			b.WriteString("\t\t<string>" + html.EscapeString(a) + "</string>\n")
		}
		logPath := joinFor(e.goos, e.dataDir, "recorder-"+e.profile+".log")
		b.WriteString(`	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>Umask</key>
	<integer>63</integer>
	<key>StandardOutPath</key>
	<string>` + html.EscapeString(logPath) + `</string>
	<key>StandardErrorPath</key>
	<string>` + html.EscapeString(logPath) + `</string>
</dict>
</plist>
`)
		return Service{Path: path, Content: b.String(), Activate: "launchctl load -w " + shellQuote(path), StartsNow: true}
	case "windows":
		name := "AudD stream recorder (" + e.profile + ")"
		path := joinFor(e.goos, e.dataDir, "recorder-"+e.profile+"-task.xml")
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = windowsQuote(a)
		}
		content := `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Records AudD stream results for audd profile ` + html.EscapeString(e.profile) + `.</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
    <Hidden>true</Hidden>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + html.EscapeString(e.exe) + `</Command>
      <Arguments>` + html.EscapeString(strings.Join(quoted, " ")) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
		content = strings.ReplaceAll(content, "\n", "\r\n")
		return Service{Path: path, Content: content,
			Activate: fmt.Sprintf(`schtasks /Create /TN "%s" /XML "%s" /F`, name, path)}
	default:
		unit := "audd-recorder-" + e.profile + ".service"
		path := joinFor(e.goos, e.configHome, "systemd", "user", unit)
		cmd := make([]string, 0, len(args)+1)
		for _, a := range append([]string{e.exe}, args...) {
			cmd = append(cmd, systemdQuote(a))
		}
		content := `[Unit]
Description=AudD stream recorder (profile ` + e.profile + `)
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=` + strings.Join(cmd, " ") + `
Restart=on-failure
RestartSec=30

[Install]
WantedBy=default.target
`
		return Service{Path: path, Content: content,
			Activate: "systemctl --user daemon-reload && systemctl --user enable --now " + unit, StartsNow: true}
	}
}

// joinFor joins path elements with the target OS's separator, so service
// files render the same whichever OS builds them.
func joinFor(goos string, elems ...string) string {
	if goos == "windows" {
		for i := range elems {
			elems[i] = strings.TrimRight(elems[i], `\/`)
		}
		return strings.Join(elems, `\`)
	}
	return path.Join(elems...)
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func systemdQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\%$") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
	return `"` + r.Replace(s) + `"`
}

func windowsQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func utf16LE(s string) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xFE})
	for _, u := range utf16.Encode([]rune(s)) {
		binary.Write(&buf, binary.LittleEndian, u)
	}
	return buf.Bytes()
}

package tui

import (
	"encoding/base64"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

// Test seams for side effects.
var (
	// openURL opens a link in the default browser.
	openURL = func(u string) error {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", u)
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
		default:
			cmd = exec.Command("xdg-open", u)
		}
		return cmd.Start()
	}
	// notify shows a desktop notification.
	notify = func(title, body string) error {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("osascript", "-e",
				fmt.Sprintf("display notification %s with title %s", appleScriptString(body), appleScriptString(title)))
		case "windows":
			cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", windowsToast(title, body))
		default:
			cmd = exec.Command("notify-send", "--app-name=audd", title, body)
		}
		return cmd.Run()
	}
)

func appleScriptString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func windowsToast(title, body string) string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	return `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null;` +
		`$t = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02);` +
		`$x = $t.GetElementsByTagName('text'); $x.Item(0).AppendChild($t.CreateTextNode(` + q(title) + `)) > $null;` +
		`$x.Item(1).AppendChild($t.CreateTextNode(` + q(body) + `)) > $null;` +
		`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('audd').Show([Windows.UI.Notifications.ToastNotification]::new($t))`
}

// copyOSC52 asks the terminal to put s on the clipboard (OSC 52). Most
// modern terminals support it, including over SSH.
func copyOSC52(w io.Writer, s string) {
	fmt.Fprintf(w, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(s)))
}

package cli

import (
	"fmt"
	"io"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
)

// VersionInfo is the output of `audd version`.
type VersionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

func currentVersion() VersionInfo {
	return VersionInfo{
		Version: app.Version, Commit: app.Commit, Date: app.Date,
		Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
	}
}

func versionLine() string {
	v := currentVersion()
	return fmt.Sprintf("audd %s (commit %s, built %s, %s, %s/%s)", v.Version, v.Commit, v.Date, v.Go, v.OS, v.Arch)
}

func newVersionCmd(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:         "version",
		Short:       "Show the audd version",
		GroupID:     GroupOther,
		Annotations: map[string]string{annotationNoConfig: "true"},
		Args:        cobra.NoArgs,
		Example:     "  audd version\n  audd version --format json",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.Out.Result(currentVersion(), func(w io.Writer) {
				fmt.Fprintln(w, versionLine())
			})
		},
	}
}

package cli

import (
	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/jobs"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newListenCmd(a))
	})
}

// listenMaxSeconds caps --seconds well below the size the standard
// endpoint accepts.
const listenMaxSeconds = 60

// Seams for tests.
var (
	listenFindTools = media.Find
	listenCapture   = media.Capture
)

type listenFlags struct {
	seconds        int
	device         string
	ret            string
	details, noArt bool
	failOnNoMatch  bool
}

func newListenCmd(a *app.App) *cobra.Command {
	var f listenFlags
	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Identify the music playing near you (microphone)",
		Long: `Record a few seconds from the microphone and identify the song.

Recording uses ffmpeg, or sox when ffmpeg is not installed. The standard
endpoint analyzes up to the first 12 seconds of audio, so 10 to 12 seconds
is enough. The recording is deleted afterwards.

--device picks an input: on macOS an avfoundation index or name (":1"),
on Linux a PulseAudio source or an ALSA device (alsa:hw:1,0), on Windows a
DirectShow device name ("Microphone (USB Audio)").`,
		GroupID: GroupRecognize,
		Args:    cobra.NoArgs,
		Example: `  audd listen
  audd listen --seconds 12 --return apple_music,spotify
  audd listen --device "Microphone (USB Audio)"
  audd listen --format json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListen(cmd, a, f)
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&f.seconds, "seconds", 10, "how many seconds to record (at most 60)")
	fl.StringVar(&f.device, "device", "", "input device (default: the system default input)")
	fl.StringVar(&f.ret, "return", "", "extra metadata: apple_music, spotify, deezer, musicbrainz (comma-separated)")
	fl.BoolVarP(&f.details, "details", "v", false, "show every field")
	fl.BoolVar(&f.noArt, "no-art", false, "do not show cover art")
	fl.BoolVar(&f.failOnNoMatch, "fail-on-no-match", false, "exit 1 when the song is not recognized")
	return cmd
}

func runListen(cmd *cobra.Command, a *app.App, f listenFlags) error {
	if f.seconds <= 0 {
		return output.Errf(output.ExitUsage, "invalid_argument", "", "--seconds must be more than 0")
	}
	if f.seconds > listenMaxSeconds {
		// The recording is uncompressed (about 88 KB a second); longer ones
		// would pass the standard endpoint's 10 MB limit.
		return output.Errf(output.ExitUsage, "invalid_argument", "audd listen --seconds 12",
			"--seconds can be at most %d; only the first 12 seconds are analyzed", listenMaxSeconds)
	}
	rc := listenRecognizeCmd(cmd.Root())
	if rc == nil {
		return app.NotImplemented("recognition")
	}
	ctx := cmd.Context()
	tools := listenFindTools()
	if tools.FFmpeg == "" && tools.Sox == "" {
		e := media.MissingTool("ffmpeg", "listening to the microphone")
		e.Message += " (sox works too)"
		return e
	}
	if err := requireToken(a); err != nil {
		return err
	}
	a.Out.Info("Listening for %d seconds…", f.seconds)
	path, cleanup, err := listenCapture(ctx, tools, f.seconds, f.device)
	if err != nil {
		if ctx.Err() != nil {
			return &output.Error{Code: "interrupted", Message: "stopped", Exit: jobs.ExitInterrupted}
		}
		return err
	}
	defer cleanup()

	return listenDelegate(cmd, rc, path, f)
}

// listenRecognizeCmd finds `audd recognize`, so a recording goes through the
// same path (cache, output, card) as a file.
func listenRecognizeCmd(root *cobra.Command) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == "recognize" && c.RunE != nil {
			return c
		}
	}
	return nil
}

// listenDelegate runs `audd recognize <recording>` with the listen flags
// that recognize also has. The result names the input "microphone".
func listenDelegate(cmd, rc *cobra.Command, path string, f listenFlags) error {
	set := map[string]string{"input-name": "microphone"}
	if f.ret != "" {
		set["return"] = f.ret
	}
	if f.details {
		set["details"] = "true"
	}
	if f.noArt {
		set["no-art"] = "true"
	}
	if f.failOnNoMatch {
		set["fail-on-no-match"] = "true"
	}
	for name, v := range set {
		if rc.Flags().Lookup(name) == nil {
			continue
		}
		if err := rc.Flags().Set(name, v); err != nil {
			return output.Errf(output.ExitUsage, "invalid_argument", "audd listen --help", "--%s: %v", name, err)
		}
	}
	rc.SetContext(cmd.Context())
	return rc.RunE(rc, []string{path})
}

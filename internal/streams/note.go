package streams

import (
	"time"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/streamstore"
)

// RecorderNote is shown the first time audd starts the background recorder
// for a profile, by any command or screen.
const RecorderNote = "Recording stream results in the background. See audd streams recorder status."

// recorderNoteKey is the stream store meta key that records that
// RecorderNote was shown for the profile.
const recorderNoteKey = "recorder_note_shown"

// EnsureRecorder starts the background recorder when it should run (through
// app.EnsureRecorder) and returns the note to show: RecorderNote when this
// call started the recorder and the profile has not seen the note before,
// else "". err is the start error, which callers treat as a note at most.
func EnsureRecorder(a *app.App) (note string, err error) {
	started, err := app.EnsureRecorder(a)
	if err != nil || !started {
		return "", err
	}
	if a.Profile == nil {
		return RecorderNote, nil
	}
	st, oerr := streamstore.Open(a.Profile.Name)
	if oerr != nil {
		return RecorderNote, nil
	}
	defer st.Close()
	if v, _ := st.Meta(recorderNoteKey); v != "" {
		return "", nil
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	_ = st.SetMeta(recorderNoteKey, now.UTC().Format(time.RFC3339))
	return RecorderNote, nil
}

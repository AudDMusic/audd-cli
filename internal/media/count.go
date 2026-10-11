package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Count is what CountInput found.
type Count struct {
	Files int  // media files found (at most the cap)
	More  bool // the count stopped at the cap or the time limit
	Batch bool // a folder or a glob, as audd recognize sees it
	// Root: the input is a whole disk ("/" or a drive), not walked.
	Root bool
	Err  error // the path does not exist, or a pattern is bad
}

var errStop = errors.New("stop")

func isStop(err error) bool {
	return errors.Is(err, errStop) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// IsDiskRoot reports whether p is "/" or a bare drive root ("C:\").
func IsDiskRoot(p string) bool {
	c := filepath.Clean(p)
	if c == "/" || c == `\` {
		return true
	}
	v := filepath.VolumeName(c)
	return v != "" && (c == v || c == v+`\` || c == v+"/")
}

// CountInput counts the media files of one recognize argument, the way
// ExpandInputs would find them, but stops after limit files or when ctx
// ends (More is then set). A whole disk is not walked (Root is set).
func CountInput(ctx context.Context, arg string, limit int, budget time.Duration) Count {
	if arg == "" || arg == "-" || strings.Contains(arg, "://") {
		return Count{}
	}
	if budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	var c Count
	visit := func(Input) error {
		if err := ctx.Err(); err != nil {
			c.More = true
			return err
		}
		c.Files++
		if limit > 0 && c.Files >= limit {
			c.More = true
			return errStop
		}
		return nil
	}
	fi, statErr := os.Stat(arg)
	switch {
	case statErr == nil && !fi.IsDir():
		return Count{Files: 1}
	case statErr == nil:
		c.Batch = true
		if IsDiskRoot(arg) {
			c.Root = true
			return c
		}
		if err := walkMedia(arg, visit); err != nil && !isStop(err) {
			c.Err = usageErr("", "cannot read folder %s: %v", arg, err)
		}
	case hasMeta(arg):
		c.Batch = true
		if err := globMedia(arg, visit); err != nil && !isStop(err) {
			c.Err = err
		}
	default:
		c.Err = usageErr("check the path", "no such file or folder: %s", arg)
	}
	if c.Err == nil && c.Files == 0 && !c.More {
		c.Err = usageErr("", "no audio or video files found in %s", arg)
	}
	return c
}

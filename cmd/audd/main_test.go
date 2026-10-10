package main

import (
	"reflect"
	"runtime"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/jobs"
	"github.com/AudDMusic/audd-cli/internal/tui"
)

func funcName(f any) string {
	return runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
}

// The binary only imports internal/cli; the packages that own each hook must
// still be linked in and their init() must have run.
func TestHooksAssignedByOwners(t *testing.T) {
	const mod = "github.com/AudDMusic/audd-cli/internal/"
	for name, tc := range map[string]struct {
		fn   any
		want string
	}{
		"RunBatch":       {app.RunBatch, mod + "jobs.RunBatch"},
		"RunExplorer":    {app.RunExplorer, mod + "tui.RunExplorer"},
		"RenderResult":   {app.RenderResult, mod + "tui.RenderResultCard"},
		"RunHome":        {app.RunHome, mod + "tui.RunHome"},
		"EnsureRecorder": {app.EnsureRecorder, mod + "streams.EnsureBackground"},
		// Hooks that cli fills in from the API layer, the results cache,
		// the stream store and the jobs store.
		"jobs.Recognize":      {jobs.Recognize, mod + "cli.batchRecognize"},
		"jobs.CacheGet":       {jobs.CacheGet, mod + "cli.batchCacheGet"},
		"jobs.CachePut":       {jobs.CachePut, mod + "cli.batchCachePut"},
		"tui.NewFeed":         {tui.NewFeed, mod + "cli.newStoreFeed"},
		"tui.NewExplorerData": {tui.NewExplorerData, mod + "cli.newExplorerData"},
	} {
		if got := funcName(tc.fn); got != tc.want {
			t.Errorf("%s is %s, want %s", name, got, tc.want)
		}
	}
}

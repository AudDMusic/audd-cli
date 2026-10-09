package cli

import (
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/app"
)

// Building the command tree replaces the App's not-implemented factories.
func TestFactoriesAssigned(t *testing.T) {
	a := app.New()
	a.In = strings.NewReader("")
	newRoot(a, IO{In: a.In, Out: io.Discard, Err: io.Discard}, &runState{})
	for name, f := range map[string]any{"APIClient": a.APIClient, "Account": a.Account} {
		got := runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
		if strings.HasPrefix(got, "github.com/AudDMusic/audd-cli/internal/app.") {
			t.Errorf("App.%s is still the default (%s)", name, got)
		}
	}
	if got := runtime.FuncForPC(reflect.ValueOf(a.APIClient).Pointer()).Name(); !strings.Contains(got, "NewClientFactory") {
		t.Errorf("App.APIClient comes from %s, want api.NewClientFactory", got)
	}
}

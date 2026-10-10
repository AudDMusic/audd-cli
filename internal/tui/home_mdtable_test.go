package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/AudDMusic/audd-cli/internal/output"
)

func TestSplitTableRowKeepsEscapedPipes(t *testing.T) {
	got := splitTableRow("| macOS | `curl -fsSL https://x/install.sh \\| sh` |")
	want := []string{"macOS", "`curl -fsSL https://x/install.sh | sh`"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitTableRow = %q, want %q", got, want)
	}
}

func TestRenderTableAlignsAndFits(t *testing.T) {
	rows := [][]string{
		{"Where", "Command"},
		{"Go", "`go install github.com/AudDMusic/audd-cli/cmd/audd@latest`"},
		{"macOS (Homebrew)", "`brew install auddmusic/tap/audd`"},
	}
	lines := renderTable(rows, 40, output.Styles{})
	col := -1
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > 40 {
			t.Errorf("line is %d wide, want at most 40: %q", w, l)
		}
		if i := strings.Index(l, "brew install"); i >= 0 {
			col = i
		}
	}
	if i := strings.Index(lines[0], "Command"); i != col {
		t.Errorf("Command header at %d, brew cell at %d; want the same column\n%s", i, col, strings.Join(lines, "\n"))
	}
}

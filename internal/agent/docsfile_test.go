package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docs/cli.md is the copy of the embedded CLI reference that people read on
// GitHub. The two must not drift apart.
func TestDocsCLIMatchesEmbedded(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "cli.md"))
	if err != nil {
		t.Fatalf("read docs/cli.md: %v", err)
	}
	norm := func(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
	if norm(string(b)) != norm(CLIDoc) {
		t.Fatal("docs/cli.md differs from internal/agent/cli.md; copy internal/agent/cli.md over docs/cli.md")
	}
}

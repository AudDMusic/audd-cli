// Command audd is the AudD command-line tool.
package main

import (
	"os"

	"github.com/AudDMusic/audd-cli/internal/cli"
	_ "github.com/AudDMusic/audd-cli/internal/termbg" // before Bubble Tea's terminal query
)

func main() {
	os.Exit(cli.Execute())
}

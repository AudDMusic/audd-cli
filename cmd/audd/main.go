// Command audd is the AudD command-line tool.
package main

import (
	"os"

	"github.com/AudDMusic/audd-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}

package art

import (
	"fmt"
	"image"
	"strings"

	kittygfx "github.com/charmbracelet/x/ansi/kitty"
)

// CellSize is the size of one terminal character cell in pixels.
type CellSize struct{ W, H int }

// DefaultCell is the cell size assumed when the terminal does not report
// one: about twice as tall as wide, like most terminal fonts.
var DefaultCell = CellSize{W: 10, H: 20}

// Known reports whether both sides are set.
func (c CellSize) Known() bool { return c.W > 0 && c.H > 0 }

// OrDefault returns c, or DefaultCell when c is not known or implausible.
func (c CellSize) OrDefault() CellSize {
	if !c.Known() || c.W > 200 || c.H > 400 {
		return DefaultCell
	}
	return c
}

// ColsFor returns how many columns make a box of rows rows square on
// screen with this cell size. Half-blocks use it too: each half-block
// pixel is one cell wide and half a cell tall.
func (c CellSize) ColsFor(rows int) int {
	c = c.OrDefault()
	if rows <= 0 {
		return 0
	}
	return max(1, (rows*c.H+c.W/2)/c.W)
}

// KittyTransmit sends img to the terminal under image id and creates a
// virtual placement of cols×rows cells (U=1), which KittyPlaceholders then
// shows wherever its cells are printed. q=2 silences the terminal's
// replies. id must be between 1 and 2^24-1.
func KittyTransmit(img image.Image, id uint32, cols, rows int, cell CellSize) string {
	if img == nil || cols <= 0 || rows <= 0 {
		return ""
	}
	return kittyAPC(fmt.Sprintf("a=T,U=1,i=%d,f=100,q=2,c=%d,r=%d", id, cols, rows), img, cols, rows, cell)
}

// KittyPlaceholders returns rows lines of cols Unicode placeholder cells
// (U+10EEEE) for image id. The foreground color carries the image id and
// two combining marks on each cell carry its row and column, so the cells
// show the image wherever they are drawn and can be moved, redrawn, or
// replaced like any other text.
func KittyPlaceholders(id uint32, cols, rows int) string {
	if cols <= 0 || rows <= 0 {
		return ""
	}
	color := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", id>>16&0xff, id>>8&0xff, id&0xff)
	var sb strings.Builder
	for r := 0; r < rows; r++ {
		sb.WriteString(color)
		for c := 0; c < cols; c++ {
			sb.WriteRune(kittygfx.Placeholder)
			sb.WriteRune(kittygfx.Diacritic(r))
			sb.WriteRune(kittygfx.Diacritic(c))
		}
		sb.WriteString("\x1b[39m")
		if r < rows-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// KittyDelete frees image id and its placements in the terminal.
func KittyDelete(id uint32) string {
	return fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id)
}

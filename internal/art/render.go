package art

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"

	"golang.org/x/image/draw"
)

// Render draws img in a box of cols×rows terminal cells. The half-block
// protocols return rows lines of colored "▀" cells separated by newlines;
// Kitty, iTerm2, and sixel return a single escape sequence that the
// terminal draws over cols×rows cells starting at the cursor. ProtoNone and
// an empty box return "".
//
// Cover art is square and a cell is about twice as tall as it is wide, so
// cols = 2×rows keeps the picture square.
func Render(img image.Image, cols, rows int, p Protocol) string {
	return RenderCell(img, cols, rows, p, DefaultCell)
}

// RenderCell is Render for a terminal whose cells are cell pixels in size,
// which sets the pixel size of images sent with Kitty, iTerm2, and sixel.
func RenderCell(img image.Image, cols, rows int, p Protocol, cell CellSize) string {
	if img == nil || cols <= 0 || rows <= 0 {
		return ""
	}
	switch p {
	case ProtoHalfBlock:
		return halfBlock(img, cols, rows, true)
	case ProtoHalfBlock256:
		return halfBlock(img, cols, rows, false)
	case ProtoKitty:
		return kitty(img, cols, rows, cell)
	case ProtoITerm2:
		return iterm2(img, cols, rows, cell)
	case ProtoSixel:
		return sixel(img, cols, rows, cell)
	}
	return ""
}

// scale resizes img to w×h. An image already that size is returned as is.
func scale(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	if b.Dx() == w && b.Dy() == h {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

func rgbAt(img image.Image, x, y int) (r, g, b uint8) {
	b0 := img.Bounds()
	c := color.NRGBAModel.Convert(img.At(b0.Min.X+x, b0.Min.Y+y)).(color.NRGBA)
	// Composite transparent pixels over black.
	a := uint32(c.A)
	return uint8(uint32(c.R) * a / 255), uint8(uint32(c.G) * a / 255), uint8(uint32(c.B) * a / 255)
}

// halfBlock draws two pixel rows per text row: the upper pixel is the
// foreground of "▀", the lower one the background.
func halfBlock(img image.Image, cols, rows int, truecolor bool) string {
	src := scale(img, cols, rows*2)
	var sb strings.Builder
	for row := 0; row < rows; row++ {
		lastFG, lastBG := "", ""
		for x := 0; x < cols; x++ {
			tr, tg, tb := rgbAt(src, x, row*2)
			br, bg, bb := rgbAt(src, x, row*2+1)
			fg, bgc := colorCode(38, tr, tg, tb, truecolor), colorCode(48, br, bg, bb, truecolor)
			if fg != lastFG {
				sb.WriteString(fg)
				lastFG = fg
			}
			if bgc != lastBG {
				sb.WriteString(bgc)
				lastBG = bgc
			}
			sb.WriteString("▀")
		}
		sb.WriteString("\x1b[0m")
		if row < rows-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func colorCode(layer int, r, g, b uint8, truecolor bool) string {
	if truecolor {
		return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", layer, r, g, b)
	}
	return fmt.Sprintf("\x1b[%d;5;%dm", layer, To256(r, g, b))
}

// To256 maps a color to the nearest entry of the xterm 256-color palette
// (the 6×6×6 cube or the gray ramp).
func To256(r, g, b uint8) int {
	levels := [6]int{0, 95, 135, 175, 215, 255}
	near := func(v uint8) int {
		best, bd := 0, 1<<30
		for i, l := range levels {
			if d := abs(int(v) - l); d < bd {
				best, bd = i, d
			}
		}
		return best
	}
	ri, gi, bi := near(r), near(g), near(b)
	cube := 16 + 36*ri + 6*gi + bi
	cd := sq(int(r)-levels[ri]) + sq(int(g)-levels[gi]) + sq(int(b)-levels[bi])

	avg := (int(r) + int(g) + int(b)) / 3
	gi2 := (avg - 8 + 5) / 10
	if gi2 < 0 {
		gi2 = 0
	}
	if gi2 > 23 {
		gi2 = 23
	}
	gv := 8 + gi2*10
	gd := sq(int(r)-gv) + sq(int(g)-gv) + sq(int(b)-gv)
	if gd < cd {
		return 232 + gi2
	}
	return cube
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sq(v int) int { return v * v }

// maxPixels caps the larger side of an image sent with a pixel protocol,
// so escape sequences stay small.
const maxPixels = 480

// pixelSize is the source size for pixel protocols: the box in pixels,
// scaled down evenly so neither side exceeds maxPixels.
func pixelSize(cols, rows int, cell CellSize) (w, h int) {
	cell = cell.OrDefault()
	w, h = cols*cell.W, rows*cell.H
	if w > maxPixels {
		h = h * maxPixels / w
		w = maxPixels
	}
	if h > maxPixels {
		w = w * maxPixels / h
		h = maxPixels
	}
	return max(w, 1), max(h, 1)
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// kitty sends a PNG with the Kitty graphics protocol, split into 4096-byte
// base64 chunks, scaled by the terminal to cols×rows cells. q=2 silences
// the terminal's replies.
func kitty(img image.Image, cols, rows int, cell CellSize) string {
	return kittyAPC(fmt.Sprintf("a=T,f=100,q=2,c=%d,r=%d", cols, rows), img, cols, rows, cell)
}

// kittyAPC sends img as a PNG in Kitty graphics commands with the given
// control keys on the first chunk.
func kittyAPC(keys string, img image.Image, cols, rows int, cell CellSize) string {
	w, h := pixelSize(cols, rows, cell)
	data := base64.StdEncoding.EncodeToString(encodePNG(scale(img, w, h)))
	var sb strings.Builder
	const chunk = 4096
	for i := 0; i < len(data); i += chunk {
		end := min(i+chunk, len(data))
		more := 0
		if end < len(data) {
			more = 1
		}
		if i == 0 {
			fmt.Fprintf(&sb, "\x1b_G%s,m=%d;%s\x1b\\", keys, more, data[i:end])
		} else {
			fmt.Fprintf(&sb, "\x1b_Gm=%d;%s\x1b\\", more, data[i:end])
		}
	}
	return sb.String()
}

// iterm2 sends a PNG as an iTerm2 inline image sized in cells.
func iterm2(img image.Image, cols, rows int, cell CellSize) string {
	w, h := pixelSize(cols, rows, cell)
	raw := encodePNG(scale(img, w, h))
	return fmt.Sprintf("\x1b]1337;File=inline=1;size=%d;width=%d;height=%d;preserveAspectRatio=1:%s\a",
		len(raw), cols, rows, base64.StdEncoding.EncodeToString(raw))
}

// sixel encodes img with a fixed 6×6×6 color cube palette. The height is
// rounded down to whole sixel bands so the image never spills below its
// rows.
func sixel(img image.Image, cols, rows int, cell CellSize) string {
	w, h := pixelSize(cols, rows, cell)
	h -= h % 6
	if h < 6 {
		h = 6
	}
	src := scale(img, w, h)
	idx := make([]uint8, w*h)
	used := make([]bool, 216)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := rgbAt(src, x, y)
			i := uint8((int(r)*5+127)/255*36 + (int(g)*5+127)/255*6 + (int(b)*5+127)/255)
			idx[y*w+x] = i
			used[i] = true
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\x1bPq\"1;1;%d;%d", w, h)
	for i := 0; i < 216; i++ {
		if used[i] {
			r, g, b := i/36, i/6%6, i%6
			fmt.Fprintf(&sb, "#%d;2;%d;%d;%d", i, r*20, g*20, b*20)
		}
	}
	for band := 0; band < h; band += 6 {
		first := true
		for c := 0; c < 216; c++ {
			if !used[c] {
				continue
			}
			line := make([]byte, w)
			any := false
			for x := 0; x < w; x++ {
				var bits byte
				for k := 0; k < 6; k++ {
					if idx[(band+k)*w+x] == uint8(c) {
						bits |= 1 << k
					}
				}
				if bits != 0 {
					any = true
				}
				line[x] = '?' + bits
			}
			if !any {
				continue
			}
			if !first {
				sb.WriteByte('$')
			}
			first = false
			fmt.Fprintf(&sb, "#%d", c)
			writeRLE(&sb, line)
		}
		sb.WriteByte('-')
	}
	sb.WriteString("\x1b\\")
	return sb.String()
}

func writeRLE(sb *strings.Builder, line []byte) {
	for i := 0; i < len(line); {
		j := i
		for j < len(line) && line[j] == line[i] {
			j++
		}
		if n := j - i; n > 3 {
			fmt.Fprintf(sb, "!%d%c", n, line[i])
		} else {
			sb.Write(line[i:j])
		}
		i = j
	}
}

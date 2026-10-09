package art

import (
	"fmt"
	"image"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Accent picks a color that matches the cover: Apple Music's artwork
// bgColor when known (a hex string such as "1a2b3c"), else the dominant
// saturated color of img. It returns "" (no color) when there is neither.
// Very dark or very light colors are pulled toward the middle so the accent
// stays readable on both dark and light terminals.
func Accent(img image.Image, appleBG string) lipgloss.Color {
	if r, g, b, ok := parseHex(appleBG); ok {
		return hexColor(readable(r, g, b))
	}
	if img == nil {
		return ""
	}
	r, g, b := dominant(img)
	return hexColor(readable(r, g, b))
}

func parseHex(s string) (r, g, b uint8, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return 0, 0, 0, false
	}
	var v uint32
	if _, err := fmt.Sscanf(strings.ToLower(s), "%06x", &v); err != nil {
		return 0, 0, 0, false
	}
	return uint8(v >> 16), uint8(v >> 8), uint8(v), true
}

func hexColor(r, g, b uint8) lipgloss.Color {
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", r, g, b))
}

func luminance(r, g, b uint8) float64 {
	return (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)) / 255
}

// readable blends colors with a luminance outside 0.25–0.8 toward the middle.
func readable(r, g, b uint8) (uint8, uint8, uint8) {
	l := luminance(r, g, b)
	blend := func(c uint8, target float64, t float64) uint8 {
		return uint8(float64(c)*(1-t) + target*t + 0.5)
	}
	switch {
	case l < 0.25:
		t := (0.25 - l) / 0.25 * 0.6
		return blend(r, 255, t), blend(g, 255, t), blend(b, 255, t)
	case l > 0.8:
		t := (l - 0.8) / 0.2 * 0.5
		return blend(r, 0, t), blend(g, 0, t), blend(b, 0, t)
	}
	return r, g, b
}

// dominant returns the mean color of the most common color bucket, weighting
// saturated pixels more so a colorful cover does not come out gray.
func dominant(img image.Image) (uint8, uint8, uint8) {
	src := scale(img, 48, 48)
	type acc struct {
		w          float64
		r, g, b, n float64
	}
	buckets := map[int]*acc{}
	for y := 0; y < 48; y++ {
		for x := 0; x < 48; x++ {
			r, g, b := rgbAt(src, x, y)
			mx, mn := max3(r, g, b), min3(r, g, b)
			sat := 0.0
			if mx > 0 {
				sat = float64(mx-mn) / float64(mx)
			}
			key := int(r>>5)<<6 | int(g>>5)<<3 | int(b>>5)
			a := buckets[key]
			if a == nil {
				a = &acc{}
				buckets[key] = a
			}
			a.w += 0.15 + sat
			a.r += float64(r)
			a.g += float64(g)
			a.b += float64(b)
			a.n++
		}
	}
	var best *acc
	bestKey := -1
	for k, a := range buckets {
		if best == nil || a.w > best.w || (a.w == best.w && k < bestKey) {
			best, bestKey = a, k
		}
	}
	return uint8(best.r / best.n), uint8(best.g / best.n), uint8(best.b / best.n)
}

func max3(a, b, c uint8) uint8 {
	if b > a {
		a = b
	}
	if c > a {
		a = c
	}
	return a
}

func min3(a, b, c uint8) uint8 {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

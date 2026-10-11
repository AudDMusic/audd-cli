package output

import (
	"fmt"
	"strings"
)

// Plural formats a count with its noun: "1 file", "1,250 requests". The
// noun takes a plain "s" for any other count.
func Plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if strings.HasSuffix(noun, "ch") || strings.HasSuffix(noun, "sh") || strings.HasSuffix(noun, "s") || strings.HasSuffix(noun, "x") {
		return Thousands(n) + " " + noun + "es" // matches, boxes
	}
	return Thousands(n) + " " + noun + "s"
}

// Thousands formats n with comma thousands separators: 1250000 → "1,250,000".
func Thousands(n int) string {
	s := fmt.Sprint(n)
	start := 0
	if n < 0 {
		start = 1
	}
	for i := len(s) - 3; i > start; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

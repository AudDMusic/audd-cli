// Package art fetches cover art and draws it in the terminal: with the
// Kitty graphics protocol, iTerm2 inline images, or sixel when the terminal
// supports one of them, and with colored half-block characters otherwise.
package art

import "strings"

// Protocol is a way of drawing an image in a terminal.
type Protocol int

const (
	ProtoNone      Protocol = iota // no images (not a terminal, TERM=dumb, or turned off)
	ProtoKitty                     // Kitty graphics protocol (Kitty, Ghostty, WezTerm, Konsole)
	ProtoITerm2                    // iTerm2 inline images
	ProtoSixel                     // DEC sixel graphics
	ProtoHalfBlock                 // true-color "▀" cells
	// ProtoHalfBlock256 is the half-block fallback for terminals without
	// true color; colors are mapped to the 256-color palette.
	ProtoHalfBlock256
)

// String returns the name used by AUDD_ART.
func (p Protocol) String() string {
	switch p {
	case ProtoKitty:
		return "kitty"
	case ProtoITerm2:
		return "iterm2"
	case ProtoSixel:
		return "sixel"
	case ProtoHalfBlock:
		return "blocks"
	case ProtoHalfBlock256:
		return "blocks256"
	}
	return "none"
}

// IsGraphics reports whether p draws real pixels (Kitty, iTerm2, sixel)
// rather than colored characters.
func (p Protocol) IsGraphics() bool {
	return p == ProtoKitty || p == ProtoITerm2 || p == ProtoSixel
}

// Cells returns the character-cell fallback for p: half-blocks in true
// color or 256 colors, following the terminal's color support.
func Cells(env func(string) string) Protocol {
	if trueColor(env) {
		return ProtoHalfBlock
	}
	return ProtoHalfBlock256
}

func trueColor(env func(string) string) bool {
	ct := strings.ToLower(env("COLORTERM"))
	if ct == "truecolor" || ct == "24bit" {
		return true
	}
	switch env("TERM_PROGRAM") {
	case "iTerm.app", "WezTerm", "ghostty", "vscode", "Hyper":
		return true
	}
	t := env("TERM")
	return strings.Contains(t, "kitty") || strings.Contains(t, "ghostty") || strings.HasSuffix(t, "-direct") ||
		env("WT_SESSION") != "" || env("KITTY_WINDOW_ID") != ""
}

// Detect picks the best protocol from the environment. Images are only drawn
// on a terminal. AUDD_ART (kitty, iterm2, sixel, blocks, blocks256, none)
// overrides the detection. Inside tmux or screen, which do not pass images
// through by default, it uses half-blocks.
//
// Detect does not query the terminal. Sixel is found from TERM (names
// containing "sixel", foot, mlterm, contour); DetectFullScreen also asks
// the terminal (DA1) and so finds the others.
func Detect(env func(string) string, isTTY bool) Protocol {
	if !isTTY {
		return ProtoNone
	}
	if p, ok := forced(env); ok {
		return p
	}
	term := env("TERM")
	if term == "dumb" {
		return ProtoNone
	}
	if inMultiplexer(env) {
		return Cells(env)
	}
	prog := env("TERM_PROGRAM")
	switch {
	case env("KITTY_WINDOW_ID") != "" || term == "xterm-kitty":
		return ProtoKitty
	case env("GHOSTTY_RESOURCES_DIR") != "" || prog == "ghostty" || term == "xterm-ghostty":
		return ProtoKitty
	case env("WEZTERM_PANE") != "" || prog == "WezTerm":
		return ProtoKitty
	case env("KONSOLE_VERSION") != "":
		return ProtoKitty
	case prog == "iTerm.app" || env("LC_TERMINAL") == "iTerm2":
		return ProtoITerm2
	case strings.Contains(term, "sixel") || term == "foot" || strings.HasPrefix(term, "foot-") ||
		term == "mlterm" || term == "contour":
		return ProtoSixel
	}
	return Cells(env)
}

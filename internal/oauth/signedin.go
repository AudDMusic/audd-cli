package oauth

import (
	"fmt"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// SignedInLine is the one-line message that replaces the waiting block when
// a sign-in succeeds: "✓ Signed in as user@example.com (profile default).",
// in the success style.
func SignedInLine(st output.Styles, account, profile string) string {
	if account == "" {
		account = "your AudD account"
	}
	return st.OK.Render(fmt.Sprintf("✓ Signed in as %s (profile %s).", account, profile))
}

// noteAfter prints a warning now or, while a sign-in's waiting block is on
// screen, holds it until FlushNotes, so it is not erased with the block.
func (c *Client) noteAfter(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if c.out.Transient() == nil {
		c.out.Warn("%s", msg)
		return
	}
	c.notesMu.Lock()
	c.notes = append(c.notes, msg)
	c.notesMu.Unlock()
}

// FlushNotes prints the warnings held back during a sign-in. Call it after
// the waiting block was replaced by the success line (or left on screen).
func (c *Client) FlushNotes() {
	c.notesMu.Lock()
	notes := c.notes
	c.notes = nil
	c.notesMu.Unlock()
	for _, n := range notes {
		c.out.Warn("%s", n)
	}
}

// keepBlock leaves an open waiting block on screen (the sign-in failed) and
// prints the notes held back.
func (c *Client) keepBlock() {
	if b := c.out.Transient(); b != nil {
		b.Keep()
	}
	c.FlushNotes()
}

// signedIn replaces an open waiting block with the success line, then
// prints the notes held back. Without a block (no terminal, or output that
// is not for people) it prints only the notes.
func (c *Client) signedIn() {
	if b := c.out.Transient(); b != nil && b.Clear() {
		c.out.Warn("%s", SignedInLine(c.out.Styles(), c.profile.Account, c.profile.Name))
	}
	c.FlushNotes()
}

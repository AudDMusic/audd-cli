package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AudDMusic/audd-cli/internal/account"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func (m *explorer) usageView() string {
	switch {
	case m.usageErr != nil:
		e := output.AsError(m.usageErr)
		msg := "Could not load usage: " + e.Message
		if e.Hint != "" {
			msg += "\nTry: " + e.Hint
		}
		return m.st.Warn.Render(msg)
	case m.usage == nil:
		return m.st.Dim.Render("Loading…")
	}
	u := m.usage
	var b strings.Builder
	title := "This billing cycle"
	if u.CycleStart != "" && u.CycleEnd != "" {
		title += " (" + dayWords(u.CycleStart) + " to " + dayWords(u.CycleEnd) + ")"
	}
	b.WriteString(m.st.Bold.Render(title) + "\n\n")
	fmt.Fprintf(&b, "  %-12s %s\n", "Used", fmtInt(u.UsedThisCycle))
	if u.Allowance > 0 {
		fmt.Fprintf(&b, "  %-12s %s\n", "Allowance", fmtInt(u.Allowance))
	}
	fmt.Fprintf(&b, "  %-12s %s\n", "Remaining", fmtInt(u.Remaining))
	if u.Allowance > 0 {
		frac := float64(u.UsedThisCycle) / float64(u.Allowance)
		barW := min(50, m.w-20)
		if barW > 4 {
			n := int(max(0, min(1, frac))*float64(barW) + 0.5)
			fmt.Fprintf(&b, "  %s %d%%\n", m.st.Accent.Render(strings.Repeat("█", n))+m.st.Dim.Render(strings.Repeat("░", barW-n)), int(frac*100+0.5))
		}
	}
	if len(u.Days) > 0 {
		// The chart is the last days, which is not the billing cycle:
		// said, so the two never seem to contradict each other.
		head := fmt.Sprintf("Requests per day (last %d days)", len(u.Days))
		b.WriteString("\n" + m.st.Bold.Render(head) + "\n")
		if note := usageMismatch(u); note != "" {
			b.WriteString(m.st.Dim.Render(output.Wrap(note, max(20, m.w-2))) + "\n")
		}
		b.WriteString("\n")
		// The chart takes the rows left (and at least a few).
		used := strings.Count(b.String(), "\n") + 4
		b.WriteString(dayChart(u.Days, m.w-4, max(3, m.h-used), m.st))
		b.WriteString("\n")
	}
	note := "Plans and payments: audd billing plans · audd billing renew · audd billing buy <n>"
	if m.embedded {
		note = "Plans, payments, and more requests: → or enter opens the Billing tab."
	}
	b.WriteString("\n" + styleLines(m.st.Dim, output.Wrap(note, max(20, m.w-2))))
	return b.String()
}

// dayChart is a horizontal bar per day (most recent last), fitting h rows.
func dayChart(days []account.DayUsage, w, h int, st output.Styles) string {
	if h < 1 {
		h = 1
	}
	if len(days) > h {
		days = days[len(days)-h:]
	}
	peak := 1
	for _, d := range days {
		peak = max(peak, d.Requests)
	}
	barW := min(50, max(4, w-20))
	var lines []string
	for _, d := range days {
		n := max(0, d.Requests) * barW / peak // a malformed negative count draws no bar
		if d.Requests > 0 && n == 0 {
			n = 1
		}
		lines = append(lines, fmt.Sprintf("  %-10s %s %s", d.Date, st.Accent.Render(strings.Repeat("▇", n)), st.Dim.Render(fmtInt(d.Requests))))
	}
	return strings.Join(lines, "\n")
}

func fmtInt(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// dayWords is a date of the API ("2026-10-01") as "Oct 1, 2026".
func dayWords(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.Format("Jan 2, 2006")
}

// usageMismatch says, when the daily counts and the cycle's total do not
// agree, that both are as AudD reports them.
func usageMismatch(u *account.Usage) string {
	inCycle := 0
	for _, d := range u.Days {
		if (u.CycleStart == "" || d.Date >= u.CycleStart) && (u.CycleEnd == "" || d.Date < u.CycleEnd) {
			inCycle += d.Requests
		}
	}
	total := 0
	for _, d := range u.Days {
		total += d.Requests
	}
	if total > 0 && inCycle != u.UsedThisCycle && inCycle == 0 {
		return "These days are outside the billing cycle AudD reports, so they are not in Used above."
	}
	if inCycle > u.UsedThisCycle {
		return fmt.Sprintf("AudD reports %s used this cycle; the days below add up to %s.", fmtInt(u.UsedThisCycle), fmtInt(inCycle))
	}
	return ""
}

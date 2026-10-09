package tui

import (
	"fmt"
	"strconv"
	"strings"

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
	b.WriteString(m.st.Bold.Render("This billing cycle") + "\n\n")
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
		b.WriteString("\n" + m.st.Bold.Render("Requests per day") + "\n\n")
		b.WriteString(dayChart(u.Days, m.w-4, m.h-16, m.st))
		b.WriteString("\n")
	}
	b.WriteString("\n" + m.st.Dim.Render("Plans and payments: audd billing plans · audd billing renew · audd billing buy <n>"))
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

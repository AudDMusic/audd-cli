package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// mdHeading is a heading of a rendered document and the line it is on.
type mdHeading struct {
	level int
	text  string
	line  int
}

var (
	mdLink   = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	mdBold   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdCode   = regexp.MustCompile("`([^`]+)`")
	mdTblSep = regexp.MustCompile(`^\|?\s*:?-{2,}`)
)

// inlineMD drops markdown markup inside a line: links become "text (url)"
// (or the URL alone when it is the text), bold and code marks go.
func inlineMD(s string, st output.Styles) string {
	s = mdLink.ReplaceAllStringFunc(s, func(m string) string {
		p := mdLink.FindStringSubmatch(m)
		if p[1] == p[2] {
			return p[2]
		}
		return p[1] + " (" + p[2] + ")"
	})
	s = mdBold.ReplaceAllString(s, "$1")
	return mdCode.ReplaceAllStringFunc(s, func(m string) string {
		return st.Accent.Render(m[1 : len(m)-1])
	})
}

// splitTableRow splits a markdown table row into cells. A "\|" is a pipe
// inside a cell, not a cell boundary.
func splitTableRow(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	if strings.HasSuffix(row, "|") && !strings.HasSuffix(row, "\\|") {
		row = row[:len(row)-1]
	}
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(row); i++ {
		switch {
		case row[i] == '\\' && i+1 < len(row) && row[i+1] == '|':
			cur.WriteByte('|')
			i++
		case row[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(row[i])
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

// renderTable lays out table rows in aligned columns that fit a w-wide
// pane, wrapping the widest columns when they don't fit.
func renderTable(rows [][]string, w int, st output.Styles) []string {
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	cells := make([][]string, len(rows))
	widths := make([]int, cols)
	for i, r := range rows {
		cells[i] = make([]string, cols)
		for j := range cols {
			if j < len(r) {
				cells[i][j] = inlineMD(r[j], st)
			}
			widths[j] = max(widths[j], ansi.StringWidth(cells[i][j]))
		}
	}
	const indent, gap = 2, 3
	avail := max(cols*4, w-indent-gap*(cols-1))
	for {
		total := 0
		widest := 0
		for j, cw := range widths {
			total += cw
			if cw > widths[widest] {
				widest = j
			}
		}
		if total <= avail || widths[widest] <= 4 {
			break
		}
		widths[widest] = max(4, widths[widest]-(total-avail))
	}
	var out []string
	for i, r := range cells {
		wrapped := make([][]string, cols)
		height := 1
		for j, c := range r {
			wrapped[j] = strings.Split(output.Wrap(c, widths[j]), "\n")
			height = max(height, len(wrapped[j]))
		}
		for k := range height {
			var b strings.Builder
			b.WriteString(strings.Repeat(" ", indent))
			for j := range cols {
				part := ""
				if k < len(wrapped[j]) {
					part = ansi.Truncate(wrapped[j][k], widths[j], "…")
				}
				if i == 0 {
					part = st.Bold.Render(part)
				}
				b.WriteString(part)
				if j < cols-1 {
					b.WriteString(strings.Repeat(" ", widths[j]-ansi.StringWidth(part)+gap))
				}
			}
			out = append(out, strings.TrimRight(b.String(), " "))
		}
	}
	return out
}

// renderMarkdown lays out a markdown document for a w-wide pane: headings,
// wrapped paragraphs, lists, code blocks, and tables as they are. It
// returns the lines and the headings.
func renderMarkdown(md string, w int, st output.Styles) ([]string, []mdHeading) {
	var out []string
	var heads []mdHeading
	var para []string
	var table [][]string
	flushTable := func() {
		if len(table) > 0 {
			out = append(out, renderTable(table, w, st)...)
			table = nil
		}
	}
	flush := func() {
		flushTable()
		if len(para) == 0 {
			return
		}
		text := strings.Join(para, " ")
		para = nil
		indent := ""
		first := ""
		switch {
		case strings.HasPrefix(text, "- "), strings.HasPrefix(text, "* "):
			first, indent = "• ", "  "
			text = text[2:]
		}
		lines := strings.Split(output.Wrap(inlineMD(text, st), max(10, w-len(indent))), "\n")
		for i, l := range lines {
			if i == 0 {
				out = append(out, first+l)
			} else {
				out = append(out, indent+l)
			}
		}
	}
	inCode := false
	for _, raw := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(raw, " ")
		if strings.HasPrefix(line, "```") {
			flush()
			inCode = !inCode
			continue
		}
		if inCode {
			out = append(out, st.Dim.Render("  "+line))
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			flush()
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
		case strings.HasPrefix(trimmed, "#"):
			flush()
			level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
			text := strings.TrimSpace(trimmed[level:])
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			heads = append(heads, mdHeading{level: level, text: text, line: len(out)})
			style := st.Bold
			if level <= 2 {
				style = st.Title.Inherit(st.Accent)
			}
			label := text
			if level == 1 {
				label = strings.ToUpper(text)
			}
			out = append(out, style.Render(label))
		case strings.HasPrefix(trimmed, "|"):
			if len(para) > 0 {
				flush()
			}
			if mdTblSep.MatchString(trimmed) {
				continue
			}
			table = append(table, splitTableRow(trimmed))
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
			flush()
			para = append(para, trimmed)
		default:
			para = append(para, trimmed)
		}
	}
	flush()
	return out, heads
}

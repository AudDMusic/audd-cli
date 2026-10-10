package tui

import (
	"regexp"
	"strings"

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

// renderMarkdown lays out a markdown document for a w-wide pane: headings,
// wrapped paragraphs, lists, code blocks, and tables as they are. It
// returns the lines and the headings.
func renderMarkdown(md string, w int, st output.Styles) ([]string, []mdHeading) {
	var out []string
	var heads []mdHeading
	var para []string
	flush := func() {
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
			flush()
			if mdTblSep.MatchString(trimmed) {
				continue
			}
			cells := strings.Split(strings.Trim(trimmed, "|"), "|")
			for i := range cells {
				cells[i] = inlineMD(strings.TrimSpace(cells[i]), st)
			}
			out = append(out, "  "+strings.Join(cells, "  │  "))
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

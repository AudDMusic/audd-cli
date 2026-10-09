package tui

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// export writes the current list (filtered) to a file in the working
// directory.
func (m *explorer) export(format string) tea.Cmd {
	l := m.top()
	if l == nil {
		if m.tab == tabUsage && m.usage != nil {
			header := []string{"date", "requests"}
			var cells [][]string
			for _, d := range m.usage.Days {
				cells = append(cells, []string{d.Date, strconv.Itoa(d.Requests)})
			}
			return m.writeExport("usage", format, header, cells, m.usage)
		}
		return m.setFlash("Nothing to export")
	}
	rows := l.visible()
	header := make([]string, len(l.cols))
	for i, c := range l.cols {
		header[i] = c.title
	}
	var data []any
	var cells [][]string
	for _, r := range rows {
		data = append(data, r.data)
		cells = append(cells, r.cells)
	}
	return m.writeExport(l.kind, format, header, cells, data)
}

func (m *explorer) writeExport(kind, format string, header []string, cells [][]string, data any) tea.Cmd {
	name := fmt.Sprintf("audd-%s-%s.%s", kind, m.now().Format("20060102-150405"), format)
	path := filepath.Join(explorerExportDir, name)
	f, err := os.Create(path)
	if err != nil {
		return m.setFlash("Export failed: " + err.Error())
	}
	defer f.Close()
	var werr error
	if format == "csv" {
		werr = writeCSV(f, header, cells)
	} else {
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		werr = enc.Encode(map[string]any{"schema_version": output.SchemaVersion, "items": data})
	}
	if werr != nil {
		return m.setFlash("Export failed: " + werr.Error())
	}
	return m.setFlash("Saved " + name)
}

func writeCSV(w io.Writer, header []string, cells [][]string) error {
	cw := csv.NewWriter(w)
	if len(header) > 0 {
		if err := cw.Write(header); err != nil {
			return err
		}
	}
	for _, c := range cells {
		if err := cw.Write(c); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

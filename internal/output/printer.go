package output

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
)

// SchemaVersion is emitted in every JSON document and JSONL line.
const SchemaVersion = 1

// PrinterOptions configures a Printer.
type PrinterOptions struct {
	// Format is the resolved format (flag > AUDD_FORMAT > auto). Empty means
	// automatic: table on a TTY, json otherwise, jsonl after SetStreaming.
	Format  Format
	Fields  []string // --fields
	Quiet   bool
	NoColor bool // --no-color, NO_COLOR; FORCE_COLOR overrides

	StdoutTTY, StderrTTY, StdinTTY bool

	// StderrWidth is the terminal width of stderr; 0 reads it from the
	// terminal (80 when that fails).
	StderrWidth int
	// StdoutWidth is the terminal width of stdout; 0 reads it from the
	// terminal (80 when that fails).
	StdoutWidth int

	// Stdin is read by Confirm. Defaults to os.Stdin.
	Stdin io.Reader

	// Ask, when set, answers Confirm's questions instead of the terminal
	// (interactive mode runs commands in-process and asks in its own UI).
	Ask func(question string) bool
}

// Printer writes results to stdout and notes, progress, and errors to stderr,
// in the format chosen for this invocation. It is safe for concurrent use.
type Printer struct {
	// Separate locks so a human renderer passed to Result can call Info,
	// Warn, or Styles without deadlocking.
	stateMu sync.Mutex // format, styles
	outMu   sync.Mutex // stdout and CSV state
	errMu   sync.Mutex // stderr
	inMu    sync.Mutex // stdin (Confirm)
	linesMu sync.Mutex

	stdout   io.Writer
	stderr   io.Writer
	opts     PrinterOptions
	format   Format
	explicit bool

	csvw      *csv.Writer
	csvHeader []string
	// fieldsOK is set once --fields was checked against output that has
	// them, so later lines of a stream are not checked again.
	fieldsOK bool

	lines  *LineReader
	styles *Styles

	transient *TransientBlock // open block on stderr; guarded by errMu

	progMu sync.Mutex
	prog   *lineProgress // the progress line on stderr, while one is drawn
	noted  bool          // a note went to stderr; guarded by errMu
}

// NewPrinter returns a Printer. See PrinterOptions for defaults.
func NewPrinter(stdout, stderr io.Writer, opts PrinterOptions) *Printer {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	p := &Printer{stdout: stdout, stderr: stderr, opts: opts, format: opts.Format, explicit: opts.Format != ""}
	if p.format == "" {
		p.format = autoFormat(opts.StdoutTTY, false)
	}
	return p
}

// Format returns the active output format.
func (p *Printer) Format() Format {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.format
}

// Explicit reports whether the format came from --format or AUDD_FORMAT
// rather than automatic detection.
func (p *Printer) Explicit() bool {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.explicit
}

// IsHuman reports whether output is for people (table format).
func (p *Printer) IsHuman() bool { return p.Format() == FormatTable }

// Quiet reports whether --quiet is set.
func (p *Printer) Quiet() bool { return p.opts.Quiet }

// Fields returns the --fields selection.
func (p *Printer) Fields() []string { return p.opts.Fields }

// Options returns the options the Printer was built with.
func (p *Printer) Options() PrinterOptions { return p.opts }

// Stdout is the result stream (for TUIs and custom renderers).
func (p *Printer) Stdout() io.Writer { return p.stdout }

// Stderr is the stream for notes, progress, and errors.
func (p *Printer) Stderr() io.Writer { return p.stderr }

// SetStreaming marks the command as streaming: an automatically chosen json
// format becomes jsonl. Explicit formats and table mode are unchanged.
func (p *Printer) SetStreaming() {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if !p.explicit && p.format == FormatJSON {
		p.format = FormatJSONL
	}
}

// SetDocument undoes SetStreaming for a run that prints one document after
// all (a dry run): an automatically chosen jsonl format becomes json again.
func (p *Printer) SetDocument() {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if !p.explicit && p.format == FormatJSONL {
		p.format = FormatJSON
	}
}

// Result prints one result (a struct, map, or slice). In table mode it calls
// human(w), or renders a generic table when human is nil or --fields is set
// (--fields trims every format, so the table shows only those fields). In json it prints
// {"schema_version":1, <fields of v>} (slices as "items"). jsonl prints one
// "result" line per item. csv flattens v (or []v) into rows; nested keys are
// dotted ("extra.isrc") and --fields picks and orders columns.
func (p *Printer) Result(v any, human func(w io.Writer)) error {
	format := p.Format()
	if format == FormatTable && human != nil && len(p.opts.Fields) == 0 {
		human(p.out()) // not under a lock: human may call Info or Styles
		return nil
	}
	raw, err := marshalRaw(v)
	if err != nil {
		return err
	}
	p.outMu.Lock()
	defer p.outMu.Unlock()
	if err := p.checkResultFields(v, raw); err != nil {
		return err
	}
	switch format {
	case FormatTable:
		return p.genericTable(raw)
	case FormatJSONL:
		items := []json.RawMessage{raw}
		if kind(raw) == '[' {
			if items, err = parseArray(raw); err != nil {
				return err
			}
		}
		for _, it := range items {
			if err := p.writeLine(p.eventDoc("result", it)); err != nil {
				return err
			}
		}
		return nil
	case FormatCSV:
		items := []json.RawMessage{raw}
		if kind(raw) == '[' {
			if items, err = parseArray(raw); err != nil {
				return err
			}
		}
		return p.writeCSV(items, true, v)
	default:
		return p.writeLine(p.document(raw))
	}
}

// checkResultFields checks --fields against what they apply to: the object,
// or each item of a list.
func (p *Printer) checkResultFields(v any, raw json.RawMessage) error {
	if len(p.opts.Fields) == 0 || p.fieldsOK {
		return nil
	}
	var items []json.RawMessage
	switch kind(raw) {
	case '{':
		items = []json.RawMessage{raw}
	case '[':
		all, err := parseArray(raw)
		if err != nil {
			return nil
		}
		for _, it := range all {
			if kind(it) == '{' {
				items = append(items, it)
			}
		}
	default:
		return nil
	}
	if err := p.checkFields(v, items); err != nil {
		return err
	}
	if len(items) > 0 {
		p.fieldsOK = true
	}
	return nil
}

// document builds {"schema_version":1, ...} for a value.
func (p *Printer) document(raw json.RawMessage) object {
	doc := object{{Key: "schema_version", Val: json.RawMessage("1")}}
	switch kind(raw) {
	case '{':
		o, err := parseObject(raw)
		if err != nil {
			break
		}
		if len(p.opts.Fields) > 0 {
			o = selectFields(raw, p.opts.Fields)
		}
		return append(doc, o.without("schema_version")...)
	case '[':
		items, err := parseArray(raw)
		if err != nil {
			break
		}
		if len(p.opts.Fields) > 0 {
			for i, it := range items {
				items[i], _ = selectFields(it, p.opts.Fields).MarshalJSON()
			}
		}
		arr, _ := marshalRaw(items)
		return append(doc, member{Key: "items", Val: arr})
	}
	return append(doc, member{Key: "data", Val: raw})
}

// eventDoc builds {"schema_version":1,"type":kind, ...v}. --fields applies
// to "result" lines only: event, progress, and summary lines always keep
// their keys (job IDs, counts, status).
func (p *Printer) eventDoc(kindName string, raw json.RawMessage) object {
	t, _ := json.Marshal(kindName)
	doc := object{{Key: "schema_version", Val: json.RawMessage("1")}, {Key: "type", Val: t}}
	switch kind(raw) {
	case 'n':
		return doc
	case '{':
		o, err := parseObject(raw)
		if err == nil {
			if len(p.opts.Fields) > 0 && kindName == "result" {
				o = selectFields(raw, p.opts.Fields)
			}
			return append(doc, o.without("schema_version", "type")...)
		}
	}
	return append(doc, member{Key: "data", Val: raw})
}

func (p *Printer) writeLine(o object) error {
	b, err := o.MarshalJSON()
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = p.out().Write(b)
	return err
}

// Event writes one streaming record. jsonl and json print
// {"schema_version":1,"type":kind, ...v}; csv writes "result" events as rows
// and drops other kinds; table prints result events as a compact line.
func (p *Printer) Event(kindName string, v any) error {
	raw, err := marshalRaw(v)
	if err != nil {
		return err
	}
	format := p.Format()
	if format == FormatTable && kindName != "result" {
		p.errMu.Lock()
		defer p.errMu.Unlock()
	} else {
		p.outMu.Lock()
		defer p.outMu.Unlock()
	}
	if kindName == "result" {
		if err := p.checkResultFields(v, raw); err != nil {
			return err
		}
	}
	switch format {
	case FormatCSV:
		if kindName != "result" {
			return nil
		}
		return p.writeCSV([]json.RawMessage{raw}, false, v)
	case FormatTable:
		var parts []string
		for _, c := range flatten(raw) {
			if c.Val == "" || c.Key == "type" || c.Key == "schema_version" {
				continue
			}
			if p.wantField(c.Key) {
				parts = append(parts, c.Key+"="+c.Val)
			}
		}
		w := p.out()
		line := fmt.Sprintf("%s  %s\n", kindName, strings.Join(parts, "  "))
		if kindName != "result" {
			p.writeErrLocked(line)
			return nil
		}
		_, err := io.WriteString(w, line)
		return err
	default:
		return p.writeLine(p.eventDoc(kindName, raw))
	}
}

func (p *Printer) wantField(key string) bool {
	if len(p.opts.Fields) == 0 {
		return true
	}
	for _, f := range p.opts.Fields {
		if f == key || strings.HasPrefix(key, f+".") {
			return true
		}
	}
	return false
}

// writeCSV writes rows. When the header has not been written yet it is taken
// from --fields, else from the union of keys across items (first-seen order).
// Later streaming rows with new keys keep the original columns.
func (p *Printer) writeCSV(items []json.RawMessage, union bool, v any) error {
	rows := make([]map[string]string, len(items))
	var keys []string
	seen := map[string]bool{}
	for i, it := range items {
		rows[i] = map[string]string{}
		for _, c := range flatten(it) {
			rows[i][c.Key] = c.Val
			if !seen[c.Key] {
				seen[c.Key] = true
				keys = append(keys, c.Key)
			}
		}
		if !union && i == 0 {
			break
		}
	}
	if p.csvw == nil {
		header := keys
		if len(p.opts.Fields) > 0 {
			header = append([]string(nil), p.opts.Fields...)
		} else if len(header) == 0 {
			// No rows: take the columns from the row type, so an empty
			// export still has its header.
			if sample := zeroSample(v); sample != nil {
				for _, c := range flatten(sample) {
					header = append(header, c.Key)
				}
			}
		}
		if len(header) == 0 {
			return nil // nothing known to write, not even a header
		}
		p.csvw = csv.NewWriter(p.out())
		p.csvHeader = header
		if err := p.csvw.Write(p.csvHeader); err != nil {
			return err
		}
	}
	for _, r := range rows {
		rec := make([]string, len(p.csvHeader))
		for j, k := range p.csvHeader {
			rec[j] = r[k]
		}
		if err := p.csvw.Write(rec); err != nil {
			return err
		}
	}
	p.csvw.Flush()
	return p.csvw.Error()
}

// genericTable renders a value without a custom human view: an object as
// "key  value" lines, a list of objects as columns.
func (p *Printer) genericTable(raw json.RawMessage) error {
	tw := tabwriter.NewWriter(p.out(), 0, 4, 2, ' ', 0)
	if kind(raw) == '[' {
		items, err := parseArray(raw)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Fprintln(p.out(), "Nothing to show.")
			return nil
		}
		var cols []string
		seen := map[string]bool{}
		rows := make([]map[string]string, len(items))
		for i, it := range items {
			rows[i] = map[string]string{}
			for _, c := range flatten(it) {
				if !p.wantField(c.Key) {
					continue
				}
				rows[i][c.Key] = c.Val
				if !seen[c.Key] {
					seen[c.Key] = true
					cols = append(cols, c.Key)
				}
			}
		}
		fmt.Fprintln(tw, strings.ToUpper(strings.Join(cols, "\t")))
		for _, r := range rows {
			vals := make([]string, len(cols))
			for j, c := range cols {
				vals[j] = r[c]
			}
			fmt.Fprintln(tw, strings.Join(vals, "\t"))
		}
		return tw.Flush()
	}
	for _, c := range flatten(raw) {
		if c.Key == "schema_version" || !p.wantField(c.Key) {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\n", c.Key, c.Val)
	}
	return tw.Flush()
}

// Error prints err to stderr (human text, or JSON for machine formats) and
// returns the exit code. A nil error returns ExitOK and prints nothing.
func (p *Printer) Error(err error) int {
	if err == nil {
		return ExitOK
	}
	e := AsError(err)
	exit := ExitCode(e)
	format := p.Format()
	p.errMu.Lock()
	defer p.errMu.Unlock()
	if format == FormatTable {
		var b strings.Builder
		fmt.Fprintf(&b, "Error: %s\n", e.Message)
		if e.Hint != "" {
			fmt.Fprintf(&b, "Try: %s\n", e.Hint)
		}
		if docs := docsFor(e, exit); docs != "" {
			fmt.Fprintf(&b, "Docs: %s\n", docs)
		}
		p.writeErrLocked(Wrap(b.String(), p.noteWidth()))
		return exit
	}
	body := struct {
		Code      string `json:"code"`
		APICode   int    `json:"api_code"`
		Message   string `json:"message"`
		Hint      string `json:"hint"`
		Retryable bool   `json:"retryable"`
	}{e.Code, e.APICode, e.Message, e.Hint, e.Retryable}
	raw, _ := marshalRaw(struct {
		SchemaVersion int `json:"schema_version"`
		Error         any `json:"error"`
	}{SchemaVersion, body})
	p.writeErrLocked(string(raw) + "\n")
	return exit
}

// CLIDocsURL is the CLI reference: commands, limits, output, and exit
// codes (also offline with audd docs cli).
const CLIDocsURL = "https://github.com/AudDMusic/audd-cli/blob/main/docs/cli.md"

// docsFor is the docs link shown with an error: its own, or the CLI
// reference for usage, account, safety, and partial-failure errors, whose
// exit codes and flags it explains.
func docsFor(e *Error, exit int) string {
	if e.DocsURL != "" {
		return e.DocsURL
	}
	switch exit {
	case ExitUsage, ExitQuota, ExitSafety, ExitPartial:
		return CLIDocsURL
	}
	return ""
}

// Info writes a note to stderr. Suppressed by --quiet; never on stdout.
func (p *Printer) Info(format string, args ...any) {
	if p.opts.Quiet {
		return
	}
	p.Warn(format, args...)
}

// Warn writes a note to stderr even with --quiet.
func (p *Printer) Warn(format string, args ...any) {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	msg := Wrap(fmt.Sprintf(format, args...), p.noteWidth())
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	p.writeErrLocked(msg)
	p.noted = true
}

// SeparateNotes prints an empty line on stderr when notes were printed
// before, so they stand apart from the result that follows. It does so only
// for people (table format) and not with --quiet.
func (p *Printer) SeparateNotes() {
	if !p.IsHuman() || p.opts.Quiet {
		return
	}
	p.errMu.Lock()
	defer p.errMu.Unlock()
	if p.noted {
		p.writeErrLocked("\n")
		p.noted = false
	}
}

// StdinLines is the shared line reader over the printer's stdin. Every
// reader of terminal input in the process goes through it, so input typed
// after one reader stopped waiting reaches the next one.
func (p *Printer) StdinLines() *LineReader {
	p.linesMu.Lock()
	defer p.linesMu.Unlock()
	if p.lines == nil {
		in := p.opts.Stdin
		if in == nil {
			in = strings.NewReader("")
		}
		p.lines = NewLineReader(in)
	}
	return p.lines
}

// CanAsk reports whether Confirm can put a question to a person: stdin is
// a terminal, or an Ask hook is set.
func (p *Printer) CanAsk() bool { return p.opts.StdinTTY || p.opts.Ask != nil }

// Confirm asks a yes/no question on stderr. yes=true (from --yes) skips the
// question. Without a TTY on stdin it returns confirmation_required (exit 6).
// Any answer other than y/yes returns "declined" (exit 6).
func (p *Printer) Confirm(question string, yes bool) error {
	if yes {
		return nil
	}
	if p.opts.Ask != nil {
		if p.opts.Ask(question) {
			return nil
		}
		return &Error{Code: "declined", Message: "cancelled", Exit: ExitSafety}
	}
	if !p.opts.StdinTTY {
		return &Error{
			Code:    "confirmation_required",
			Message: "this needs confirmation, and there is no terminal to ask: " + strings.TrimSuffix(question, "?"),
			Hint:    "pass --yes to confirm",
			Exit:    ExitSafety,
		}
	}
	p.inMu.Lock()
	defer p.inMu.Unlock()
	p.errMu.Lock()
	p.writeErrLocked(Wrap(question+" [y/N]", p.noteWidth()) + " ")
	p.noted = true
	p.errMu.Unlock()
	line, _ := p.StdinLines().ReadLine(context.Background())
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return &Error{Code: "declined", Message: "cancelled", Exit: ExitSafety}
}

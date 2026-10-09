package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestResolveFormat(t *testing.T) {
	tests := []struct {
		name      string
		flag, env string
		tty       bool
		streaming bool
		want      Format
		explicit  bool
		wantErr   bool
	}{
		{"flag wins over env", "csv", "json", true, false, FormatCSV, true, false},
		{"env when no flag", "", "jsonl", true, false, FormatJSONL, true, false},
		{"tty auto is table", "", "", true, false, FormatTable, false, false},
		{"pipe auto is json", "", "", false, false, FormatJSON, false, false},
		{"pipe streaming is jsonl", "", "", false, true, FormatJSONL, false, false},
		{"tty streaming stays table", "", "", true, true, FormatTable, false, false},
		{"explicit json stays json when streaming", "json", "", false, true, FormatJSON, true, false},
		{"case insensitive", "JSON", "", true, false, FormatJSON, true, false},
		{"bad flag", "yaml", "", true, false, "", false, true},
		{"bad env", "", "xml", true, false, "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, explicit, err := ResolveFormat(tt.flag, tt.env, tt.tty, tt.streaming)
			if tt.wantErr {
				var oe *Error
				if !errors.As(err, &oe) || oe.Exit != ExitUsage {
					t.Fatalf("want usage error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || explicit != tt.explicit {
				t.Fatalf("got %q explicit=%v, want %q explicit=%v", got, explicit, tt.want, tt.explicit)
			}
		})
	}
}

func TestSetStreamingSwitchesAutoJSON(t *testing.T) {
	auto := NewPrinter(io.Discard, io.Discard, PrinterOptions{})
	auto.SetStreaming()
	if auto.Format() != FormatJSONL {
		t.Fatalf("auto json should become jsonl, got %s", auto.Format())
	}
	explicit := NewPrinter(io.Discard, io.Discard, PrinterOptions{Format: FormatJSON})
	explicit.SetStreaming()
	if explicit.Format() != FormatJSON {
		t.Fatalf("explicit json must stay json, got %s", explicit.Format())
	}
	tty := NewPrinter(io.Discard, io.Discard, PrinterOptions{StdoutTTY: true})
	tty.SetStreaming()
	if tty.Format() != FormatTable || !tty.IsHuman() {
		t.Fatalf("tty should stay table, got %s", tty.Format())
	}
}

type song struct {
	Artist string  `json:"artist"`
	Title  string  `json:"title"`
	Album  *string `json:"album"`
	Extra  struct {
		ISRC string `json:"isrc"`
	} `json:"extra"`
	Genres []string `json:"genres,omitempty"`
}

func newSong() song {
	s := song{Artist: "Imagine Dragons", Title: "Warriors"}
	s.Extra.ISRC = "USUM71414155"
	s.Genres = []string{"Rock", "Pop"}
	return s
}

func TestResultJSON(t *testing.T) {
	var out bytes.Buffer
	p := NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatJSON})
	if err := p.Result(newSong(), func(io.Writer) { t.Fatal("human called in json mode") }); err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"artist":"Imagine Dragons","title":"Warriors","album":null,"extra":{"isrc":"USUM71414155"},"genres":["Rock","Pop"]}` + "\n"
	if out.String() != want {
		t.Fatalf("got  %s\nwant %s", out.String(), want)
	}
}

func TestResultJSONFields(t *testing.T) {
	var out bytes.Buffer
	p := NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatJSON, Fields: []string{"title", "extra.isrc"}})
	if err := p.Result(newSong(), nil); err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"title":"Warriors","extra":{"isrc":"USUM71414155"}}` + "\n"
	if out.String() != want {
		t.Fatalf("got  %s\nwant %s", out.String(), want)
	}
}

func TestUnknownFieldsAreAUsageError(t *testing.T) {
	for _, format := range []Format{FormatJSON, FormatJSONL, FormatCSV, FormatTable} {
		var out bytes.Buffer
		p := NewPrinter(&out, io.Discard, PrinterOptions{Format: format, Fields: []string{"title", "extra.isr", "missing"}})
		err := p.Result(newSong(), func(io.Writer) {})
		e := AsError(err)
		if ExitCode(err) != ExitUsage || e.Code != "invalid_argument" || out.Len() != 0 {
			t.Fatalf("%s: %v (stdout %q)", format, err, out.String())
		}
		for _, s := range []string{`"extra.isr", "missing"`, "available fields: artist, title, album, extra.isrc, genres"} {
			if !strings.Contains(e.Message, s) {
				t.Fatalf("%s: message %q lacks %q", format, e.Message, s)
			}
		}
	}
}

func TestFieldsThatMayBeAbsent(t *testing.T) {
	type line struct {
		Input  string          `json:"input"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code string `json:"code"`
		} `json:"error,omitempty"`
	}
	for _, fields := range [][]string{
		{"input", "result.artist"}, // result is null: no match
		{"error.code"},             // declared, left out when empty
		{"result.apple_music.url"}, // only with --return
	} {
		var out bytes.Buffer
		p := NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatJSONL, Fields: fields})
		if err := p.Event("result", line{Input: "a.mp3", Result: json.RawMessage(`{"artist":"A"}`)}); err != nil {
			t.Fatalf("%v: %v", fields, err)
		}
		if err := p.Event("result", line{Input: "b.mp3", Result: json.RawMessage(`null`)}); err != nil {
			t.Fatalf("%v: %v", fields, err)
		}
	}
	var out bytes.Buffer
	p := NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatJSON, Fields: []string{"genres"}})
	if err := p.Result(song{Title: "x"}, nil); err != nil {
		t.Fatalf("omitempty field: %v", err)
	}
	p = NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatJSONL, Fields: []string{"result.artis"}})
	if err := p.Event("result", line{Input: "a.mp3", Result: json.RawMessage(`{"artist":"A"}`)}); ExitCode(err) != ExitUsage {
		t.Fatalf("typo inside the result: %v", err)
	}
}

func TestEmptyCSVHasAHeader(t *testing.T) {
	var out bytes.Buffer
	p := NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatCSV})
	if err := p.Result([]song{}, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "artist,title,album,extra.isrc\n" {
		t.Fatalf("got %q", out.String())
	}
	out.Reset()
	p = NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatCSV})
	if err := p.Result([]map[string]any{}, nil); err != nil || out.String() != "" {
		t.Fatalf("no known columns, no blank line: %q %v", out.String(), err)
	}
}

func TestResultJSONSliceAndExistingSchemaVersion(t *testing.T) {
	var out bytes.Buffer
	p := NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatJSON, Fields: []string{"title"}})
	if err := p.Result([]song{newSong(), {Title: "Believer"}}, nil); err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"items":[{"title":"Warriors"},{"title":"Believer"}]}` + "\n"
	if out.String() != want {
		t.Fatalf("got  %s\nwant %s", out.String(), want)
	}

	out.Reset()
	p = NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatJSON})
	if err := p.Result(map[string]any{"schema_version": 7, "a": 1}, nil); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != `{"schema_version":1,"a":1}`+"\n" {
		t.Fatalf("got %s", got)
	}
}

func TestResultHumanAndQuiet(t *testing.T) {
	var out bytes.Buffer
	p := NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatTable})
	called := false
	if err := p.Result(newSong(), func(w io.Writer) { called = true; io.WriteString(w, "Imagine Dragons — Warriors\n") }); err != nil {
		t.Fatal(err)
	}
	if !called || out.String() != "Imagine Dragons — Warriors\n" {
		t.Fatalf("human not used: %q", out.String())
	}

	out.Reset()
	if err := p.Result(newSong(), nil); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"artist", "Imagine Dragons", "extra.isrc", "USUM71414155"} {
		if !strings.Contains(out.String(), s) {
			t.Fatalf("default table missing %q:\n%s", s, out.String())
		}
	}
}

func TestResultCSV(t *testing.T) {
	var out bytes.Buffer
	p := NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatCSV})
	if err := p.Result([]song{newSong(), {Artist: "A, B", Title: "Q\"x"}}, nil); err != nil {
		t.Fatal(err)
	}
	want := "artist,title,album,extra.isrc,genres\n" +
		"Imagine Dragons,Warriors,,USUM71414155,\"[\"\"Rock\"\",\"\"Pop\"\"]\"\n" +
		"\"A, B\",\"Q\"\"x\",,,\n"
	if out.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", out.String(), want)
	}

	out.Reset()
	p = NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatCSV, Fields: []string{"extra.isrc", "artist"}})
	if err := p.Result(newSong(), nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "extra.isrc,artist\nUSUM71414155,Imagine Dragons\n" {
		t.Fatalf("got %q", out.String())
	}
}

func TestEventJSONLAndCSV(t *testing.T) {
	var out bytes.Buffer
	p := NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatJSONL})
	_ = p.Event("result", map[string]any{"file": "a.mp3", "type": "ignored"})
	_ = p.Event("summary", nil)
	_ = p.Event("progress", []int{1, 2})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{
		`{"schema_version":1,"type":"result","file":"a.mp3"}`,
		`{"schema_version":1,"type":"summary"}`,
		`{"schema_version":1,"type":"progress","data":[1,2]}`,
	}
	if len(lines) != len(want) {
		t.Fatalf("lines: %q", lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d: got %s want %s", i, lines[i], want[i])
		}
	}

	out.Reset()
	var errBuf bytes.Buffer
	p = NewPrinter(&out, &errBuf, PrinterOptions{Format: FormatCSV})
	_ = p.Event("result", map[string]any{"file": "a.mp3", "n": 1})
	_ = p.Event("progress", map[string]any{"done": 1})
	_ = p.Event("result", map[string]any{"file": "b.mp3", "n": 2})
	if out.String() != "file,n\na.mp3,1\nb.mp3,2\n" {
		t.Fatalf("csv events: %q", out.String())
	}
}

func TestErrorPrinting(t *testing.T) {
	e := Errf(ExitAuth, "no_token", "audd login", "no API token found for profile %q", "default")
	e.DocsURL = "https://docs.audd.io"
	e.APICode = 900

	var stderr bytes.Buffer
	p := NewPrinter(io.Discard, &stderr, PrinterOptions{Format: FormatTable})
	if code := p.Error(e); code != ExitAuth {
		t.Fatalf("exit %d", code)
	}
	want := "Error: no API token found for profile \"default\"\nTry: audd login\nDocs: https://docs.audd.io\n"
	if stderr.String() != want {
		t.Fatalf("human error:\n%q\nwant\n%q", stderr.String(), want)
	}

	stderr.Reset()
	p = NewPrinter(io.Discard, &stderr, PrinterOptions{Format: FormatJSON})
	if code := p.Error(e); code != ExitAuth {
		t.Fatalf("exit %d", code)
	}
	var doc struct {
		SchemaVersion int `json:"schema_version"`
		Error         map[string]any
	}
	if err := json.Unmarshal(stderr.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != 1 || doc.Error["code"] != "no_token" || doc.Error["api_code"].(float64) != 900 ||
		doc.Error["hint"] != "audd login" || doc.Error["retryable"] != false || doc.Error["message"] == "" {
		t.Fatalf("json error: %s", stderr.String())
	}
	if len(doc.Error) != 5 {
		t.Fatalf("json error must have exactly code, api_code, message, hint, retryable: %s", stderr.String())
	}

	stderr.Reset()
	if code := p.Error(errors.New("boom")); code != ExitUnexpected {
		t.Fatalf("plain error exit %d", code)
	}
	if !strings.Contains(stderr.String(), `"code":"unexpected"`) {
		t.Fatalf("plain error: %s", stderr.String())
	}

	if code := p.Error(nil); code != ExitOK {
		t.Fatalf("nil error exit %d", code)
	}
}

func TestConfirm(t *testing.T) {
	p := NewPrinter(io.Discard, io.Discard, PrinterOptions{})
	if err := p.Confirm("Proceed?", true); err != nil {
		t.Fatalf("yes should pass: %v", err)
	}
	err := p.Confirm("Proceed?", false)
	var oe *Error
	if !errors.As(err, &oe) || oe.Exit != ExitSafety || oe.Code != "confirmation_required" || !strings.Contains(oe.Hint, "--yes") {
		t.Fatalf("non-tty confirm: %#v", err)
	}

	var stderr bytes.Buffer
	p = NewPrinter(io.Discard, &stderr, PrinterOptions{StdinTTY: true, Stdin: strings.NewReader("y\n")})
	if err := p.Confirm("Proceed?", false); err != nil {
		t.Fatalf("answered yes: %v", err)
	}
	if !strings.Contains(stderr.String(), "Proceed? [y/N]") {
		t.Fatalf("prompt: %q", stderr.String())
	}
	p = NewPrinter(io.Discard, io.Discard, PrinterOptions{StdinTTY: true, Stdin: strings.NewReader("\n")})
	if err := p.Confirm("Proceed?", false); !errors.As(err, &oe) || oe.Code != "declined" {
		t.Fatalf("answered no: %v", err)
	}
}

func TestInfoQuietAndProgress(t *testing.T) {
	var stdout, stderr bytes.Buffer
	p := NewPrinter(&stdout, &stderr, PrinterOptions{Quiet: true})
	p.Info("hello %d", 1)
	if stderr.Len() != 0 || stdout.Len() != 0 {
		t.Fatal("quiet must suppress info")
	}
	p = NewPrinter(&stdout, &stderr, PrinterOptions{})
	p.Info("hello %d", 1)
	if stderr.String() != "hello 1\n" || stdout.Len() != 0 {
		t.Fatalf("info: %q", stderr.String())
	}

	stderr.Reset()
	pr := p.Progress()
	pr.Start(3, "files")
	pr.Inc(1)
	pr.SetLabel("x")
	pr.Done()
	if stderr.Len() != 0 {
		t.Fatal("progress must be silent when stderr is not a tty")
	}
	p = NewPrinter(&stdout, &stderr, PrinterOptions{StderrTTY: true})
	pr = p.Progress()
	pr.Start(2, "files")
	pr.Inc(2)
	pr.Done()
	if !strings.Contains(stderr.String(), "2/2") {
		t.Fatalf("progress output: %q", stderr.String())
	}
}

func TestResolveNoColor(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if !ResolveNoColor(true, env(nil), true) {
		t.Fatal("flag disables color")
	}
	if !ResolveNoColor(false, env(map[string]string{"NO_COLOR": "1"}), true) {
		t.Fatal("NO_COLOR disables color")
	}
	if ResolveNoColor(false, env(map[string]string{"NO_COLOR": "1", "FORCE_COLOR": "1"}), true) {
		t.Fatal("FORCE_COLOR overrides NO_COLOR")
	}
	if !ResolveNoColor(false, env(nil), false) {
		t.Fatal("no tty means no color")
	}
	if ResolveNoColor(false, env(map[string]string{"FORCE_COLOR": "1"}), false) {
		t.Fatal("FORCE_COLOR forces color without tty")
	}
	if ResolveNoColor(false, env(nil), true) {
		t.Fatal("tty default is color")
	}
}

func TestStylesNoColor(t *testing.T) {
	p := NewPrinter(io.Discard, io.Discard, PrinterOptions{NoColor: true})
	if got := p.Styles().Bold.Render("x"); got != "x" {
		t.Fatalf("no-color styles must not emit escapes: %q", got)
	}
}

func TestHumanRendererMayUsePrinter(t *testing.T) {
	var out, errb bytes.Buffer
	p := NewPrinter(&out, &errb, PrinterOptions{Format: FormatTable, NoColor: true})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p.Result(1, func(w io.Writer) {
			p.Info("note")
			io.WriteString(w, p.Styles().Bold.Render("x")+"\n")
			_ = p.Event("progress", map[string]int{"done": 1})
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: human renderer calling Info/Styles/Event")
	}
	if out.String() != "x\n" || !strings.Contains(errb.String(), "note") {
		t.Fatalf("out %q err %q", out.String(), errb.String())
	}
}

func TestPluralAndThousands(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{{0, "0 requests"}, {1, "1 request"}, {2, "2 requests"}, {1000, "1,000 requests"}, {1250000, "1,250,000 requests"}} {
		if got := Plural(c.n, "request"); got != c.want {
			t.Errorf("Plural(%d) = %q, want %q", c.n, got, c.want)
		}
	}
	if got := Thousands(-1234); got != "-1,234" {
		t.Errorf("Thousands(-1234) = %q", got)
	}
	if got := Thousands(-123); got != "-123" {
		t.Errorf("Thousands(-123) = %q", got)
	}
}

func TestFieldsApplyToResultLinesOnly(t *testing.T) {
	var out bytes.Buffer
	p := NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatJSONL, Fields: []string{"input", "result.title"}})
	p.Event("event", map[string]any{"event": "job_started", "job_id": "zyqq9f", "total": 2})
	p.Event("result", map[string]any{"input": "a.mp3", "result": map[string]any{"title": "T", "artist": "A"}})
	p.Event("summary", map[string]any{"job_id": "zyqq9f", "status": "done", "recognized": 1, "failed": 0})
	want := `{"schema_version":1,"type":"event","event":"job_started","job_id":"zyqq9f","total":2}
{"schema_version":1,"type":"result","input":"a.mp3","result":{"title":"T"}}
{"schema_version":1,"type":"summary","failed":0,"job_id":"zyqq9f","recognized":1,"status":"done"}
`
	if out.String() != want {
		t.Fatalf("got\n%s", out.String())
	}
}

func TestResultTableFieldsSkipsTheHumanRenderer(t *testing.T) {
	var out bytes.Buffer
	p := NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatTable, Fields: []string{"title", "extra.isrc"}})
	called := false
	if err := p.Result(newSong(), func(w io.Writer) { called = true; io.WriteString(w, "card\n") }); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if called || strings.Contains(got, "card") || strings.Contains(got, "Imagine Dragons") {
		t.Fatalf("--fields ignored in table mode: %q", got)
	}
	if !strings.Contains(got, "Warriors") || !strings.Contains(got, "USUM71414155") {
		t.Fatalf("selected fields missing: %q", got)
	}

	out.Reset()
	p = NewPrinter(&out, io.Discard, PrinterOptions{Format: FormatTable, Fields: []string{"radio_id"}})
	if err := p.Result([]map[string]any{{"radio_id": 1, "url": "https://x"}, {"radio_id": 2, "url": "https://y"}}, func(w io.Writer) { io.WriteString(w, "card\n") }); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Contains(got, "URL") || strings.Contains(got, "https://") || !strings.Contains(got, "RADIO_ID") {
		t.Fatalf("table columns: %q", got)
	}
}

func TestErrorDocsFallback(t *testing.T) {
	var stderr bytes.Buffer
	p := NewPrinter(io.Discard, &stderr, PrinterOptions{Format: FormatTable})
	p.Error(Errf(ExitSafety, "confirmation_required", "add --yes", "this run needs a confirmation"))
	if !strings.Contains(stderr.String(), "Docs: "+CLIDocsURL+"\n") {
		t.Fatalf("safety error without a docs link: %q", stderr.String())
	}
	stderr.Reset()
	p.Error(Errf(ExitNetwork, "network", "try again", "could not reach AudD"))
	if strings.Contains(stderr.String(), "Docs:") {
		t.Fatalf("network errors need no docs link: %q", stderr.String())
	}
}

func TestWrapAndFit(t *testing.T) {
	got := Wrap("Plan: 12 files, stops after 5 of 12 requests (--max-requests 5)\n  indented line that is long", 30)
	want := "Plan: 12 files, stops after 5\nof 12 requests (--max-requests\n5)\n  indented line that is long"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if got := Wrap("a https://example.com/a-very-long-address-that-cannot-break b", 20); got != "a\nhttps://example.com/a-very-long-address-that-cannot-break\nb" {
		t.Fatalf("long word: %q", got)
	}
	if Wrap("short", 0) != "short" {
		t.Fatal("width 0 leaves text alone")
	}

	var b bytes.Buffer
	WriteKeyValues(&b, [][2]string{{"Profile", "default"}, {"Permissions", "account:read billing:pay billing:read openid profile:read token:read"}}, 40)
	if b.String() != "Profile      default\nPermissions  account:read billing:pay\n             billing:read openid\n             profile:read token:read\n" {
		t.Fatalf("key values:\n%s", b.String())
	}

	rows := [][]string{{"ID", "URL"}, {"1", "https://radio.example/a/very/long/stream/address.mp3"}}
	b.Reset()
	WriteTable(&b, rows, 30, 1)
	if b.String() != "ID  URL\n1   https://radio.example/a/v…\n" {
		t.Fatalf("table:\n%s", b.String())
	}
}

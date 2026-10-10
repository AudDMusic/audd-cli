package tui

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/agent"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// fakeTree is a small command tree for the palette and Help tests.
func fakeTree() *cobra.Command {
	run := func(cmd *cobra.Command, args []string) error { return nil }
	root := &cobra.Command{Use: "audd"}
	rec := &cobra.Command{Use: "recognize <file|url|dir|glob|->", Short: "Identify music in files, URLs, and folders", RunE: run,
		Long: "Identify the music in a file, a URL, a folder, a glob, or a list of files.", Example: "  audd recognize song.mp3"}
	rec.Flags().Bool("enterprise", false, "scan the whole file")
	rec.Flags().String("limit", "", "with --enterprise: at most N 12-second chunks per file, or none")
	agent.SetRequiredWhen(rec.Flags(), "limit", "with --enterprise")
	rec.Flags().String("return", "", "extra metadata")
	rec.Flags().Int("concurrency", 0, "parallel requests")
	streams := &cobra.Command{Use: "streams", Short: "Manage stream monitoring"}
	add := &cobra.Command{Use: "add <url> --id N", Short: "Add a stream to monitor", RunE: run}
	add.Flags().Int("id", 0, "the stream's ID")
	_ = add.MarkFlagRequired("id")
	add.Flags().Bool("start", false, "results when songs start")
	remove := &cobra.Command{Use: "remove <id>", Short: "Stop monitoring a stream", RunE: run}
	report := &cobra.Command{Use: "report", Short: "Plays grouped", RunE: run}
	report.Flags().String("by", "song", "group by")
	_ = report.RegisterFlagCompletionFunc("by", cobra.FixedCompletions([]string{"song", "artist", "label", "station"}, cobra.ShellCompDirectiveNoFileComp))
	report.Flags().StringSlice("fields", nil, "fields")
	streams.AddCommand(add, remove, report)
	np := &cobra.Command{Use: "now-playing [radio-id...]", Short: "Show the latest songs on your streams", RunE: run}
	comp := &cobra.Command{Use: "completion bash|zsh|fish|powershell", Short: "Print a shell completion script", RunE: run,
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"}}
	docs := &cobra.Command{Use: "docs [topic]", Short: "Print AudD documentation as markdown", RunE: run, ValidArgs: []string{"api", "cli"}}
	secret := &cobra.Command{Use: "secret", Short: "hidden", Hidden: true, RunE: run}
	root.AddCommand(rec, streams, np, comp, docs, secret)
	return root
}

func useFakeTree(t *testing.T) {
	old := HomeCommands
	HomeCommands = fakeTree
	t.Cleanup(func() { HomeCommands = old })
}

func TestPaletteList(t *testing.T) {
	useFakeTree(t)
	snap(t, "palette_list", func() *home { return newTestHome(t, &fakeRun{}, homeOpts{start: "recognize"}) },
		"Recognize music", ctrlK(), "Run a command")
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{}), 120, 40)
	press(h, ctrlK())
	v := h.View()
	if strings.Contains(v, "secret") || !strings.Contains(v, "streams add") || strings.Contains(v, "Manage stream monitoring") {
		t.Fatalf("list: hidden commands and groups are left out:\n%s", v)
	}
	press(h, typeText("sadd")...)
	if it := h.palette.picker.selected(); it == nil || it.value != "streams add" {
		t.Fatalf("fuzzy: %+v", it)
	}
	press(h, key("esc"))
	if !h.paletteO || h.palette.picker.filter.Value() != "" {
		t.Fatal("esc clears the filter first")
	}
	press(h, key("esc"))
	if h.paletteO {
		t.Fatal("then esc closes the palette")
	}
}

func TestPaletteForm(t *testing.T) {
	useFakeTree(t)
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{}), 120, 40)
	press(h, ctrlK())
	press(h, typeText("recognize")...)
	press(h, key("enter"))
	v := h.View()
	for _, s := range []string{"file|url|dir|glob|-*", "--enterprise", "$ audd recognize '<file|url|dir|glob|->'"} {
		if !strings.Contains(v, s) {
			t.Fatalf("form lacks %q:\n%s", s, v)
		}
	}
	press(h, typeText("song.mp3")...)
	h.palette.cf.form.focusName("flag:enterprise")
	press(h, key(" "), key("down"))
	if help := h.palette.cf.form.current().help; !strings.Contains(help, "required with --enterprise") || !strings.Contains(help, "a number, or type none") {
		t.Fatalf("the limit's help: %q", help)
	}
	press(h, typeText("none")...)
	if c := h.command(); c != "audd recognize song.mp3 --enterprise --limit none" {
		t.Fatalf("command: %s", c)
	}
	snap(t, "palette_form", func() *home { return newTestHome(t, &fakeRun{}, homeOpts{}) },
		"Recognize music", ctrlK(), typeText("streams add"), key("enter"), "Add a stream to monitor")
}

// The palette runs the command without --yes; its confirmation is asked
// in the screen.
func TestPaletteRunsWithConfirmation(t *testing.T) {
	useFakeTree(t)
	f := &fakeRun{}
	f.on("streams remove 3", func(args []string, io RunIO) int {
		for _, a := range args {
			if a == "--yes" || a == "-y" {
				t.Errorf("the palette added %s", a)
			}
		}
		if !io.Ask("Stop monitoring stream 3?") {
			io.Stderr.Write([]byte(`{"error":{"code":"declined","message":"cancelled"}}` + "\n"))
			return 6
		}
		io.Stdout.Write([]byte(`{"schema_version":1,"radio_id":3,"removed":true}`))
		return 0
	})
	h := newTestHome(t, f, homeOpts{start: "recognize"})
	v := drive(t, h, 120, 40, "Recognize music", ctrlK(), typeText("streams remove"), key("enter"), "Stop monitoring a stream",
		typeText("3"), key("enter"), "Stop monitoring stream 3?", key("y"), "removed")
	if !strings.Contains(v, "radio_id: 3") || !strings.Contains(v, "$ audd streams remove 3") {
		t.Fatalf("result:\n%s", v)
	}
}

func TestPaletteRecognitionAndInteractive(t *testing.T) {
	useFakeTree(t)
	f := &fakeRun{}
	f.reply("recognize song.mp3", warriorsDoc, "", 0)
	h := newTestHome(t, f, homeOpts{start: "recognize"})
	v := drive(t, h, 120, 40, "Recognize music", ctrlK(), typeText("recognize"), key("enter"), "scan the whole file",
		typeText("song.mp3"), keys("up"), key("enter"), "USUM71409990")
	if !strings.Contains(v, "Imagine Dragons") {
		t.Fatalf("card:\n%s", v)
	}
	h = sized(newTestHome(t, f, homeOpts{}), 120, 40)
	press(h, ctrlK())
	press(h, typeText("now-playing")...)
	press(h, key("enter"))
	if h.paletteO || h.activeID() != "now-playing" {
		t.Fatalf("now-playing opens its section: palette %v, %s", h.paletteO, h.activeID())
	}
}

// A hint such as "audd streams add <url> --id 1" opens that form with
// what it names filled in.
func TestPaletteOpensHint(t *testing.T) {
	useFakeTree(t)
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{}), 120, 40)
	drain(h, h.openPaletteLine("audd streams add <url> --id 1"), 0)
	if c := h.command(); c != "audd streams add '<url>' --id 1" {
		t.Fatalf("command: %s", c)
	}
}

func TestFormProblems(t *testing.T) {
	root := fakeTree()
	if p := FormProblems(root); len(p) != 0 {
		t.Fatalf("problems: %q", p)
	}
	odd := &cobra.Command{Use: "odd", RunE: func(*cobra.Command, []string) error { return nil }}
	odd.Flags().IP("ip", nil, "an address")
	root.AddCommand(odd)
	if p := FormProblems(root); len(p) != 1 || !strings.Contains(p[0], "--ip") {
		t.Fatalf("an unknown flag type is reported: %q", p)
	}
	bad := &cobra.Command{Use: "bad {x}", RunE: func(*cobra.Command, []string) error { return nil }}
	root.AddCommand(bad)
	if p := FormProblems(root); len(p) != 2 {
		t.Fatalf("an unreadable argument is reported: %q", p)
	}
}

func TestHelpPages(t *testing.T) {
	useFakeTree(t)
	for i, name := range []string{"started", "guide", "commands", "keys", "exits", "links"} {
		steps := []any{"Getting started"}
		for range i {
			steps = append(steps, key("]"))
		}
		steps = append(steps, []string{"Getting started", "AUDD CLI", "streams add", "Everywhere", "Exit codes", "Report a problem"}[i])
		snap(t, "help_"+name, func() *home { return newTestHome(t, &fakeRun{}, homeOpts{start: "help"}) }, steps...)
	}
}

func TestHelpGuideSearch(t *testing.T) {
	useFakeTree(t)
	h := newTestHome(t, &fakeRun{}, homeOpts{start: "help"})
	v := drive(t, h, 120, 40, "Getting started", key("]"), "AUDD CLI", key("/"), typeText("exit codes"), key("enter"), `"exit codes"`)
	s := h.subs["help"].(*helpSection)
	if s.matchN <= 0 || !strings.Contains(strings.ToLower(s.guide[s.scroll.off]), "exit codes") {
		t.Fatalf("search did not scroll to a match (line %d):\n%s", s.scroll.off, v)
	}
}

func TestHelpGettingStartedJumps(t *testing.T) {
	useFakeTree(t)
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "help"}), 120, 40)
	drain(h, h.activate(), 0)
	press(h, key("down"), key("enter"))
	if h.activeID() != "recognize" || h.command() != "audd recognize https://audd.tech/example.mp3" {
		t.Fatalf("step 2: %s %s", h.activeID(), h.command())
	}
	h.show("help", true)
	press(h, key("down"), key("down"), key("enter"))
	if h.activeID() != "streams" || !h.subs["streams"].(*streamsSection).adding {
		t.Fatalf("step 4 opens the add form: %s", h.activeID())
	}
	_ = testutil.Golden
}

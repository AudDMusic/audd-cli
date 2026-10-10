// Package cli defines the audd command tree.
//
// Each command group lives in its own file and adds itself with Register
// from an init() function, so files never need to edit root.go:
//
//	func init() {
//		Register(func(root *cobra.Command, a *app.App) {
//			root.AddCommand(newStreamsCmd(a))
//		})
//	}
//
// Register callbacks run when the tree is built, before flags are parsed.
// The *app.App they receive is filled in (config, secrets, printer, flags)
// by the root's PersistentPreRunE before any RunE runs, so read its fields
// inside RunE, not in the callback.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
	"github.com/AudDMusic/audd-cli/internal/update"
)

// Command groups shown in `audd --help`. Set cmd.GroupID to one of these.
const (
	GroupRecognize = "recognize"
	GroupStreams   = "streams"
	GroupAccount   = "account"
	GroupLocal     = "local"
	GroupAgents    = "agents"
	GroupOther     = "other"
)

// AnnotationStreaming marks a command whose piped output defaults to JSONL
// (cmd.Annotations[AnnotationStreaming] = "true"). Commands that only
// sometimes stream call a.Out.SetStreaming() at run time instead.
const AnnotationStreaming = "audd:streaming"

// annotationNoConfig marks a command that still runs, with default settings,
// when the config file cannot be read (version, completion).
const annotationNoConfig = "audd:no-config"

var (
	regMu      sync.Mutex
	registered []func(root *cobra.Command, a *app.App)
)

// Register adds a function that attaches commands to the root. Call it from
// init().
func Register(f func(root *cobra.Command, a *app.App)) {
	regMu.Lock()
	defer regMu.Unlock()
	registered = append(registered, f)
}

// IO is the process environment a run uses.
type IO struct {
	In                             io.Reader
	Out, Err                       io.Writer
	StdinTTY, StdoutTTY, StderrTTY bool
	// StderrWidth is the terminal width of Err; 0 reads it from the terminal.
	StderrWidth int
	// StdoutWidth is the terminal width of Out; 0 reads it from the terminal.
	StdoutWidth int
	// Ask, when set, answers confirmations (interactive mode); see
	// output.PrinterOptions.Ask.
	Ask func(question string) bool
	// NoUpdateNotice skips the release check (runs inside interactive mode).
	NoUpdateNotice bool
}

// Execute runs audd with os.Args and the real terminal; it returns the exit code.
func Execute() int {
	// SIGHUP comes when the terminal goes away (a lost SSH session): stop
	// the way Ctrl-C does, so requests in progress finish and are saved.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if runtime.GOOS == "windows" {
		// An update on Windows renames the running binary to audd.exe.old;
		// the next run removes it.
		if exe, err := executablePath(); err == nil {
			update.RemoveOld(exe)
		}
	}
	return Run(ctx, os.Args[1:], IO{
		In: os.Stdin, Out: os.Stdout, Err: os.Stderr,
		StdinTTY:  isTerminal(os.Stdin),
		StdoutTTY: isTerminal(os.Stdout),
		StderrTTY: isTerminal(os.Stderr),
	})
}

func isTerminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// Main runs audd with the given arguments and streams, none of them a TTY.
// Tests use it (see testutil.Exec).
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return Run(context.Background(), args, IO{In: stdin, Out: stdout, Err: stderr})
}

// Run builds a fresh command tree and runs it; it returns the exit code.
func Run(ctx context.Context, args []string, stdio IO) int {
	if stdio.In == nil {
		stdio.In = strings.NewReader("")
	}
	a := app.New()
	a.In = stdio.In
	// A fallback printer, replaced once flags are parsed, so early errors print too.
	a.Out = output.NewPrinter(stdio.Out, stdio.Err, output.PrinterOptions{
		StdoutTTY: stdio.StdoutTTY, StderrTTY: stdio.StderrTTY, StdinTTY: stdio.StdinTTY,
		Stdin: stdio.In, NoColor: true, StderrWidth: stdio.StderrWidth, StdoutWidth: stdio.StdoutWidth,
		Ask: stdio.Ask,
	})
	st := &runState{}
	root := newRoot(a, stdio, st)
	root.SetArgs(args)
	root.SetIn(stdio.In)
	root.SetOut(stdio.Out)
	root.SetErr(stdio.Err)
	if opensHome(root, args, stdio) {
		args = append([]string{"ui"}, args...)
		root.SetArgs(args)
	}
	if err := unknownSubcommand(root, args); err != nil {
		return a.Out.Error(err)
	}
	if !stdio.NoUpdateNotice {
		notice := startUpdateNotice(root, args, stdio)
		defer notice()
	}
	err := root.ExecuteContext(ctx)
	if err == nil {
		return output.ExitOK
	}
	var oe *output.Error
	if !errors.As(err, &oe) {
		if st.running {
			err = output.AsError(err)
		} else {
			// Errors raised by cobra before RunE (unknown command, wrong
			// argument count, missing required flag) are usage errors.
			err = cobraUsageError(root, args, err)
		}
	}
	return a.Out.Error(err)
}

type runState struct {
	running bool
}

func newRoot(a *app.App, stdio IO, st *runState) *cobra.Command {
	cobra.EnableTraverseRunHooks = true
	// Commands are listed in the order they are added (most used first),
	// not alphabetically; see orderCommands for the top level.
	cobra.EnableCommandSorting = false
	root := &cobra.Command{
		Use:   "audd",
		Short: "Recognize music and manage AudD from the command line",
		Long: `audd recognizes music in files, URLs, folders, long recordings, and from the
microphone; monitors streams; and manages your AudD account.

Output is a readable table on a terminal and JSON when piped. Commands that
can spend many requests ask for explicit limits (--limit, --max-files) and
show a plan before they start; --dry-run shows the plan only.

Get your API token at https://dashboard.audd.io, or run audd login.

Run audd with no arguments (or audd ui) on a terminal for interactive mode.`,
		Example: `  audd ui
  audd recognize song.mp3
  audd recognize https://audd.tech/example.mp3 --return apple_music,spotify
  audd recognize ./recordings --max-files 200 --dry-run
  audd recognize mix.mp3 --enterprise --limit 20 --tracklist
  audd listen
  audd streams list
  audd now-playing
  audd login`,
		Version:           app.Version,
		SilenceUsage:      true,
		SilenceErrors:     true,
		DisableAutoGenTag: true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	root.SetVersionTemplate(versionLine() + "\n")
	// Declared so cobra does not add a -v shorthand (recognize uses -v).
	root.Flags().Bool("version", false, "show the audd version")

	f := root.PersistentFlags()
	f.StringVar(&a.Flags.Token, "token", "", "AudD API token (default: AUDD_API_TOKEN, then audd config, then audd login)")
	f.StringVar(&a.Flags.Profile, "profile", "", "profile to use (default: AUDD_PROFILE or the active profile)")
	f.StringVar(&a.Flags.Format, "format", "", "output format: table, json, jsonl, or csv (default: table on a terminal, JSON when piped)")
	f.StringSliceVar(&a.Flags.Fields, "fields", nil, "only output these fields (comma-separated, dotted for nested: result.artist)")
	f.BoolVarP(&a.Flags.Yes, "yes", "y", false, "answer yes to confirmations (required for billable runs without a terminal)")
	f.BoolVarP(&a.Flags.Quiet, "quiet", "q", false, "hide notes and progress; in table output, print only the essential line")
	f.IntVar(&a.Flags.MaxRequests, "max-requests", 0, "stop before spending more than N API requests (0: use the config default)")
	f.BoolVar(&a.Flags.Debug, "debug", false, "log HTTP requests to stderr (tokens redacted)")
	f.BoolVar(&a.Flags.NoColor, "no-color", false, "disable colors")

	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return output.Errf(output.ExitUsage, "invalid_argument", c.CommandPath()+" --help", "%s", flagErrorMessage(err.Error()))
	})

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		return setup(a, cmd, stdio)
	}

	root.AddCommand(newVersionCmd(a), newCompletionCmd(a), newConfigCmd(a))

	regMu.Lock()
	regs := append([]func(*cobra.Command, *app.App){}, registered...)
	regMu.Unlock()
	for _, r := range regs {
		r(root, a)
	}
	orderCommands(root)
	addGroups(root)
	wrapRunE(root, st)
	return root
}

// commandOrder is the order of the top-level commands in `audd --help`
// within each group. Commands not listed follow, by name.
var commandOrder = []string{
	"recognize", "listen", "catalog",
	"streams", "now-playing",
	"login", "account", "usage", "billing", "token", "auth",
	"browse", "jobs", "cache", "config",
	"api", "commands", "docs", "agent-setup", "mcp",
	"ui", "version", "update", "completion", "help",
}

func orderCommands(root *cobra.Command) {
	rank := map[string]int{}
	for i, n := range commandOrder {
		rank[n] = i + 1
	}
	cmds := append([]*cobra.Command(nil), root.Commands()...)
	sort.SliceStable(cmds, func(i, j int) bool {
		ri, rj := rank[cmds[i].Name()], rank[cmds[j].Name()]
		switch {
		case ri != 0 && rj != 0:
			return ri < rj
		case ri != 0 || rj != 0:
			return ri != 0
		}
		return cmds[i].Name() < cmds[j].Name()
	})
	root.RemoveCommand(cmds...)
	root.AddCommand(cmds...)
}

var groups = []cobra.Group{
	{ID: GroupRecognize, Title: "Recognition:"},
	{ID: GroupStreams, Title: "Streams:"},
	{ID: GroupAccount, Title: "Account:"},
	{ID: GroupLocal, Title: "History, jobs, and settings:"},
	{ID: GroupAgents, Title: "Scripts and agents:"},
	{ID: GroupOther, Title: "Other:"},
}

// addGroups declares the help groups that have commands, in a fixed order.
func addGroups(root *cobra.Command) {
	used := map[string]bool{GroupOther: true}
	for _, c := range root.Commands() {
		used[c.GroupID] = true
	}
	for _, g := range groups {
		if used[g.ID] {
			g := g
			root.AddGroup(&g)
		}
	}
	root.SetHelpCommandGroupID(GroupOther)
}

// wrapRunE records that a command's own code started running, so errors
// from cobra's argument checks can be told apart from command failures.
func wrapRunE(c *cobra.Command, st *runState) {
	if c.RunE != nil {
		orig := c.RunE
		c.RunE = func(cmd *cobra.Command, args []string) error {
			st.running = true
			return orig(cmd, args)
		}
	} else if c.Run != nil {
		orig := c.Run
		c.Run = nil
		c.RunE = func(cmd *cobra.Command, args []string) error {
			st.running = true
			orig(cmd, args)
			return nil
		}
	}
	for _, sub := range c.Commands() {
		wrapRunE(sub, st)
	}
}

// setup fills in the App from config, environment, and flags.
func setup(a *app.App, cmd *cobra.Command, stdio IO) error {
	cfg, err := config.Load()
	if err != nil {
		if cmd.Annotations[annotationNoConfig] != "true" {
			// A config file that does not parse is an invalid_config usage
			// error; anything else (such as a permission error) is unexpected.
			return output.AsError(err)
		}
		cfg = config.Defaults(filepath.Join(config.Dir(), "config.toml"))
	}
	a.Cfg = cfg
	// The profile name is part of file names (the stream store, the
	// recorder's lock and log), so it must stay one plain name.
	name := config.ProfileName(a.Flags.Profile, cfg)
	if name == "" || config.FileSafe(name) != name {
		return output.Errf(output.ExitUsage, "invalid_argument", "audd auth switch default",
			"profile names use letters, digits, - and _, got %q", name)
	}
	a.Profile = cfg.Profile(name)
	if a.Secrets == nil {
		a.Secrets = secrets.Default()
	}

	format := a.Flags.Format
	if format == "" {
		format = os.Getenv("AUDD_FORMAT")
	}
	if format == "" {
		format = a.Profile.Format
	}
	var f output.Format
	if format != "" {
		if f, err = output.ParseFormat(format); err != nil {
			return err
		}
	}
	if a.Flags.MaxRequests < 0 {
		return output.Errf(output.ExitUsage, "invalid_argument", "--max-requests 500", "--max-requests must be 0 or more")
	}
	if !cmd.Flags().Changed("max-requests") && a.Profile.MaxRequests > 0 {
		a.Flags.MaxRequests = a.Profile.MaxRequests
		a.Flags.MaxRequestsFromConfig = true
	}
	a.Out = output.NewPrinter(stdio.Out, stdio.Err, output.PrinterOptions{
		Format:      f,
		Fields:      a.Flags.Fields,
		Quiet:       a.Flags.Quiet,
		NoColor:     output.ResolveNoColor(a.Flags.NoColor, os.Getenv, stdio.StdoutTTY),
		StdoutTTY:   stdio.StdoutTTY,
		StderrTTY:   stdio.StderrTTY,
		StdinTTY:    stdio.StdinTTY,
		Stdin:       stdio.In,
		StderrWidth: stdio.StderrWidth,
		StdoutWidth: stdio.StdoutWidth,
		Ask:         stdio.Ask,
	})
	if cmd.Annotations[AnnotationStreaming] == "true" {
		a.Out.SetStreaming()
	}
	return nil
}

// unknownSubcommand reports a mistyped subcommand of a command group (audd
// streams histroy) as a usage error. Cobra would print the group's help and
// exit 0. A group run with no arguments, or with only flags, still prints
// its help.
var (
	unknownCmdRe = regexp.MustCompile(`^unknown command "([^"]*)" for "([^"]*)"`)
	requiredRe   = regexp.MustCompile(`^required flag\(s\) (.+) not set$`)
	badValueRe   = regexp.MustCompile(`^invalid argument "(.*)" for "(?:-[^,"]*, )?(--?[^"]+)" flag: (.*)$`)
	argCountRe   = regexp.MustCompile(`^(?:requires at least (\d+) arg\(s\), only received (\d+)|accepts at most (\d+) arg\(s\), received (\d+)|accepts (\d+) arg\(s\), received (\d+)|accepts between (\d+) and (\d+) arg\(s\), received (\d+))$`)
)

// cobraUsageError turns an error cobra raised before a command ran (an
// unknown command, a wrong number of arguments, a missing required flag)
// into a one-line usage error whose hint is the next thing to run.
func cobraUsageError(root *cobra.Command, args []string, err error) error {
	msg := strings.TrimSpace(err.Error())
	c, _, ferr := root.Find(args)
	if ferr != nil || c == nil {
		c = root
	}
	hint := c.CommandPath() + " --help"
	if m := unknownCmdRe.FindStringSubmatch(msg); m != nil {
		if root.SuggestionsMinimumDistance <= 0 {
			root.SuggestionsMinimumDistance = 2
		}
		hint = "audd --help"
		if s := root.SuggestionsFor(m[1]); len(s) > 0 {
			hint = "audd " + s[0]
		}
		return output.Errf(output.ExitUsage, "invalid_argument", hint, "unknown command %q for %q", m[1], m[2])
	}
	if m := requiredRe.FindStringSubmatch(msg); m != nil {
		var need []string
		usage := ""
		for _, q := range strings.Split(m[1], ", ") {
			name := strings.Trim(q, `"`)
			s := "--" + name
			usage = ""
			if f := c.Flags().Lookup(name); f != nil {
				switch f.Value.Type() {
				case "int", "int64", "uint", "float64":
					s += " N"
				case "string":
					s += " VALUE"
				}
				usage = strings.TrimSuffix(f.Usage, " (required)")
			}
			need = append(need, s)
		}
		msg := fmt.Sprintf("%s needs %s", c.CommandPath(), strings.Join(need, " and "))
		if len(need) == 1 && usage != "" {
			msg += ": " + usage
		}
		return output.Errf(output.ExitUsage, "invalid_argument", hint, "%s", msg)
	}
	if m := argCountRe.FindStringSubmatch(msg); m != nil {
		atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
		usage := strings.TrimSuffix(c.UseLine(), " [flags]")
		var want string
		var got int
		switch {
		case m[1] != "":
			want, got = "at least "+output.Plural(atoi(m[1]), "argument"), atoi(m[2])
		case m[3] != "":
			want, got = "at most "+output.Plural(atoi(m[3]), "argument"), atoi(m[4])
		case m[5] != "":
			want, got = output.Plural(atoi(m[5]), "argument"), atoi(m[6])
		default:
			want, got = "between "+m[7]+" and "+output.Plural(atoi(m[8]), "argument"), atoi(m[9])
		}
		return output.Errf(output.ExitUsage, "invalid_argument", hint,
			"%s takes %s and got %d (usage: %s)", c.CommandPath(), want, got, usage)
	}
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	return output.Errf(output.ExitUsage, "invalid_argument", hint, "%s", msg)
}

// flagErrorMessage rewrites pflag's message for a flag value it could not
// parse: "--id must be a whole number, got \"abc\"".
func flagErrorMessage(msg string) string {
	m := badValueRe.FindStringSubmatch(msg)
	if m == nil {
		return msg
	}
	val, name, why := m[1], m[2], m[3]
	switch {
	case strings.Contains(why, "strconv.ParseInt"), strings.Contains(why, "strconv.ParseUint"), strings.Contains(why, "strconv.Atoi"):
		return fmt.Sprintf("%s must be a whole number, got %q", name, val)
	case strings.Contains(why, "strconv.ParseFloat"):
		return fmt.Sprintf("%s must be a number, got %q", name, val)
	case strings.Contains(why, "strconv.ParseBool"):
		return fmt.Sprintf("%s must be true or false, got %q", name, val)
	case strings.Contains(why, "time: "):
		return fmt.Sprintf("%s must be a duration such as 30s or 5m, got %q", name, val)
	}
	return fmt.Sprintf("invalid value %q for %s: %s", val, name, why)
}

func unknownSubcommand(root *cobra.Command, args []string) error {
	c, rest, err := root.Find(args)
	if err != nil || c == nil || c == root || c.Runnable() || !c.HasSubCommands() {
		return nil
	}
	name := firstPositional(c, rest)
	if name == "" || name == "help" {
		return nil
	}
	hint := c.CommandPath() + " --help"
	if c.SuggestionsMinimumDistance <= 0 {
		c.SuggestionsMinimumDistance = 2
	}
	if s := c.SuggestionsFor(name); len(s) > 0 {
		hint = c.CommandPath() + " " + s[0]
	}
	return output.Errf(output.ExitUsage, "invalid_argument", hint, "unknown command %q for %q", name, c.CommandPath())
}

// firstPositional returns the first argument that is not a flag or a
// flag's value. A help flag means help was asked for: it returns "".
func firstPositional(c *cobra.Command, args []string) string {
	lookup := func(name string, short bool) *pflag.Flag {
		for _, fs := range []*pflag.FlagSet{c.Flags(), c.InheritedFlags(), c.Root().PersistentFlags()} {
			var f *pflag.Flag
			if short {
				f = fs.ShorthandLookup(name)
			} else {
				f = fs.Lookup(name)
			}
			if f != nil {
				return f
			}
		}
		return nil
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--help" || arg == "-h" {
			return ""
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		case strings.HasPrefix(arg, "--"):
			if !strings.Contains(arg, "=") {
				if f := lookup(arg[2:], false); f != nil && f.NoOptDefVal == "" {
					i++ // the flag's value
				}
			}
		case strings.HasPrefix(arg, "-") && len(arg) > 1:
			if len(arg) == 2 {
				if f := lookup(arg[1:], true); f != nil && f.NoOptDefVal == "" {
					i++
				}
			}
		default:
			return arg
		}
	}
	return ""
}

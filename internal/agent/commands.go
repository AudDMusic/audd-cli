package agent

import (
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Reference is the output of `audd commands --json`: every command, its
// flags, what it prints, and the exit codes.
type Reference struct {
	Version     string            `json:"version"`
	GlobalFlags []Flag            `json:"global_flags"`
	Commands    []Command         `json:"commands"`
	ExitCodes   []ExitCode        `json:"exit_codes"`
	Outputs     map[string]Output `json:"outputs"`
}

// Command describes one command.
type Command struct {
	Path    string   `json:"path"` // "audd streams add"
	Aliases []string `json:"aliases,omitempty"`
	// AliasOf is the command this one is a shortcut for: audd logout is
	// audd auth logout.
	AliasOf string `json:"alias_of,omitempty"`
	Summary string `json:"summary"`
	Usage   string `json:"usage"`
	// Runnable is false for groups that only hold subcommands.
	Runnable  bool     `json:"runnable"`
	Flags     []Flag   `json:"flags,omitempty"`
	Examples  []string `json:"examples,omitempty"`
	Output    string   `json:"output,omitempty"` // a key of Reference.Outputs
	Streaming bool     `json:"streaming,omitempty"`
	ExitCodes []int    `json:"exit_codes,omitempty"`
}

// Flag describes one flag.
type Flag struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Type      string `json:"type"`
	Default   string `json:"default,omitempty"`
	Required  bool   `json:"required,omitempty"`
	// RequiredWhen names the case in which the flag is required, for flags
	// that are required only sometimes (for example --limit with --enterprise).
	RequiredWhen string `json:"required_when,omitempty"`
	Usage        string `json:"usage"`
}

// AnnotationRequiredWhen is the flag annotation that SetRequiredWhen sets.
const AnnotationRequiredWhen = "audd_required_when"

// SetRequiredWhen records in the reference when a conditionally required
// flag must be given. The command still checks it itself.
func SetRequiredWhen(fs *pflag.FlagSet, name, when string) {
	_ = fs.SetAnnotation(name, AnnotationRequiredWhen, []string{when})
}

// ExitCode is one exit code and its meaning.
type ExitCode struct {
	Code    int    `json:"code"`
	Name    string `json:"name"`
	Meaning string `json:"meaning"`
}

// Output describes what a command prints when piped or with --format json.
type Output struct {
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema,omitempty"`
}

// ExitCodes are the exit codes every command uses.
var ExitCodes = []ExitCode{
	{0, "ok", "success, including no match"},
	{1, "unexpected", "unexpected error, or no match with --fail-on-no-match"},
	{2, "usage", "invalid arguments or input, or a missing tool such as ffmpeg"},
	{3, "auth", "missing or rejected API token, or sign-in needed (audd login)"},
	{4, "quota", "quota, plan, or feature not available on the account"},
	{5, "network", "network or server error; retrying may help"},
	{6, "safety", "a bound is missing (--limit, --max-files), a confirmation is needed (--yes), --max-requests was reached, or an unfinished job for the same files needs --resume or --new"},
	{7, "partial", "a batch finished with some files failed"},
	{8, "threshold", "usage --check found fewer remaining requests than --min-remaining"},
	{130, "interrupted", "stopped with Ctrl-C; a batch keeps finished files (audd jobs resume continues it)"},
}

// Exit codes that apply to every command.
var baseExitCodes = []int{0, 1, 2}

// commandInfo is what the reference knows about a command beyond cobra.
type commandInfo struct {
	output string
	exits  []int
}

// Keyed by command path without the leading "audd ".
var commandInfos = map[string]commandInfo{
	"recognize":              {"recognition", []int{3, 4, 5, 6, 7, 130}},
	"listen":                 {"recognition", []int{3, 4, 5, 130}},
	"catalog add":            {"object", []int{3, 4, 5, 6}},
	"api":                    {"api_response", []int{3, 4, 5}},
	"streams list":           {"list", []int{3, 4, 5}},
	"streams add":            {"object", []int{3, 4, 5}},
	"streams remove":         {"object", []int{3, 4, 5, 6}},
	"streams set-url":        {"object", []int{3, 4, 5}},
	"streams callback":       {"object", []int{3, 4, 5}},
	"streams watch":          {"play_stream", []int{3, 4, 5, 6}},
	"streams record":         {"play_stream", []int{3, 4, 5, 6}},
	"streams recorder":       {"object", nil},
	"streams recorder start": {"object", []int{3, 4, 5, 6}},
	"streams history":        {"plays", []int{3, 5}},
	"streams report":         {"report", []int{3, 5}},
	"streams export":         {"play_stream", []int{3, 5}},
	"now-playing":            {"now_playing", []int{3, 5}},
	"auth":                   {"object", []int{3, 5}},
	"login":                  {"object", []int{3, 5}},
	"account":                {"object", []int{3, 5}},
	"usage":                  {"object", []int{3, 5, 8}},
	"billing":                {"object", []int{3, 4, 5}},
	"token":                  {"object", []int{3, 5, 6}},
	"jobs":                   {"object", nil},
	"jobs resume":            {"recognition", []int{3, 4, 5, 6, 7, 130}},
	"docs":                   {"docs", []int{5}},
	"mcp":                    {"", []int{3}},
	"commands":               {"commands", nil},
	"update":                 {"object", []int{5}},
	"browse":                 {"object", nil},
	"cache":                  {"object", nil},
	"config":                 {"object", nil},
	"version":                {"object", nil},
	"agent-setup":            {"object", nil},
	"completion":             {"", nil},
	"ui":                     {"", nil},
	"streams callback get":   {"object", []int{3, 4, 5}},
}

// Outputs describes the JSON shapes commands print.
var Outputs = map[string]Output{
	"object": {Description: `one JSON document: {"schema_version":1, ...fields}`},
	"list":   {Description: `one JSON document with the rows under "items": {"schema_version":1,"items":[...]}`},
	"recognition": {
		Description: `One file or URL: {"schema_version":1,"input","cached","result"} where result is AudD's result object or null for no match; with --enterprise, "matches" (and "tracks" with --tracklist) instead of "result"; with --dry-run (one input or a batch), {"dry_run":true,"input":name|null,"job_id":id|null,"endpoint":"standard"|"enterprise","plan":{"files","cached_files","requests","approximate","cost_usd"}}: input is set for one file or URL, job_id when a batch would resume a job; requests and cost_usd are null with "unbounded":true when enterprise URLs with --limit none (or a dry run without --limit) leave the scan uncapped; with --format jsonl the plan is a "type":"event" line with "event":"dry_run". Folders, globs, lists, and jobs resume print JSON lines: "result" per file, "progress", and a final "summary" whose counts are for the whole job (as in audd jobs list), with "requests_spent_this_run" for this run.`,
		Schema: obj(map[string]any{
			"schema_version": typ("integer"),
			"input":          typ("string"),
			"cached":         typ("boolean"),
			"result":         map[string]any{"type": []string{"object", "null"}, "description": "AudD's result: artist, title, album, release_date, label, timecode, song_link, plus any --return blocks"},
			"matches":        map[string]any{"type": "array", "description": "enterprise matches, each with start and end positions"},
			"tracks":         map[string]any{"type": "array", "description": "with --tracklist: merged tracks with start_seconds and end_seconds"},
		}),
	},
	"api_response": {Description: `{"schema_version":1, ...the fields of the API's JSON response}: the response as AudD sent it, with "schema_version" added`},
	"plays": {
		Description: `{"schema_version":1,"since","plays":[play...],"gaps":[{"from","to"}...]}: plays newest first; gaps are periods some stream was not recorded, so totals may miss plays there`,
		Schema: obj(map[string]any{
			"plays": map[string]any{"type": "array", "items": playSchema},
			"gaps":  map[string]any{"type": "array", "items": gapSchema},
		}),
	},
	"play_stream": {
		Description: `JSON lines: {"schema_version":1,"type":"result", ...play} per play, and "event" lines for stream health`,
		Schema:      playSchema,
	},
	"now_playing": {
		Description: `With --once, one document with the latest song of each stream ({"stations":[{"radio_id","url","stream_running","health","now_playing":{...play,"state","playing","elapsed_seconds","played_seconds","ended_at","ago_seconds","length_seconds"}}]}); otherwise JSON lines, a "result" line with the same fields each time the song changes. state is just_played or last_recognized for results sent when a song ends (the default), playing for streams added with --start; elapsed_seconds only while playing, played_seconds and ended_at only for end-of-song results, length_seconds only with provider metadata`,
	},
	"report": {
		Description: `{"schema_version":1,"by","since","rows":[{"key","plays","airtime_seconds","stations"}...],"gaps","complete"}; complete is false when gaps mean totals miss plays`,
	},
	"docs":     {Description: `The page's markdown as is, piped or not; with --format json, {"schema_version":1,"topic","source","markdown"}. Without a topic, the list of topics`},
	"commands": {Description: "this reference"},
	"error": {
		Description: "printed to stderr when a command fails in a machine format",
		Schema: obj(map[string]any{
			"schema_version": typ("integer"),
			"error": obj(map[string]any{
				"code":      typ("string"),
				"api_code":  typ("integer"),
				"message":   typ("string"),
				"hint":      map[string]any{"type": "string", "description": "usually the command that fixes it"},
				"retryable": typ("boolean"),
			}),
		}),
	},
}

var playSchema = obj(map[string]any{
	"radio_id":     typ("integer"),
	"timestamp":    map[string]any{"type": "string", "format": "date-time"},
	"play_length":  map[string]any{"type": "integer", "description": "seconds; 0 when unknown"},
	"artist":       typ("string"),
	"title":        typ("string"),
	"album":        typ("string"),
	"label":        typ("string"),
	"release_date": typ("string"),
	"isrc":         typ("string"),
	"upc":          typ("string"),
	"song_link":    typ("string"),
	"score":        typ("integer"),
})

var gapSchema = obj(map[string]any{
	"from": map[string]any{"type": "string", "format": "date-time"},
	"to":   map[string]any{"type": "string", "format": "date-time"},
})

func obj(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props}
}

func typ(t string) map[string]any { return map[string]any{"type": t} }

// AnnotationStreaming is the cobra annotation for commands whose piped
// output is JSON lines (the same key internal/cli uses).
const AnnotationStreaming = "audd:streaming"

// AnnotationAliasOf is the cobra annotation on a top-level shortcut (audd
// logout) that names the command it runs ("auth logout"). Shortcuts are
// listed even when hidden from help.
const AnnotationAliasOf = "audd:alias_of"

// BuildReference walks the command tree. Hidden commands and flags are
// left out, except shortcuts marked with AnnotationAliasOf; commands are in
// tree order, sorted by name.
func BuildReference(root *cobra.Command, version string) Reference {
	ref := Reference{Version: version, ExitCodes: ExitCodes, Outputs: Outputs}
	ref.GlobalFlags = flags(root.PersistentFlags())
	ref.Commands = []Command{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		subs := append([]*cobra.Command(nil), c.Commands()...)
		sort.Slice(subs, func(i, j int) bool { return subs[i].Name() < subs[j].Name() })
		for _, sub := range subs {
			alias := sub.Annotations[AnnotationAliasOf] != ""
			if (sub.Hidden && !alias) || (!sub.IsAvailableCommand() && !alias) || sub.Name() == "help" {
				continue
			}
			ref.Commands = append(ref.Commands, describe(sub))
			walk(sub)
		}
	}
	walk(root)
	return ref
}

func describe(c *cobra.Command) Command {
	path := c.CommandPath()
	key := strings.TrimPrefix(path, c.Root().Name()+" ")
	var aliasOf string
	if target := c.Annotations[AnnotationAliasOf]; target != "" {
		aliasOf = c.Root().Name() + " " + target
		key = target
	}
	cmd := Command{
		Path:      path,
		AliasOf:   aliasOf,
		Aliases:   c.Aliases,
		Summary:   c.Short,
		Usage:     c.UseLine(),
		Runnable:  c.Runnable(),
		Flags:     flags(c.LocalNonPersistentFlags()),
		Streaming: c.Annotations[AnnotationStreaming] == "true",
	}
	if c.Example != "" {
		for _, l := range strings.Split(c.Example, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				cmd.Examples = append(cmd.Examples, l)
			}
		}
	}
	// The most specific entry wins: "streams callback get", then
	// "streams callback", then "streams".
	info, found := commandInfo{}, false
	for k := key; k != ""; {
		if i, ok := commandInfos[k]; ok {
			info, found = i, true
			break
		}
		j := strings.LastIndex(k, " ")
		if j < 0 {
			break
		}
		k = k[:j]
	}
	if cmd.Runnable {
		cmd.Output = info.output
		if !found {
			cmd.Output = "object"
		}
		codes := append(append([]int(nil), baseExitCodes...), info.exits...)
		sort.Ints(codes)
		cmd.ExitCodes = codes
	}
	return cmd
}

func flags(fs *pflag.FlagSet) []Flag {
	out := []Flag{}
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" || f.Name == "version" {
			return
		}
		fl := Flag{Name: f.Name, Shorthand: f.Shorthand, Type: f.Value.Type(), Usage: f.Usage}
		if f.DefValue != "" && f.DefValue != "[]" && !(f.Value.Type() == "bool" && f.DefValue == "false") {
			fl.Default = f.DefValue
		}
		if r, ok := f.Annotations[cobra.BashCompOneRequiredFlag]; ok && len(r) > 0 && r[0] == "true" {
			fl.Required = true
		}
		if w := f.Annotations[AnnotationRequiredWhen]; len(w) > 0 {
			fl.RequiredWhen = w[0]
		}
		out = append(out, fl)
	})
	return out
}

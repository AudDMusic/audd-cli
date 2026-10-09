package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Providers are the valid values of recognize's "return".
var Providers = []string{"apple_music", "spotify", "deezer", "musicbrainz"}

type source struct {
	Path string `json:"path,omitempty" jsonschema:"a local audio or video file (give path or url)"`
	URL  string `json:"url,omitempty" jsonschema:"an http or https URL of an audio or video file (give path or url)"`
}

type recognizeIn struct {
	source
	Return   []string `json:"return,omitempty" jsonschema:"extra metadata to include: apple_music, spotify, deezer, musicbrainz"`
	At       string   `json:"at,omitempty" jsonschema:"send a clip starting here instead of the first 12 seconds, e.g. 90, 1:30 or 1m30s (needs ffmpeg)"`
	Duration string   `json:"duration,omitempty" jsonschema:"clip length with at (default 12s)"`
}

type enterpriseIn struct {
	source
	Limit            *int `json:"limit,omitempty" jsonschema:"the most 12-second chunks to scan; each chunk is billed as one request. Required unless dry_run is true"`
	Tracklist        bool `json:"tracklist,omitempty" jsonschema:"merge consecutive matches into tracks with start and end times"`
	DryRun           bool `json:"dry_run,omitempty" jsonschema:"only return the plan with the estimated requests; nothing is sent"`
	Skip             *int `json:"skip,omitempty" jsonschema:"chunks to skip after each scanned run"`
	Every            *int `json:"every,omitempty" jsonschema:"chunks to scan in a row"`
	SkipFirstSeconds *int `json:"skip_first_seconds,omitempty" jsonschema:"seconds to skip at the start of the file"`
}

type radioIn struct {
	RadioID int `json:"radio_id" jsonschema:"the stream's radio ID"`
}

type streamsAddIn struct {
	URL     string `json:"url" jsonschema:"the stream URL (an audio stream, HLS playlist, or a page AudD supports)"`
	RadioID int    `json:"radio_id" jsonschema:"a number you choose to identify the stream"`
	Start   bool   `json:"start,omitempty" jsonschema:"send each result when a song starts instead of when it ends"`
}

type historyIn struct {
	RadioID *int   `json:"radio_id,omitempty" jsonschema:"only this stream"`
	Since   string `json:"since,omitempty" jsonschema:"how far back: 24h, 7d, 2w, a date, or all (default 7d)"`
	Limit   *int   `json:"limit,omitempty" jsonschema:"at most this many plays (default 50; 0 for all)"`
}

type nowPlayingIn struct {
	RadioIDs []int `json:"radio_ids,omitempty" jsonschema:"only these streams (default: all)"`
}

type catalogIn struct {
	source
	AudioID *int `json:"audio_id,omitempty" jsonschema:"required: the audio_id to store the song under; a song already there is replaced"`
}

type usageIn struct {
	Days *int `json:"days,omitempty" jsonschema:"days of per-day history (default 30)"`
}

type docsIn struct {
	Topic string `json:"topic,omitempty" jsonschema:"api, enterprise, streams, upload, mcp, sdks, or cli; empty lists the topics"`
}

type paymentIn struct {
	Action   string `json:"action" jsonschema:"subscribe, renew, or buy"`
	Plan     string `json:"plan,omitempty" jsonschema:"with subscribe: the plan key from billing_plans (its plan field, such as startup_plan)"`
	Requests int    `json:"requests,omitempty" jsonschema:"with buy: extra requests, a multiple of 1000"`
}

type noArgs struct{}

func (t *tools) register(s *mcp.Server) {
	openWorld := boolPtr(true)
	readOnly := func(title string) *mcp.ToolAnnotations {
		return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: openWorld}
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "recognize",
		Description: "Identify the song in one audio or video file or URL. The standard endpoint analyzes up to the first 12 seconds of audio; pass at for another moment. Costs one request unless the result is cached. Returns {input, cached, result}; result is null when nothing matched.",
		Annotations: &mcp.ToolAnnotations{Title: "Recognize a song", DestructiveHint: boolPtr(false), OpenWorldHint: openWorld},
	}, t.recognize)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "recognize_enterprise",
		Description: "Find every song in a long recording (a mix, show, or video) with each match's position. Billed per 12-second chunk, so limit (the most chunks to scan) is required for a real run. Call with dry_run true first (limit optional) to see the estimated requests; a URL's length is unknown, so without a limit its plan has requests null and unbounded true.",
		Annotations: &mcp.ToolAnnotations{Title: "Scan a whole recording", DestructiveHint: boolPtr(false), OpenWorldHint: openWorld},
	}, t.enterprise)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "streams_list",
		Description: "List the streams monitored on the AudD account, with what played last on each.",
		Annotations: readOnly("List streams"),
	}, t.simple("streams", "list"))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "streams_add",
		Description: "Start monitoring a stream under a radio ID you choose. Needs stream monitoring on the account.",
		Annotations: &mcp.ToolAnnotations{Title: "Add a stream", DestructiveHint: boolPtr(false), OpenWorldHint: openWorld},
	}, t.streamsAdd)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "streams_remove",
		Description: "Stop monitoring a stream. Its recorded plays stay in the local history.",
		Annotations: &mcp.ToolAnnotations{Title: "Remove a stream", DestructiveHint: boolPtr(true), IdempotentHint: true, OpenWorldHint: openWorld},
	}, t.streamsRemove)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "streams_history",
		Description: "Recorded plays on the account's streams, newest first, from the local stream store that the background recorder keeps complete. gaps lists periods some stream was not recorded.",
		Annotations: readOnly("Stream history"),
	}, t.history)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "now_playing",
		Description: "The latest song on each stream (or the given ones). state is just_played or last_recognized for results sent when a song ends (the default; with played_seconds and ended_at), playing for streams added with --start (with elapsed_seconds), and length_seconds appears only when provider metadata is on for stream results.",
		Annotations: readOnly("Now playing"),
	}, t.nowPlaying)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "catalog_add",
		Description: "Fingerprint a song into the account's custom catalog under audio_id. Billed, sent exactly once, and never retried automatically; a song already under that audio_id is replaced. Custom catalog access is enabled per account.",
		Annotations: &mcp.ToolAnnotations{Title: "Add to custom catalog", DestructiveHint: boolPtr(true), OpenWorldHint: openWorld},
	}, t.catalogAdd)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "usage",
		Description: "Requests used this billing cycle, the allowance, what remains, and requests per day. Needs audd login.",
		Annotations: readOnly("Usage"),
	}, t.usage)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "account_status",
		Description: "The signed-in AudD account: email, plan, subscription status, paid-until date, and bonus requests. Needs audd login.",
		Annotations: readOnly("Account"),
	}, t.simple("account"))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "docs",
		Description: "AudD documentation as markdown: api, enterprise, streams, upload, mcp, sdks, or cli (this CLI).",
		Annotations: readOnly("Documentation"),
	}, t.docs)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "billing_plans",
		Description: "The plans the account can subscribe to, with prices and included requests. Needs audd login.",
		Annotations: readOnly("Plans"),
	}, t.simple("billing", "plans"))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "payment_link",
		Description: "Get a Stripe payment link to subscribe to a plan, renew, or buy extra requests. Nothing is charged: give the link to the user to open and approve. Needs audd login.",
		Annotations: &mcp.ToolAnnotations{Title: "Payment link", ReadOnlyHint: true, OpenWorldHint: openWorld},
	}, t.payment)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "token_rotate",
		Description: "Explains how the user can replace their API token. The token is never rotated or shown here.",
		Annotations: &mcp.ToolAnnotations{Title: "Rotate the API token", ReadOnlyHint: true, OpenWorldHint: boolPtr(false)},
	}, t.tokenRotate)
}

// result wraps a command's JSON document as the tool's output.
func result(raw json.RawMessage, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	return nil, raw, nil
}

func (t *tools) simple(args ...string) mcp.ToolHandlerFor[noArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		return result(t.run(ctx, args...))
	}
}

func invalid(format string, args ...any) *ToolError {
	return &ToolError{Code: "invalid_argument", Message: fmt.Sprintf(format, args...), Exit: 2}
}

// input checks a path-or-URL argument and returns it for the command line.
func (t *tools) input(s source) (string, error) {
	p, u := strings.TrimSpace(s.Path), strings.TrimSpace(s.URL)
	switch {
	case p == "" && u == "":
		return "", invalid("give path (a local file) or url")
	case p != "" && u != "":
		return "", invalid("give path or url, not both")
	case u != "":
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return "", invalid("url must start with http:// or https://")
		}
		return u, nil
	}
	st, err := t.opts.Stat(p)
	if err != nil {
		return "", invalid("cannot read %s: %v", p, err)
	}
	if st.IsDir() {
		return "", &ToolError{Code: "invalid_argument", Exit: 2,
			Message: p + " is a folder; these tools take one file",
			Hint:    "audd recognize " + p + " --max-files N --dry-run (in a terminal) recognizes a folder as a resumable job"}
	}
	return p, nil
}

func (t *tools) recognize(ctx context.Context, _ *mcp.CallToolRequest, in recognizeIn) (*mcp.CallToolResult, any, error) {
	src, err := t.input(in.source)
	if err != nil {
		return nil, nil, err
	}
	args := []string{"recognize", "--no-art"}
	if len(in.Return) > 0 {
		for _, r := range in.Return {
			if !contains(Providers, r) {
				return nil, nil, invalid("unknown return value %q; use %s", r, strings.Join(Providers, ", "))
			}
		}
		args = append(args, "--return", strings.Join(in.Return, ","))
	}
	if in.At != "" {
		args = append(args, "--at", in.At)
	}
	if in.Duration != "" {
		args = append(args, "--duration", in.Duration)
	}
	return result(t.run(ctx, append(args, "--", src)...))
}

func (t *tools) enterprise(ctx context.Context, _ *mcp.CallToolRequest, in enterpriseIn) (*mcp.CallToolResult, any, error) {
	// A dry run sends nothing, so it may omit the limit and see the full
	// estimate. A real run always needs a positive limit.
	limit := "none"
	switch {
	case in.Limit != nil && *in.Limit < 1:
		return nil, nil, invalid("limit must be 1 or more")
	case in.Limit != nil:
		limit = strconv.Itoa(*in.Limit)
	case !in.DryRun:
		return nil, nil, &ToolError{Code: "limit_required", Exit: 6,
			Message: "limit is required: the most 12-second chunks to scan, each billed as one request",
			Hint:    "call recognize_enterprise with dry_run true (no limit needed) to see the estimated requests, then again with limit set"}
	}
	src, err := t.input(in.source)
	if err != nil {
		return nil, nil, err
	}
	// The limit bounds the cost and the client approved this call, which is
	// the confirmation the CLI would otherwise ask for.
	args := []string{"recognize", "--enterprise", "--limit", limit, "--no-art", "--yes"}
	if in.Tracklist {
		args = append(args, "--tracklist")
	}
	if in.DryRun {
		args = append(args, "--dry-run")
	}
	for _, o := range []struct {
		flag string
		v    *int
	}{{"--skip", in.Skip}, {"--every", in.Every}, {"--skip-first-seconds", in.SkipFirstSeconds}} {
		if o.v != nil {
			args = append(args, o.flag, strconv.Itoa(*o.v))
		}
	}
	return result(t.run(ctx, append(args, "--", src)...))
}

func (t *tools) streamsAdd(ctx context.Context, _ *mcp.CallToolRequest, in streamsAddIn) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.URL) == "" {
		return nil, nil, invalid("url is required")
	}
	args := []string{"streams", "add", "--id", strconv.Itoa(in.RadioID)}
	if in.Start {
		args = append(args, "--start")
	}
	return result(t.run(ctx, append(args, "--", in.URL)...))
}

func (t *tools) streamsRemove(ctx context.Context, _ *mcp.CallToolRequest, in radioIn) (*mcp.CallToolResult, any, error) {
	return result(t.run(ctx, "streams", "remove", "--yes", strconv.Itoa(in.RadioID)))
}

func (t *tools) history(ctx context.Context, _ *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, any, error) {
	args := []string{"streams", "history"}
	if in.RadioID != nil {
		args = append(args, "--id", strconv.Itoa(*in.RadioID))
	}
	if in.Since != "" {
		args = append(args, "--since", in.Since)
	}
	if in.Limit != nil {
		args = append(args, "--limit", strconv.Itoa(*in.Limit))
	}
	return result(t.run(ctx, args...))
}

func (t *tools) nowPlaying(ctx context.Context, _ *mcp.CallToolRequest, in nowPlayingIn) (*mcp.CallToolResult, any, error) {
	args := []string{"now-playing", "--once", "--no-art"}
	for _, id := range in.RadioIDs {
		args = append(args, strconv.Itoa(id))
	}
	return result(t.run(ctx, args...))
}

func (t *tools) catalogAdd(ctx context.Context, _ *mcp.CallToolRequest, in catalogIn) (*mcp.CallToolResult, any, error) {
	if in.AudioID == nil || *in.AudioID < 0 {
		return nil, nil, invalid("audio_id is required (0 or more)")
	}
	src, err := t.input(in.source)
	if err != nil {
		return nil, nil, err
	}
	return result(t.run(ctx, "catalog", "add", "--id", strconv.Itoa(*in.AudioID), "--", src))
}

func (t *tools) usage(ctx context.Context, _ *mcp.CallToolRequest, in usageIn) (*mcp.CallToolResult, any, error) {
	args := []string{"usage"}
	if in.Days != nil {
		args = append(args, "--days", strconv.Itoa(*in.Days))
	}
	return result(t.run(ctx, args...))
}

func (t *tools) docs(ctx context.Context, _ *mcp.CallToolRequest, in docsIn) (*mcp.CallToolResult, any, error) {
	args := []string{"docs"}
	if in.Topic != "" {
		args = append(args, "--", in.Topic)
	}
	raw, err := t.run(ctx, args...)
	if err != nil {
		return nil, nil, err
	}
	var doc struct {
		Markdown string `json:"markdown"`
	}
	if json.Unmarshal(raw, &doc) == nil && doc.Markdown != "" {
		return textResult(doc.Markdown), raw, nil
	}
	return nil, raw, nil
}

func (t *tools) payment(ctx context.Context, _ *mcp.CallToolRequest, in paymentIn) (*mcp.CallToolResult, any, error) {
	args := []string{"billing"}
	switch in.Action {
	case "subscribe":
		if strings.TrimSpace(in.Plan) == "" {
			return nil, nil, invalid("subscribe needs plan (a plan key from billing_plans, such as startup_plan)")
		}
		args = append(args, "subscribe", "--", in.Plan)
	case "renew":
		args = append(args, "renew")
	case "buy":
		if in.Requests <= 0 || in.Requests%1000 != 0 {
			return nil, nil, invalid("buy needs requests, a positive multiple of 1000")
		}
		args = append(args, "buy", strconv.Itoa(in.Requests))
	default:
		return nil, nil, invalid("action must be subscribe, renew, or buy")
	}
	// Never start a browser sign-in from here: if the sign-in lacks payment
	// access, the command says what the user has to run.
	args = append([]string{args[0], args[1], NoConsentFlag}, args[2:]...)
	return result(t.run(ctx, args...))
}

// NoConsentFlag is the hidden billing flag that fails with instructions
// instead of opening a browser to approve payment access.
const NoConsentFlag = "--no-consent-prompt"

// TokenRotateInstructions is what token_rotate returns.
var TokenRotateInstructions = map[string]any{
	"action_required": "Rotating the API token disables the current token at once, so only the user can do it. Ask them to run the command below in a terminal. If audd is signed in, it stores the new token for audd automatically; other places that use the old token need the new one.",
	"command":         "audd token rotate",
	"done":            false,
}

func (t *tools) tokenRotate(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
	return nil, TokenRotateInstructions, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Package mcpserver is the local MCP server behind `audd mcp`. It speaks
// MCP over stdio and runs each tool as the matching audd command in the same
// process, so tools use the same token, profile, cache, limits, and stream
// store as the CLI.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Result is the outcome of one in-process audd command.
type Result struct {
	Stdout, Stderr []byte
	Exit           int
}

// Runner runs an audd command (arguments without the program name) with
// JSON output and no terminal.
type Runner func(ctx context.Context, args []string) Result

// Options configure the server.
type Options struct {
	// Run executes audd commands. Required.
	Run Runner
	// Version is reported to clients.
	Version string
	// Stat checks local paths; defaults to os.Stat.
	Stat func(string) (os.FileInfo, error)
}

// Instructions tell the client's model how to use the tools.
const Instructions = `Tools for AudD music recognition and stream monitoring, run by the local audd CLI.

recognize identifies one file or URL with the standard endpoint (up to the first 12 seconds of audio; pass "at" for another moment). recognize_enterprise scans a whole recording and is billed per 12-second chunk, so a real run needs "limit"; for long files, call it first with dry_run true (no limit needed) to see the estimated requests. A URL's length is unknown, so a plan for a URL without a limit has requests null and unbounded true: set a limit. No match is not an error: the result is null.

Streams: streams_list, streams_add, streams_remove, streams_history, now_playing. A background recorder on this computer saves every stream result, so history is complete.

Payments and token changes are never made here: payment_link returns a link for the user to open, and token_rotate explains what the user has to run.`

// New returns the MCP server with every audd tool.
func New(opts Options) *mcp.Server {
	if opts.Stat == nil {
		opts.Stat = os.Stat
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "audd", Title: "AudD", Version: opts.Version}, &mcp.ServerOptions{
		Instructions: Instructions,
	})
	t := &tools{opts: opts}
	t.register(s)
	return s
}

// Serve runs the server on in/out until the client disconnects or ctx ends.
func Serve(ctx context.Context, opts Options, in io.ReadCloser, out io.WriteCloser) error {
	err := New(opts).Run(ctx, &mcp.IOTransport{Reader: in, Writer: out})
	if err != nil && isDisconnect(err) {
		return nil
	}
	return err
}

// isDisconnect reports whether Run ended because the client went away:
// stdin closed, the go-sdk's "server is closing" error, or a reply written
// into a pipe the exiting client closed.
func isDisconnect(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || isServerClosing(err) ||
		errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.EPIPE)
}

// codeServerClosing is the go-sdk's JSON-RPC error code for "server is
// closing", which Run returns when the client disconnects.
const codeServerClosing = -32004

func isServerClosing(err error) bool {
	var we *jsonrpc.Error
	return errors.As(err, &we) && we.Code == codeServerClosing
}

type tools struct {
	opts Options
	// mu runs one command at a time: commands share the process's
	// configuration and caches.
	mu sync.Mutex
}

// ToolError is a failed command as the tool reports it.
type ToolError struct {
	Code      string `json:"code"`
	APICode   int    `json:"api_code,omitempty"`
	Message   string `json:"message"`
	Hint      string `json:"hint,omitempty"`
	Retryable bool   `json:"retryable"`
	Exit      int    `json:"exit_code"`
}

func (e *ToolError) Error() string {
	var b strings.Builder
	b.WriteString(e.Message)
	if e.Hint != "" {
		b.WriteString("\nNext step: ")
		b.WriteString(e.Hint)
	}
	if e.Code != "" {
		fmt.Fprintf(&b, "\n(error code %s, exit code %d)", e.Code, e.Exit)
	}
	return b.String()
}

// run executes an audd command and returns its JSON document.
func (t *tools) run(ctx context.Context, args ...string) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// --format goes before a "--" that ends the flags.
	full := make([]string, 0, len(args)+2)
	at := len(args)
	for i, a := range args {
		if a == "--" {
			at = i
			break
		}
	}
	full = append(append(append(full, args[:at]...), "--format", "json"), args[at:]...)
	r := t.opts.Run(ctx, full)
	if r.Exit != 0 {
		return nil, parseError(r)
	}
	out := bytes.TrimSpace(r.Stdout)
	if len(out) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(out) || out[0] != '{' {
		return nil, &ToolError{Code: "unexpected_output", Message: "audd printed output that is not a JSON object", Exit: 1}
	}
	return json.RawMessage(out), nil
}

// parseError turns a failed command's stderr into a ToolError.
func parseError(r Result) *ToolError {
	te := &ToolError{Exit: r.Exit}
	for _, line := range bytes.Split(bytes.TrimSpace(r.Stderr), []byte("\n")) {
		var doc struct {
			Error *ToolError `json:"error"`
		}
		if json.Unmarshal(line, &doc) == nil && doc.Error != nil {
			doc.Error.Exit = r.Exit
			// Hints about CLI flags (run again with --debug) are not
			// something a tool call can act on.
			if strings.Contains(doc.Error.Hint, "--debug") {
				doc.Error.Hint = ""
			}
			return doc.Error
		}
	}
	te.Code = "failed"
	te.Message = strings.TrimSpace(string(r.Stderr))
	if te.Message == "" {
		te.Message = "audd exited with code " + strconv.Itoa(r.Exit)
	}
	return te
}

// textResult returns text as the tool's only content. Callers that also
// have a structured copy return it as the handler's second result.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func boolPtr(b bool) *bool { return &b }

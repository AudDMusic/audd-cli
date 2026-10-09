// Package agent holds what audd offers coding agents: the documentation
// topics behind `audd docs`, the skill and rules files `audd agent-setup`
// writes, and the machine-readable command reference behind
// `audd commands --json`.
package agent

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// CLIDoc is the CLI reference served by `audd docs cli`.
//
//go:embed cli.md
var CLIDoc string

// DefaultDocsBase is where the AudD docs live. Each page has a markdown twin
// at the same path plus ".md".
const DefaultDocsBase = "https://docs.audd.io"

// DocsBase returns the docs site, or AUDD_DOCS_URL when set (tests, mirrors).
func DocsBase() string {
	if u := strings.TrimSpace(os.Getenv("AUDD_DOCS_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return DefaultDocsBase
}

// Topic is one `audd docs` topic.
type Topic struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	// Path is the markdown page on the docs site; empty for embedded topics.
	Path string `json:"-"`
	// Account is the topic name the account service's docs tool knows, used
	// when the docs site cannot be reached; empty when it has none.
	Account string `json:"-"`
}

// Topics are the documentation topics, in display order.
var Topics = []Topic{
	{Name: "api", Title: "Recognition API: endpoints, parameters, results, errors", Path: "/.md", Account: "api"},
	{Name: "enterprise", Title: "Enterprise endpoint: whole files, every match with timestamps", Path: "/enterprise.md", Account: "enterprise"},
	{Name: "streams", Title: "Stream monitoring: streams, callbacks, longpoll", Path: "/streams.md", Account: "streams"},
	{Name: "upload", Title: "Sending audio files and URLs", Path: "/upload_audio_endpoint.md", Account: "upload"},
	{Name: "mcp", Title: "The AudD MCP server", Path: "/mcp.md"},
	{Name: "sdks", Title: "Official SDKs", Path: "/sdks.md"},
	{Name: "cli", Title: "This CLI: commands, limits, output, exit codes"},
}

// FindTopic looks a topic up by name (case-insensitive).
func FindTopic(name string) (Topic, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "file-upload" {
		name = "upload"
	}
	for _, t := range Topics {
		if t.Name == name {
			return t, true
		}
	}
	return Topic{}, false
}

// TopicNames lists the topic names.
func TopicNames() []string {
	names := make([]string, len(Topics))
	for i, t := range Topics {
		names[i] = t.Name
	}
	return names
}

// URL is the topic's markdown page, or "" for embedded topics.
func (t Topic) URL(base string) string {
	if t.Path == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + t.Path
}

// PageURL is the topic's page for people to open: the markdown URL without
// ".md" ("https://docs.audd.io/" for the api topic), or "" for embedded
// topics.
func (t Topic) PageURL(base string) string {
	u := t.URL(base)
	if u == "" {
		return ""
	}
	u = strings.TrimSuffix(u, ".md")
	if strings.HasSuffix(u, "/.") {
		u = strings.TrimSuffix(u, ".")
	}
	return u
}

// maxDocBytes bounds a downloaded page.
const maxDocBytes = 4 << 20

// FetchDocs downloads a topic's markdown from the docs site at base.
// Embedded topics are returned without a request.
func FetchDocs(ctx context.Context, client *http.Client, base string, t Topic) (string, error) {
	if t.Path == "" {
		return CLIDoc, nil
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL(base), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/markdown, text/plain;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned HTTP %d", t.URL(base), resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		return "", fmt.Errorf("%s returned a web page instead of markdown", t.URL(base))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDocBytes))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(b)) == "" {
		return "", fmt.Errorf("%s returned an empty page", t.URL(base))
	}
	return string(b), nil
}

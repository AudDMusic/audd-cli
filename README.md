# AudD CLI

`audd` is the command-line tool for [AudD](https://audd.io) music recognition. It recognizes music in files, URLs, folders, long recordings, and from the microphone; records and reports on your monitored streams; and manages your AudD account. It prints tables on a terminal and JSON when piped, so it works the same for people, scripts, and AI agents.

## Install

| Where | Command |
| --- | --- |
| Any system with Node.js | `npx @audd/cli recognize song.mp3` (no install) or `npm install -g @audd/cli` |
| Any system with Python | `uvx audd-cli recognize song.mp3` (no install), `pipx install audd-cli`, or `uv tool install audd-cli` |
| macOS (Homebrew) | `brew install auddmusic/tap/audd` |
| macOS, Linux (script) | `curl -fsSL https://github.com/AudDMusic/audd-cli/releases/latest/download/install.sh \| sh` |
| Windows (Scoop) | `scoop bucket add auddmusic https://github.com/AudDMusic/scoop-bucket` then `scoop install auddmusic/audd` |
| Docker | `docker run --rm -e AUDD_API_TOKEN -v "$PWD:/work" ghcr.io/auddmusic/audd-cli recognize song.mp3` |
| Go | `go install github.com/AudDMusic/audd-cli/cmd/audd@latest` |
| Manually | download an archive from [Releases](https://github.com/AudDMusic/audd-cli/releases) and put `audd` on your `PATH` |

Every package contains the same single binary for macOS, Linux, and Windows on x86-64 and ARM64. The install script downloads the release archive for your system, checks it against the release's SHA-256 checksums, and puts `audd` in `~/.local/bin` without sudo (`AUDD_INSTALL_DIR` and `AUDD_VERSION` change the folder and version).

`--at` clips and `audd listen` use [ffmpeg](https://ffmpeg.org) (or sox for `listen`) when it is installed; other commands need no extra tools. The Docker image has no ffmpeg.

## Quickstart

```sh
audd login                               # sign in; audd fetches your API token
# or: export AUDD_API_TOKEN=your-api-token   (get your token at https://dashboard.audd.io)

audd recognize song.mp3
audd recognize https://audd.tech/example.mp3
audd recognize song.mp3 --format json | jq .result.title
```

On a terminal a match prints as a card with the cover art, artist, title, album, label, and links. Piped, it is one JSON document:

```json
{
  "schema_version": 1,
  "input": "song.mp3",
  "cached": false,
  "result": {
    "artist": "Imagine Dragons",
    "title": "Warriors",
    "album": "Warriors",
    "release_date": "2014-09-18",
    "label": "Universal Music",
    "timecode": "00:40",
    "song_link": "https://lis.tn/Warriors"
  }
}
```

No match is not an error: `result` is `null` and the exit code is 0.

`--return apple_music,spotify` adds metadata from those services (also `deezer` and `musicbrainz`):

```sh
audd recognize song.mp3 --return apple_music,spotify --format json | jq .result.spotify.uri
```

## Signing in and tokens

`audd login` signs you in to your AudD account. Signing in gives `audd` your API token and enables the account commands (`account`, `usage`, `billing`, `token rotate`, `token refresh-local`). There are two ways to approve the sign-in, and `audd` picks one:

- **Browser**: on a desktop (macOS, Windows, or Linux with a display), `audd` opens the sign-in page and also prints its URL. If the browser is on another machine, approve there and paste back the address the browser was sent to.
- **Code**: over SSH, in containers, and in scripts and agents, `audd` prints a page and a short code. Open the page on any device, check that it shows the same code, and approve. The code expires in 15 minutes.

`audd login --browser` and `audd login --device` choose the method yourself. On the approval page you can untick any permission; `audd auth status` shows what was approved, and a command that needs one you unticked (payment links, rotating the token) asks for it again when you use it.

Without a terminal, `audd login` prints a `login_pending` JSON line with `verification_uri_complete` and `user_code` for an agent to show you, then waits until you approve.

The API token is the first of these that is set:

1. `--token`
2. `AUDD_API_TOKEN`
3. `audd config set token your-api-token` (kept in the system credential store, or in `~/.config/audd/credentials.json` with mode 0600 when there is none)
4. the token `audd login` fetched

`audd auth status` shows which one is in use. `audd logout` removes the sign-in and the token it fetched, and stops a background stream recorder that was using that token. CI jobs and agents only need `AUDD_API_TOKEN`. Profiles (`--profile work`, `AUDD_PROFILE`) keep separate sign-ins, tokens, and settings.

## Recognizing music

The standard endpoint analyzes up to the first 12 seconds of audio. To identify a later moment, send a clip from there (needs ffmpeg):

```sh
audd recognize mix.mp3 --at 1:30            # a 12-second clip from 1:30
audd listen                                 # record 10 seconds from the microphone
```

To find every song in a long recording, use the enterprise endpoint. It is billed per 12-second chunk, so it needs `--limit` (chunks per file, or `none`):

```sh
audd recognize mix.mp3 --enterprise --limit 20 --dry-run      # see the plan; nothing is sent
audd recognize mix.mp3 --enterprise --limit 20 --tracklist    # tracks with start and end times
```

A folder, a glob, several files, or a list on stdin is a batch. Batches need `--max-files` (or `none`), show a plan with the estimated requests and cost, and ask before starting:

```sh
audd recognize ./recordings --max-files 500 --dry-run
audd recognize ./recordings --max-files 500 > results.jsonl
find . -name '*.m4a' | audd recognize - --max-files 100 --yes
```

Every batch is a job saved after each file. Ctrl-C, a crash, or a dropped SSH session loses nothing that finished, and nothing finished is sent again:

```sh
audd jobs list
audd jobs resume <id>
audd jobs resume <id> --retry-failed
```

A plain resume sends only the files that are left. Files that failed after their upload started, or that were being sent when audd stopped, may have been counted, so they are sent again only with `--retry-failed`.

Running the same batch again while its job is unfinished offers to resume it. Without a terminal it stops with exit 6: pass `--resume` to continue that job, or `--new` to recognize every file again.

Results are cached by file contents, so the same audio under another name is free (`"cached": true`). `--no-cache` sends it anyway.

## Limits and confirmations

- `--dry-run` shows what a command would send and its estimated cost.
- `--limit` (enterprise) and `--max-files` (batches) are required, except with `--dry-run`, which shows the uncapped plan. There is no unbounded default.
- `--max-requests N` stops before spending more than N requests; `audd config set max_requests N` makes it the default. A stopped batch can be resumed.
- Anything that can cost more than one request shows a plan and asks first. Without a terminal it needs `--yes`.
- Custom catalog uploads (`audd catalog add`) are never retried automatically.
- Payment commands never charge: `audd billing subscribe`, `renew`, and `buy` print a Stripe link that you open and approve yourself.

## Streams

```sh
audd streams add https://radio.example/stream.mp3 --id 1
audd streams list
audd now-playing
audd streams history --since 24h
audd streams report --by artist --since 30d
audd streams export --since 7d --format csv > plays.csv
```

The first time you use a streams command, `audd` starts a recorder in the background. It longpolls all your streams and saves every result to a local database, so history, reports, and now-playing are complete even when no window is open. AudD also keeps the last 30 or so results of each stream, which the recorder reads when it starts to fill short gaps.

```sh
audd streams recorder status
audd streams recorder start --install-service   # start the recorder when you log in
audd streams recorder stop
audd streams record                             # the same recorder in the foreground
audd config set streams.background_recorder false
```

The recorder runs until you stop it or restart the computer; `--install-service` adds a systemd user unit, a launchd agent, or a Windows scheduled task.

Receiving results needs a callback URL on the account. When there is none, `audd` offers to set the placeholder `https://audd.tech/empty/`; without a terminal to ask in, as in a container, `--yes` sets it. To use your own, run `audd streams callback set <url>` first.

For servers, run `audd streams record` under your own service manager or in Docker:

```sh
docker run -d --name audd-recorder --restart unless-stopped \
  -e AUDD_API_TOKEN -v audd-data:/home/nonroot ghcr.io/auddmusic/audd-cli streams record --yes
```

`audd streams watch --forward-to http://localhost:8080/callback` (and `streams record --forward-to`) POSTs each result to your URL in the shape of an AudD callback, for testing a callback handler locally.

### Now playing

`audd now-playing` is the terminal version of the [AudD widget](https://widget.audd.tech): a full-screen card for each stream with the cover art, title, artist, album, label and year of the latest song, and the songs played before it (up to 30). With several streams it shows a grid; the arrow keys pick a stream and Enter zooms in. A stream that is down shows its status instead of a song. `--notify` sends a desktop notification on each new song.

What the card shows depends on when AudD sends a stream's results:

- By default AudD sends a result when the song ends, with when it started and how long it played. The card shows the song as "Just played" (later "Last played"), how long it played, and when it ended.
- For a stream added with `audd streams add <url> --id N --start`, results arrive when songs start, so the card shows "Now playing" with the time since the song started. These results carry no play length, so `audd streams report` can't count airtime for that stream.
- A progress bar needs the track length, which stream results include only with Apple Music, Spotify, or Deezer metadata. Turn it on with `audd streams callback set <url> --return apple_music`.

For status bars and scripts:

```sh
audd now-playing --once
audd now-playing 1 --format "{{.Artist}} - {{.Title}}"     # a line each time the song changes
audd now-playing --timeout 1h > plays.jsonl                 # JSON lines when piped
```

JSON output and `--format` templates include the state of the song (`playing`, `just_played`, or `last_recognized`) with the times that go with it; `audd now-playing --help` lists the fields.

## Explorer

`audd browse` opens a full-screen explorer with four tabs:

- **Recent**: everything you recognized with `audd`, with search, full details, links, ISRC and UPC.
- **Jobs**: batch progress, failures, results, and enterprise tracklists; resume from here.
- **Streams**: what each stream is playing, its health, and recent plays; add or remove streams.
- **Usage**: requests this billing cycle, per day.

Keys: arrows move, Enter opens, `/` filters, `o` opens the song link, `c` copies it, `e` exports the view to CSV or JSON, `?` lists every key, `q` quits. `audd jobs browse <id>` and `audd streams watch` open the matching tab. Without a terminal, `browse` prints the same data as JSON, or as flat rows (a column per field, a row per song) with `--format csv`.

Cover art is drawn as an image on terminals that support Kitty graphics, iTerm2 inline images, or sixel (Kitty, Ghostty, iTerm2, WezTerm, foot, Konsole, and others), and with colored half-block characters everywhere else. `--no-art` (on `recognize`, `listen`, and `now-playing`) or `AUDD_NO_ART=1` turns art off, and `AUDD_ART=kitty|iterm2|sixel|blocks|blocks256|none` picks the method.

## Account

```sh
audd account
audd usage
audd usage --check --min-remaining 1000     # exit 8 when fewer requests remain
audd billing plans
audd billing subscribe <plan> --open        # opens a Stripe payment link
audd token show                             # masked; --reveal prints it
audd token rotate
```

## For scripts and AI agents

- Piped output is JSON. Every document has `"schema_version": 1`. Streaming commands (batches, `streams watch`, `streams export`, piped `now-playing`) print JSON lines with a `"type"` of `result`, `progress`, `summary`, `error`, or `event`.
- `--format table|json|jsonl|csv` (or `AUDD_FORMAT`) picks a format, `--fields result.artist,result.title` trims it (CSV columns are flat, such as `artist`; a field the output can never have exits 2 before anything is sent, and one a result lacks prints as null), and `--quiet` hides notes on stderr and, in table output, prints only the essential line.
- `audd commands --json` describes every command, flag, output, and exit code.
- `audd api <method> key=value...` calls any AudD API method directly.

```sh
audd agent-setup                  # write a skill or rules file for Claude Code, Cursor, or Codex
claude mcp add audd -- audd mcp   # use audd as a local MCP server
audd docs api                     # the AudD API docs as markdown
```

`audd mcp` is a local MCP server over stdio with tools for recognition, enterprise scans (with a required `limit`), streams, now-playing, custom catalog uploads, usage, account status, and docs. It uses the same token, profile, and limits as the CLI.

The full command reference is [docs/cli.md](docs/cli.md) (also `audd docs cli`).

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success, including no match |
| 1 | Unexpected error, or no match with `--fail-on-no-match` |
| 2 | Invalid arguments or input, or a missing tool such as ffmpeg |
| 3 | Authentication: missing or rejected token, or sign-in needed |
| 4 | Quota, plan, or feature not available on the account |
| 5 | Network or server error |
| 6 | Safety bound: a missing `--limit` or `--max-files`, a confirmation needed (`--yes`), `--max-requests` reached, or an unfinished job for the same files (`--resume` or `--new`) |
| 7 | Batch finished with some files failed |
| 8 | `usage --check` found fewer requests remaining than `--min-remaining` |
| 130 | Stopped with Ctrl-C; `audd jobs resume` continues a batch |

Errors go to stderr. When output is not a table they are JSON: `{"schema_version":1,"error":{"code","api_code","message","hint","retryable"}}`, where `hint` is usually the next command to run.

## Updates and privacy

`audd update` installs the latest release, or prints the update command for the package manager you installed it with. A notice about new releases appears at most once a day on a terminal, never in CI; `AUDD_NO_UPDATE_CHECK=1` turns it off.

There is no telemetry. `audd` talks only to AudD services, cover art hosts, and GitHub (for the release check). `HTTPS_PROXY` and `NO_PROXY` are honored, and `--debug` logs requests with tokens removed.

## Verifying a download

Each release has `checksums.txt` with the SHA-256 of every archive, a Sigstore bundle for it, an SBOM per archive, and GitHub build provenance:

```sh
sha256sum --ignore-missing -c checksums.txt
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/AudDMusic/audd-cli/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify audd_1.0.0_linux_amd64.tar.gz --repo AudDMusic/audd-cli
```

## Contributing

Run `gofmt -l .`, `go vet ./...`, and `go test -race ./...` before sending changes. The binary must build with `CGO_ENABLED=0` for Linux, macOS, and Windows.

### Adding commands

Commands live in `internal/cli`, one file per command group. A file adds its commands from `init()` with `Register`, so nobody edits `root.go`:

```go
func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newStreamsCmd(a))
	})
}
```

- The callback runs while the tree is built. The `*app.App` it receives is filled in (config, profile, secrets, printer, flags) just before the command runs, so read its fields inside `RunE`.
- Put each top-level command in a help group with `GroupID` (`cli.GroupRecognize`, `GroupStreams`, `GroupAccount`, `GroupLocal`, `GroupAgents`, `GroupOther`).
- Mark commands whose piped output is a stream of records with `Annotations: map[string]string{cli.AnnotationStreaming: "true"}`; commands that stream only sometimes call `a.Out.SetStreaming()`.
- Do not set `PersistentPreRunE` on the root. Subcommands may set their own; the root's still runs first.
- Return `*output.Error` (via `output.Errf`) for expected failures so the exit code and hint are right. Any other error from `RunE` exits with 1.
- Write results through `a.Out` (`Result`, `Event`, `Info`, `Confirm`), never directly to `os.Stdout`.

### Hooks and factories

`internal/app` holds hooks that let packages call each other without import cycles. Each one has a single owner that assigns it; everyone else only calls it.

| Hook | Owner | Assigned in |
| --- | --- | --- |
| `app.RunBatch` | `internal/jobs` | `init()` |
| `app.RunExplorer` | `internal/tui` | `init()` |
| `app.RenderResult` | `internal/tui` (plain text until then) | `init()` |
| `app.EnsureRecorder` | `internal/streams` | `init()` |
| `App.APIClient` | `internal/api` | a `cli.Register` callback |
| `App.Account` | `internal/account` | a `cli.Register` callback |

Unassigned hooks return an error with code `not_implemented`.

### Tests

- `testutil.Isolate(t)` points the config, cache, and data dirs at temp dirs (`AUDD_CONFIG_DIR`, `AUDD_CACHE_DIR`, `AUDD_DATA_DIR`) and uses the file credential store.
- `testutil.Exec(t, cli.Main, stdin, args...)` runs the CLI in-process.
- `testutil.Golden(t, name, got)` compares against `testdata/<name>.golden`; set `AUDD_UPDATE_GOLDEN=1` to rewrite.
- Tests never call the AudD API or MCP server. Use `httptest` servers, and use `0123456789abcdef0123456789abcdef` wherever a token is needed.

## License

MIT. Copyright (c) 2026 AudD, LLC (https://audd.io).

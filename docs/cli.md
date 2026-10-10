# AudD CLI

`audd` recognizes music in files, URLs, folders, long recordings, and from the
microphone; monitors streams; and manages your AudD account. It prints tables
on a terminal and JSON when its output is piped.

Get your API token at https://dashboard.audd.io, or run `audd login`.

## Install

| Where | Command |
| --- | --- |
| Node.js | `npm install -g @audd/cli` |
| Python | `pipx install audd-cli` or `uv tool install audd-cli` |
| macOS (Homebrew) | `brew install auddmusic/tap/audd` |
| macOS, Linux (script) | `curl -fsSL https://github.com/AudDMusic/audd-cli/releases/latest/download/install.sh \| sh` |
| Windows (Scoop) | `scoop bucket add auddmusic https://github.com/AudDMusic/scoop-bucket` then `scoop install auddmusic/audd` |
| Go | `go install github.com/AudDMusic/audd-cli/cmd/audd@latest` |
| Any system | download an archive from https://github.com/AudDMusic/audd-cli/releases and put `audd` on your `PATH` |

The install script puts `audd` in `~/.local/bin` and checks the download
against the release's checksums.

Run it without installing:

```sh
npx @audd/cli recognize song.mp3          # Node.js
uvx audd-cli recognize song.mp3           # Python
docker run --rm -e AUDD_API_TOKEN -v "$PWD:/work" ghcr.io/auddmusic/audd-cli recognize song.mp3
```

## Sign in

```sh
audd login                      # sign in; fetches your API token for you
export AUDD_API_TOKEN=your-api-token   # or set the token directly (CI, scripts)
audd config set token your-api-token   # or store it in the system credential store
```

The API token is taken from, in order: `--token`, `AUDD_API_TOKEN`,
`audd config set token`, and the token `audd login` fetched. `audd auth status`
shows which one is in use. Recognition and `token show` need only a token;
`account`, `usage`, `billing`, `token rotate`, and `token refresh-local` need
`audd login`.

`audd login` picks one of two ways to approve the sign-in:

- Browser: on a desktop, it opens the browser and prints the URL. If the
  browser is on another machine, paste back the address it was sent to.
- Code: over SSH, in containers, and without a terminal, it prints a page and
  a code. Open the page on any device, check the code, and approve. The code
  expires in 15 minutes.

Pass `--browser` or `--device` to pick the method. Without a terminal it
uses a code and prints a `login_pending` JSON record with `verification_uri`,
`verification_uri_complete`, `user_code`, `expires_in_seconds`, and
`interval`, then waits until the sign-in is approved, denied, or expires.
Agents: show the user `verification_uri_complete` and `user_code`, and let
the command keep running. On the approval page the user can untick
permissions; `audd auth status` shows what was approved.

## Recognize music

```sh
audd recognize song.mp3
audd recognize https://audd.tech/example.mp3
audd recognize song.mp3 --return apple_music,spotify
audd recognize mix.mp3 --at 1:30                 # send a 12-second clip from 1:30 (needs ffmpeg)
audd listen                                      # record from the microphone and identify
```

The standard endpoint analyzes up to the first 12 seconds of audio. Use `--at`
to pick another moment, or `--enterprise` to scan a whole recording.

`--return` adds metadata from `apple_music`, `spotify`, `deezer`, and
`musicbrainz`.

When nothing matches, the result is `null` and the exit code is 0. Pass
`--fail-on-no-match` to exit 1 instead. In a batch it exits 1 when any file
has no match; failed files take precedence (exit 7).

### Whole recordings (enterprise)

```sh
audd recognize mix.mp3 --enterprise --limit 20 --dry-run
audd recognize mix.mp3 --enterprise --limit 20 --tracklist
```

`--enterprise` returns every match with its position in the file. It is billed
per 12-second chunk, so it needs `--limit N` (chunks per file) or `--limit none`.
`--dry-run` works without `--limit` and shows the full, uncapped cost.
`--tracklist` merges consecutive matches into tracks with start and end times.
`--skip`, `--every`, `--skip-first-seconds`, `--use-timecode`, and
`--accurate-offsets` are passed to the enterprise endpoint.

### Folders, globs, and lists

```sh
audd recognize ./recordings --max-files 200 --dry-run
audd recognize ./recordings --max-files 200 > results.jsonl
ls *.mp3 | audd recognize - --max-files 50 --yes
```

A folder, a glob, several files, or a list on stdin (`-`) is a batch. It needs
`--max-files N` (or `none`), shows a plan with the estimated number of
requests, and asks to confirm. Without a terminal, pass `--yes` to confirm. A
list read from stdin needs `--yes` whenever it would spend requests: the list
takes up stdin, so `audd` cannot ask.
`--dry-run` works without `--max-files`.

Every batch is a job saved after each file, so Ctrl-C or a dropped connection
loses no finished files:

```sh
audd jobs list
audd jobs show <id>
audd jobs resume <id>
audd jobs resume <id> --retry-failed
```

`audd jobs resume <id>` sends the files that are left. Files that failed after
their upload started, or that were being sent when audd stopped, may have been
counted, so they are sent again only with `--retry-failed`.

Running the same batch again while its job is unfinished offers to resume it.
Without a terminal it stops with exit 6 (`resume_available`): pass `--resume`
to continue that job, or `--new` to recognize every file again.

Batch output is one JSON line per file (`"type": "result"`), then a
`"type": "summary"` line. The summary's counts, `requests_spent` included, are
for the whole job, as `audd jobs list` shows them; `requests_spent_this_run`
is what this run used.

### Results cache

Results are cached by file contents (URLs for 24 hours). Recognizing the same
audio again is free and marked `"cached": true`. `--no-cache` sends it anyway;
`audd cache clear` empties the cache.

## Limits and confirmations

- `--dry-run` shows the plan and the estimated requests without sending anything.
  Piped, one input and a batch print the same document:
  `{"dry_run":true,"input":...,"job_id":...,"endpoint":...,"plan":{...}}`, where
  `input` is null for a batch and `job_id` is set when a batch would resume a job.
  With `--format jsonl` it is one line with `"type":"event"` and
  `"event":"dry_run"`, so it is never counted as a file's result.
- `--max-requests N` stops before spending more than N requests (exit 6). A
  stopped batch can be resumed. `audd config set max_requests N` sets a default.
- Commands that can spend more than one request show a plan and ask to confirm.
  Without a terminal they need `--yes`.
- A single standard recognition never asks.

## Streams

```sh
audd streams add https://radio.example/stream.mp3 --id 1
audd streams list
audd now-playing                 # full-screen view with cover art
audd now-playing --once          # the latest song on each stream
audd streams watch               # results as they arrive
audd streams history --since 24h
audd streams report --by artist --since 30d
audd streams export --since 7d --format csv > plays.csv
```

`audd now-playing` is a terminal version of the AudD widget (widget.audd.tech): the
latest song on each stream with its cover art, and the songs played before it.
With `--once`, a stream whose results can't be read is listed with an `"error"`
object; the command fails (exit 5) only when no stream can be read. With
`--format csv` it prints one row per stream, with a column per field.

What now-playing shows depends on when AudD sends a stream's results:

- By default, when the song ends, with `timestamp` (when the song started) and
  `play_length` (seconds played). The song is shown as "Just played" (after a
  few minutes, "Last played") with how long it played and when it ended;
  `state` is `just_played`, then `last_recognized`.
- For streams added with `audd streams add <url> --id N --start`, when the
  song starts, without `play_length`. The song is shown as "Now playing" with
  the time since it started (`state` `playing`), and as "Last recognized"
  after 10 minutes with no new result. Without a play length, `streams report`
  can't count airtime for those streams.
- The track length, and with it a progress bar, is in stream results only
  with Apple Music, Spotify, or Deezer metadata. Turn it on with
  `audd streams callback set <url> --return apple_music`.

JSON output (`--once`, and each JSON line when piped) adds to every song:
`state`, `playing`, `elapsed_seconds` (since the song started, while
playing), `played_seconds` and `ended_at` (results sent when the song ended),
`ago_seconds` (since it ended, or since it started when the end is unknown,
when not playing), and `length_seconds` (when the track length is known).
`--format` templates see the song fields (`.Artist`, `.Title`, `.Album`,
`.Label`, `.ReleaseDate`, `.SongLink`, `.ISRC`, `.UPC`), `.RadioID`, `.URL`,
and `.State`, `.Playing`, `.At`, `.Elapsed`, `.Played`, `.Ended`, `.Ago`,
`.Length`.

### Background recorder

The first time you use a streams command, `audd` starts a background recorder
that receives every result as it happens and saves it to a local database, so
history, reports, and now-playing stay complete. AudD also keeps the last 30
or so results for each stream; the CLI uses them to fill short gaps.

```sh
audd streams recorder status
audd streams recorder stop
audd streams recorder start --install-service   # start the recorder when you log in
audd streams record                             # the same recorder in the foreground
audd config set streams.background_recorder false
```

`audd streams watch --forward-to http://localhost:8080/callback` and
`audd streams record --forward-to URL` also POST each result to your URL,
shaped like an AudD callback, for testing a callback handler locally.

Receiving results needs a callback URL on the account. When it is missing, the
CLI offers to set `https://audd.tech/empty/`. Set your own with
`audd streams callback set <url>`.

### Cover art

`audd now-playing`, an expanded result in `audd browse`, and a single
result printed on a terminal show the song's cover art. The full-screen
views draw it as an image when the terminal supports one:

- Kitty and Ghostty: Kitty graphics.
- iTerm2, WezTerm, and VS Code with terminal images turned on: iTerm2
  inline images.
- Terminals that report sixel support, such as foot and Konsole: sixel.
- Everywhere else, and inside tmux or screen: colored half-blocks, in true
  color when the terminal has it and in 256 colors otherwise.

Before a full-screen view opens, `audd` asks the terminal what it supports
and how large its character cells are, so covers stay square at any font
size. Images move with the layout and are redrawn after a resize or a song
change. Art is never written when the output is not a terminal or is JSON.

`--no-art` (on `recognize`, `listen`, and `now-playing`) or `AUDD_NO_ART=1`
turns art off. Set `AUDD_ART=kitty|iterm2|sixel|blocks|blocks256|none` to
pick the method yourself.

## Custom catalog

```sh
audd catalog add song.mp3 --id 42
```

Fingerprints a song into your custom catalog under `audio_id` 42. Each upload
is billed and is never retried automatically. Custom catalog access is enabled
per account.

## Account

```sh
audd account
audd usage
audd usage --check --min-remaining 1000     # exit 8 when fewer remain
audd billing plans
audd billing subscribe <plan>               # prints a payment link
audd billing buy 10000 --open
audd token show                             # masked; --reveal prints it
audd token rotate
```

Payment commands never charge: they print a Stripe link that you open and
approve in the browser.

## Output for scripts and agents

- Piped output is JSON. Every JSON document has `"schema_version": 1`.
- Streaming commands (batches, `streams watch`, `streams export`,
  `now-playing` when piped) print JSON lines with a `"type"` field:
  `result`, `progress`, `summary`, `error`, or `event`.
- `--format table|json|jsonl|csv` (or `AUDD_FORMAT`) picks a format.
  `--fields a,b` keeps only those fields; nested fields are dotted
  (`result.artist`). In JSON lines it trims `result` lines only, so
  `event` and `summary` lines keep their job ID and counts. CSV columns are
  flat (`artist`, `isrc`), the same for one file and a batch. A field the
  output can never have is a usage error (exit 2) that lists the available
  fields, before anything is sent; a field one result lacks (no ISRC, say)
  prints as null.
- `--quiet` hides notes and progress on stderr. In table output it also
  prints only the song line (`Imagine Dragons — Warriors`); JSON
  output is unchanged.
- Errors go to stderr. In JSON mode they look like
  `{"schema_version":1,"error":{"code":"...","api_code":0,"message":"...","hint":"...","retryable":false}}`;
  `hint` is usually the next command to run.
- `audd commands --json` describes every command (shortcuts such as `audd logout`
  name the command they run in `alias_of`), flag, and output.
- `audd api <method> key=value...` calls any API method directly.

### Exit codes

| Code | Meaning |
| ---- | ------- |
| 0 | Success, including no match |
| 1 | Unexpected error, or no match with `--fail-on-no-match` |
| 2 | Invalid arguments or input, or a missing tool such as ffmpeg |
| 3 | Authentication: missing or rejected token, or sign-in needed |
| 4 | Quota, plan, or feature not available on the account |
| 5 | Network or server error |
| 6 | Safety bound: a missing `--limit` or `--max-files`, a confirmation needed (`--yes`), `--max-requests` reached, or an unfinished job for the same files (`--resume` or `--new`) |
| 7 | Batch finished with some files failed |
| 8 | `usage --check` found fewer requests remaining than `--min-remaining` |
| 130 | Stopped with Ctrl-C; a batch keeps finished files, and `audd jobs resume` continues it |

## Agents

```sh
audd agent-setup                 # write a skill or rules file for your coding agent
audd agent-setup --print         # print it instead
claude mcp add audd -- audd mcp  # use audd as a local MCP server
audd docs api                    # the AudD API docs as markdown
```

`audd mcp` runs a local MCP server over stdio with tools for recognition,
enterprise scans (with a required `limit`), streams, now-playing, custom
catalog uploads, usage, account status, and docs. It uses the same token,
profile, and limits as the CLI.

## Settings

```sh
audd config list
audd config set format json
audd config set max_requests 500
audd config set concurrency 8
audd --profile work recognize song.mp3
```

Each profile has its own sign-in, token, and settings. `AUDD_PROFILE` selects
one; `audd auth switch <profile>` changes the default.

## Updates and privacy

`audd update` installs the latest release, or prints the command for the
package manager you installed it with. A notice about new releases appears at
most once a day on a terminal; `AUDD_NO_UPDATE_CHECK=1` turns it off.

There is no telemetry. The CLI talks only to AudD services, cover art hosts,
and GitHub for the release check. `HTTPS_PROXY` and `NO_PROXY` are honored, and
`--debug` logs requests with tokens removed.

More: `audd <command> --help` and https://docs.audd.io.

# @audd/cli

`audd` is the command-line tool for [AudD](https://audd.io) music recognition:
recognize music in files, URLs, folders, and long recordings, monitor streams,
and manage your AudD account from a terminal, a script, or an AI agent.

This package installs the prebuilt `audd` binary for your system.

```sh
npx @audd/cli recognize song.mp3      # run without installing
npm install -g @audd/cli              # or install the audd command
audd login                            # or get your API token at https://dashboard.audd.io
audd recognize song.mp3
```

Output is a table on a terminal and JSON when piped. `audd --help` lists every
command, and `audd docs cli` prints the full reference.

The binary comes from a per-system package (`@audd/cli-linux-x64`,
`@audd/cli-darwin-arm64`, and so on) that npm installs as an optional
dependency. Installing with `--omit=optional` leaves it out.

Documentation, other install methods, and source:
https://github.com/AudDMusic/audd-cli

MIT License. Copyright (c) 2026 AudD, LLC (https://audd.io).

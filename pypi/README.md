# audd-cli

`audd` is the command-line tool for [AudD](https://audd.io) music recognition:
recognize music in files, URLs, folders, and long recordings, monitor streams,
and manage your AudD account from a terminal, a script, or an AI agent.

This package installs the prebuilt `audd` binary for your system.

```sh
uvx audd-cli recognize song.mp3       # run without installing
pipx install audd-cli                 # or: uv tool install audd-cli
audd login                            # or get your API token at https://dashboard.audd.io
audd recognize song.mp3
```

Output is a table on a terminal and JSON when piped. `audd --help` lists every
command, and `audd docs cli` prints the full reference.

Documentation, other install methods, and source:
https://github.com/AudDMusic/audd-cli

MIT License. Copyright (c) 2026 AudD, LLC (https://audd.io).

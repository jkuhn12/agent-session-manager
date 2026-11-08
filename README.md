# agent-session-manager (`asm`)

A tiny Go TUI that lists **every opencode and Claude Code session across all
project folders**, sorted by last activity, with fuzzy search. Pick a session
and `asm` resets the shell into that project, then resumes the right agent in
the same terminal.

It solves the "laptop restarted, I forgot which sessions I had open and what
state they were in" problem.

> The full design rationale, research notes and milestone history live in
> [PLAN.md](./PLAN.md). This README is the user and operator reference.

## Contents

- [Features](#features)
- [Requirements](#requirements)
- [Install](#install)
- [Quick start](#quick-start)
- [CLI reference](#cli-reference)
- [Keys](#keys)
- [zsh wrapper](#zsh-wrapper)
- [Session sources](#session-sources)
- [How it works](#how-it-works)
- [Configuration](#configuration)
- [Project layout](#project-layout)
- [Verification](#verification)
- [Status and implementation notes](#status-and-implementation-notes)
- [Limitations](#limitations)
- [Troubleshooting](#troubleshooting)
- [Development](#development)
- [License](#license)

## Features

- **Both agents now**: opencode (SQLite) and Claude Code (JSONL transcripts).
  The Claude provider is fixture-tested and smoke-tested when a real session
  exists.
- **Everything on disk**: every top-level session across all directories,
  including `~`, `~/git` itself, and projects that no longer exist.
- **Flat, newest-first list** with fuzzy filtering across folder, title, full
  path and agent.
- **One row per session**: `[oc]`/`[cc]` agent badge, compact `~/…` directory
  (parent dimmed, project folder bold), title, relative age, and a `gone` badge
  when the project folder vanished.
- **Preview pane** with the last user message, message count, model, cost,
  open/total todos, and created/updated timestamps.
- **Same-terminal resume**: the wrapper `cd`s into the session directory and
  launches the agent as a child process, so quitting the agent leaves you in a
  shell already inside the project.
- **Degrades safely**: a missing or broken provider never kills the picker;
  problems are surfaced by `asm doctor` and as stderr warnings.

## Requirements

- **Go 1.26+** to build. (The current `modernc.org/sqlite` dependency pulls in a
  `golang.org/x/sys` version that requires Go 1.26.)
- **opencode** and/or **Claude Code** installed with session storage present.
- **zsh** for the provided resume wrapper. The picker itself is a normal
  terminal program and can be wrapped from other shells manually.
- **A terminal**: the picker renders to `/dev/tty` and needs interactive input.
  Use `asm list` for scripts and non-interactive environments.

Developed and verified on macOS (darwin/arm64) with zsh and Ghostty; the code is
portable, but Linux has not been exercised as of this writing.

## Install

```sh
go install github.com/jkuhn12/agent-session-manager/cmd/asm@latest
```

Or from a checkout:

```sh
git clone git@github.com:jkuhn12/agent-session-manager.git
cd agent-session-manager
make install
```

`make install` uses `go install` and stamps the version from `git describe`.
For a local binary in the repo root:

```sh
make build   # produces ./asm
```

Check what you have:

```sh
asm version
asm doctor
```

## Quick start

```sh
# 1. Install the shell wrapper (prints a zsh snippet)
asm init >> ~/.zshrc

# 2. Reload the shell
exec zsh

# 3. Pick a session from anywhere
asm
```

`asm` with no arguments is the interactive picker. On selection it prints a
tab-separated line on stdout; the zsh function parses it, `cd`s, and resumes the
agent. Cancelling writes nothing to stdout and exits `130`.

If the wrapped `asm` is not found, make sure `$(go env GOPATH)/bin` is on
`$PATH`.

## CLI reference

```
asm [pick] [flags]      interactive picker (default)
asm list [--json]       print sessions, newest first
asm doctor              show where sessions are read from
asm init                print the zsh wrapper function
asm version             print the version
asm help                print usage
```

`asm ls` is an alias for `asm list`. `asm --no-preview` is shorthand for
`asm pick --no-preview`.

### `asm` / `asm pick`

| Flag | Meaning |
| --- | --- |
| `--no-preview` | Disable the detail preview pane. |
| `--title string` | Override the picker header (default: `Agent Sessions`). |

The picker renders exclusively on `/dev/tty`, keeping stdout clean for the
result protocol. On Enter it prints:

```
<directory>\t<agent>\t<session-id>
```

- `agent` is `opencode` or `claude`.
- Tabs survive spaces in paths (for example `AX Tracker`).
- Esc / Ctrl-C print nothing and exit `130`.
- No sessions found prints an error on stderr and exits `1`.
- Usage errors exit `2`.

Custom wrappers can consume the same protocol from bash, fish, etc.: capture
stdout, split on tabs, `cd` to the first field, then dispatch on the second.

### `asm list`

Prints a human-readable table with `AGENT`, `UPDATED`, `FOLDER`, `TITLE` and
`DIRECTORY` columns. Missing directories are marked `(gone)`. Provider warnings
go to stderr, so stdout stays pipeable.

```sh
asm list
asm list --json
```

`--json` emits an indented array of session objects:

```json
[
  {
    "id": "ses_...",
    "agent": "opencode",
    "directory": "/Users/me/git/project",
    "title": "Fix the login flow",
    "time_created": "2026-11-08T14:55:19.174-05:00",
    "time_updated": "2026-11-08T15:09:12.826-05:00",
    "dir_exists": true
  }
]
```

An empty result is `[]` (or a stderr note for the table form) and still exits
`0`.

### `asm doctor`

Diagnoses both stores without mutating anything. It reports:

- whether the `opencode` and `claude` binaries are on `$PATH`
- resolved opencode DB path, file existence, tables found, per-table usable
  counts, and excluded subagent/archived rows
- schema caveats (for example a legacy-only database or a missing
  `session_message` table)
- resolved Claude projects directory, existence, project-directory count and
  session-file count; an absent directory is normal ("no Claude Code sessions
  yet")
- merged session count and missing directories (deduplicated, first five shown)
- any provider errors encountered while merging

`asm doctor` exits `0` even when it reports warnings; it is a diagnostic command.

### `asm init`

Prints the zsh wrapper function (see below). It does not edit `~/.zshrc`; append
it yourself or copy it into a managed dotfiles file.

## Keys

| Key | Action |
| --- | --- |
| type | fuzzy filter across folder, title, full path and agent |
| ↑ / ↓ | move (list defaults also accept `j`/`k` while not filtering) |
| Enter | select the highlighted session (works mid-filter too) |
| Esc | clear an active filter; when unfiltered, quit with exit `130` |
| Ctrl-C | quit immediately with exit `130` |

The header shows `N/M filtered · query` while filtering. The footer shows the
keys relevant to the current mode. The preview pane appears when the terminal is
tall enough and hidden with `--no-preview`.

## zsh wrapper

A child process cannot `cd` its parent shell. Following the zoxide/fzf pattern,
the picker prints the selection on stdout and a shell function performs the
`cd` and resume.

`asm init` prints:

```zsh
# Add this to ~/.zshrc, then run: exec zsh
asm() {
  # Subcommands pass through. Bare asm and picker flags run the picker.
  case "${1:-pick}" in
    pick) ;;
    -h|--help|-v|--version|help|version|list|ls|doctor|init)
      command asm "$@"
      return
      ;;
    -*) set -- pick "$@" ;;
    *) command asm "$@"; return ;;
  esac

  local sel dir agent sid
  sel=$(command asm "$@") || return
  IFS=$'\t' read -r dir agent sid <<< "$sel"
  [[ -n "$dir" ]] || return
  cd "$dir" || return
  case "$agent" in
    opencode) opencode -s "$sid" ;;
    claude)   claude --resume "$sid" ;;
  esac
}
# optional grep-able keybind for zsh: bindkey -s '^G' 'asm^M'

# Optional ghostty keybind (new split running the picker), in
# ~/.config/ghostty/config:
# keybind = cmd+g=new_split:zsh -ic asm
```

Notes:

- The function is named `asm` and calls `command asm "$@"` to reach the binary,
  avoiding recursion. Subcommands (`list`, `doctor`, `init`, `version`, `help`)
  pass straight through; bare `asm` and picker flags (`--no-preview`,
  `--title`) run the picker.
- The agent runs as a **child process**, not `exec`; quitting it drops you back
  into a shell already in the project directory.
- Ghostty needs no special configuration beyond running the picker in whatever
  split/tab the shell is in. The optional `cmd+g` keybind opens a new split.
- If `cd` fails (a `gone` directory), the function returns without launching the
  agent, making the failure visible rather than resuming in the wrong place.

## Session sources

### opencode

Storage path precedence:

1. `$ASM_OPENCODE_DB`
2. `$XDG_DATA_HOME/opencode/opencode.db`
3. `~/.local/share/opencode/opencode.db`

The DB is opened **read-only** (`mode=ro`) with a 5s busy timeout and a short
retry on `SQLITE_BUSY`, so it can be read while the opencode server is running
(WAL mode allows concurrent readers).

Schema handling:

- Queries both the current `session_v2` table and the legacy `session` table.
- Unions them, dedupes by session id, and prefers the `session_v2` row.
  Legacy titles/directories fill in when the v2 values are empty.
- Excludes subagent rows (`parent_id IS NOT NULL`) and archived rows
  (`time_archived IS NOT NULL`).
- Detects tables and columns at runtime. Missing `session_v2`, missing optional
  columns, or a legacy-only database degrade gracefully instead of crashing.
- Titles are whitespace-cleaned; empty titles become `Untitled session`.
- Epoch-millisecond timestamps are converted with `time.UnixMilli`.
- Directories are **not** filtered out when missing; merge marks them `gone`.

Previews:

- v2 sessions read the latest `type = 'user'` row from `session_message` and
  count `user`/`assistant` rows.
- Legacy sessions read the latest user message from `message`, then concatenate
  its `type = "text"` rows from `part`.
- Model and cost come from the session row (`model` JSON such as
  `{"providerID":"...","id":"..."}` is rendered as `providerID/id`).
- Todos come from `todo`; "open" means any status other than `completed`.
- The last user message is truncated to 300 runes.

Resume command: `opencode -s <session-id>` (also `opencode --session <id>`),
always run from the session's directory.

### Claude Code

Storage path precedence:

1. `$ASM_CLAUDE_PROJECTS`
2. `$CLAUDE_CONFIG_DIR/projects`
3. `~/.claude/projects`

Layout and parsing:

- Each project directory contains `<session-uuid>.jsonl` transcripts. Only
  immediate `*.jsonl` files are scanned; nested
  `<project>/<session-id>/subagents/*.jsonl` files are excluded.
- The directory name is a lossy munging of the cwd (`/` → `-`). The real
  directory is recovered from the `cwd` field in transcript lines; the munged
  name is only a fallback.
- Last activity uses file mtime, which is authoritative because JSONL is
  append-per-turn.
- Creation time uses the first parseable ISO-8601 `timestamp`, falling back to
  mtime.
- Metadata is read efficiently from the head (~512 KiB) and tail (~64 KiB) of
  each transcript during listing; a full streaming scan is only done for the
  selected session's preview.
- Unknown line types and malformed JSON lines are ignored. Sidechain lines are
  ignored for title/cwd/preview counts.

Title precedence:

1. `custom-title` line (`customTitle` or `title`)
2. `summary` line
3. first non-empty user prose message (tool-result-only turns and injected
   meta lines starting with `<` are skipped)
4. `Session <first 8 chars of the UUID>`

Preview: message count (non-sidechain user + assistant lines), last user prose
message, and the last assistant `message.model`. Cost is not reported because
there is no reliable field in the transcript.

Resume command: `claude --resume <session-id>` (also `claude -r <id>`), always
run from the session's directory.

> Claude Code deletes transcripts after ~30 days by default
> (`cleanupPeriodDays`). `asm` only reflects what is on disk.

### Merging

`internal/merge` queries providers concurrently, skips unavailable providers,
dedupes by `(agent, id)`, cleans titles, stats every final directory once to set
`DirExists`, and sorts by:

1. `TimeUpdated` descending
2. `TimeCreated` descending
3. agent ascending
4. title ascending (case-insensitive)

Provider errors are returned alongside partial results as
`<provider>: <error>` and printed as stderr warnings; they never abort the
picker. A provider panic is recovered and reported the same way.

## How it works

```
cmd/asm/main.go          CLI: pick (default), list, doctor, init, version
internal/provider/
  provider.go            Session/Preview types + Provider, Previewer, Diagnoser
  opencode.go            read-only SQLite reader (session_v2 + legacy)
  claude.go              tolerant JSONL scanner
internal/merge/          concurrent query, dedupe, sort, gone flags
internal/tui/            Bubble Tea list, one-line delegate, preview pane
testdata/claude/         synthetic Claude transcripts used by tests
```

Flow:

1. `main` constructs the opencode and Claude providers.
2. `merge.Sessions` queries them concurrently and produces one sorted list.
3. The TUI renders on `/dev/tty`; previews are requested for the highlighted
   session and cached by `agent + id`.
4. On Enter the TUI returns the selection to `main`.
5. `main` prints the tab-separated result on stdout; the zsh wrapper performs
   the `cd` and agent launch.

The picker is flat by design: one row per top-level session, with fuzzy search
instead of a folder tree. No server API polling is used; last-updated sorting is
enough for the practical "what was I doing?" case.

## Configuration

| Variable | Effect |
| --- | --- |
| `ASM_OPENCODE_DB` | Override the opencode SQLite path (tests, alternate installs). |
| `ASM_CLAUDE_PROJECTS` | Override the Claude projects root. |
| `XDG_DATA_HOME` | Used for the default opencode path (`$XDG_DATA_HOME/opencode/opencode.db`). |
| `CLAUDE_CONFIG_DIR` | Used for the default Claude path (`$CLAUDE_CONFIG_DIR/projects`). |
| `NO_COLOR` | Standard no-color switch; lipgloss honors it. |

No configuration file is required.

## Project layout

```
agent-session-manager/
├── cmd/asm/                 # CLI entrypoint + CLI tests
├── internal/
│   ├── provider/            # shared model, opencode + Claude providers, tests
│   ├── merge/               # merge/dedupe/sort, tests
│   └── tui/                 # Bubble Tea picker/delegate/preview, tests
├── testdata/claude/         # fixture transcripts (subagents included)
├── .github/workflows/ci.yml # gofmt, vet, race tests, build
├── PLAN.md                  # design + research notes
├── Makefile
├── go.mod / go.sum
├── LICENSE (MIT)
└── README.md
```

## Verification

Run the full local suite:

```sh
make fmt        # gofmt -w .
make vet        # go vet ./...
make test       # go test ./...
go test -race ./...
make build
```

CI (`.github/workflows/ci.yml`) runs on Ubuntu and enforces:

- `gofmt -l .` is empty
- `go vet ./...`
- `go test -race ./...`
- `go build ./cmd/asm`

Coverage includes:

- **opencode provider**: temp SQLite DBs covering v2 + legacy union, dedupe
  preference, empty v2 title fallback, subagent/archived exclusion, legacy-only
  schema, missing DB, v2 and legacy previews, todos, env override.
- **Claude provider**: fixture transcripts covering custom-title vs summary vs
  first-user-message precedence, tool-result-only first turns, cwd recovery,
  munged-name fallback, malformed lines, sidechains, nested subagent exclusion,
  previews and missing storage.
- **merge**: concurrent interleaving, dedupe, gone flags, unavailable
  providers, partial results + errors, panic recovery, empty input.
- **TUI**: row rendering at 80×24, fuzzy typing, Enter selection, Esc/Ctrl-C
  cancel, relative-time formatting, preview pane content, empty list.
- **CLI**: version/init/unknown command, table and JSON output against a
  fixture DB, no-session behavior, `doctor`, relative-age formatting, cell
  sanitizing.

Manual verification performed against the live environment:

- `asm list` read the real opencode DB and produced **110 merged top-level
  sessions** (108 usable v2 + 2 legacy-only at the time; counts drift as new
  sessions are created).
- `asm doctor` reported both DB tables, `session_message`, excluded
  subagent/archived counts, 11 missing directories, and the absent Claude store.
- A PTY test confirmed Enter prints exactly
  `<directory>\t<agent>\t<session-id>` to stdout while the UI stays on
  `/dev/tty`, and Esc/Ctrl-C exit `130` with empty stdout.
- Fuzzy filtering was exercised (`mtg` → 37 matches) and Esc cleared the filter
  before quitting on the second press.
- The full zsh wrapper flow was verified with a stand-in `opencode` binary: the
  shell `cd`-ed into the selected session directory and invoked
  `opencode -s <session-id>`.

## Status and implementation notes

- All milestones from [PLAN.md](./PLAN.md) (M0–M5) are implemented: repo
  scaffold, opencode provider/list, TUI, zsh wrapper/E2E, Claude provider, and
  polish (previews, relative times, colors, README, CI).
- **opencode message storage changed during its v1→v2 migration**: current
  `session_v2` sessions store messages in `session_message`; only legacy
  `session` rows use `message` + `part`. Both paths are handled.
- **The Claude provider has no real sessions on the development machine yet**.
  It is fixture-tested end-to-end against synthetic JSONL; the first real Claude
  session should be a quick smoke test. This does not affect opencode users.
- **Go 1.26 is required** because the current `modernc.org/sqlite` ->
  `golang.org/x/sys` dependency requires it.
- Previews are loaded synchronously but cached per session. This is instant for
  opencode; a very large Claude transcript could take a few milliseconds on
  first selection.
- The repository is initialized with `origin` pointing at
  `git@github.com:jkuhn12/agent-session-manager.git` (SSH). Authenticate your
  SSH key with GitHub before pushing.

## Limitations

- Archived and subagent sessions are deliberately hidden.
- Claude transcripts deleted by Claude's cleanup period cannot be listed.
- `gone` directories are still selectable so the list reflects history; the
  wrapper's `cd` fails visibly and does not launch the agent.
- The picker does not detect which sessions are currently open/attached in a
  running agent; sorting by last update is the proxy.
- Creating new sessions, deleting/archiving sessions, and full transcript
  previews are out of scope.
- The provided wrapper is zsh-specific. Other shells work if they consume the
  stdout protocol manually.
- Paths containing tabs or newlines would break the line-oriented result
  protocol (not expected for normal project paths).
- Windows is not a target (`/dev/tty`, zsh-oriented wrapper).

## Troubleshooting

**`asm: no sessions found`**
Run `asm doctor`. Confirm `opencode.db` exists at the reported path and that
`~/.claude/projects` (or the configured root) contains session JSONL files. An
absent Claude store is normal until you use Claude Code.

**The picker works but the shell does not `cd`**
Make sure you are calling the zsh **function** from `asm init`, not launching the
binary directly. The function must call `command asm "$@"` so stdout can be
captured. Reload zsh after editing `~/.zshrc`.

**`asm version` / `asm list` opens the picker**
Your wrapper is stale. The original snippet always called `asm pick` regardless
of arguments. Replace the `asm()` block in `~/.zshrc` with the output of
`asm init` (the current version passes subcommands through), then `exec zsh`.

**The list looks cramped or shows awkward truncation**
Resize the terminal wider, or hide the preview with `asm --no-preview`. Rows
show a compact `~/…` path; the full path is still used for `cd` and is visible
in `asm list`, `asm doctor` and the preview pane.

**`asm` resolves to the wrong binary**
Check `type asm` and `command -v asm`. Ensure `$(go env GOPATH)/bin` precedes
other paths, or use an absolute path in the wrapper.

**opencode is running and the DB is busy**
The provider opens read-only with a WAL-safe connection and a 5s busy timeout.
Transient locks retry; persistent failures appear in `asm doctor` and as stderr
warnings while the remaining sessions are still listed.

**A session resumes in the wrong directory**
The wrapper always `cd`s into the stored session directory before launching.
If the recorded directory is stale, you will see a `cd` failure rather than a
resume elsewhere. `asm list` shows the full stored directory.

**No colors / odd rendering**
Set `NO_COLOR=1`. The TUI needs a real terminal; use `asm list` in scripts.

**Claude sessions disappeared**
Claude Code deletes transcripts after `cleanupPeriodDays` (default ~30 days).
`asm` cannot recover them.

## Development

```sh
make fmt      # gofmt
make vet      # go vet
make test     # go test ./...
make build    # ./asm
make install  # go install with version stamp
```

Version stamping:

```sh
make build VERSION=1.2.3
# or
go build -ldflags "-X main.version=1.2.3" -o asm ./cmd/asm
```

When adding a provider, implement `provider.Provider` (and optionally
`provider.Previewer` and `provider.Diagnoser`), then register it in
`newProviders` in `cmd/asm/main.go` and in `previewers`. The merge layer,
TUI and CLI need no changes.

Publishing from a checkout:

```sh
git remote -v                       # origin -> git@github.com:jkuhn12/agent-session-manager.git
git push -u origin main             # SSH key must be registered with GitHub
```

If the remote is still HTTPS, switch it:

```sh
git remote set-url origin git@github.com:jkuhn12/agent-session-manager.git
```

## License

MIT © 2026 Jeremy Kuhn. See [LICENSE](./LICENSE).

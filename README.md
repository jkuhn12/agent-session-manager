# agent-session-manager (`asm`)

A tiny Go TUI that lists every opencode and Claude Code session across all
project folders, sorted by last activity, with fuzzy search. Pick a session,
and it `cd`s into the project and resumes the agent in the same terminal.

> Full design notes live in [PLAN.md](./PLAN.md).

## Install

```sh
go install github.com/jkuhn12/agent-session-manager/cmd/asm@latest
```

Or from a checkout:

```sh
make install
```

## Usage

```sh
asm          # interactive picker (same as `asm pick`)
asm list     # plain table, handy for scripts and debugging
asm list --json
asm doctor   # report where sessions were found and what could be read
asm init     # print the zsh wrapper function
```

## zsh wrapper

A child process cannot `cd` the parent shell, so `asm` prints the selection on
stdout and a small shell function does the `cd` + resume. Add the output of
`asm init` to your `~/.zshrc`:

```zsh
asm() {
  local sel dir agent sid
  sel=$(command asm pick) || return
  IFS=$'\t' read -r dir agent sid <<< "$sel"
  [[ -n "$dir" ]] || return
  cd "$dir" || return
  case "$agent" in
    opencode) opencode -s "$sid" ;;
    claude)   claude --resume "$sid" ;;
  esac
}
```

The agent runs as a child process, so quitting it drops you back into a shell
already in the project directory.

Optional ghostty keybind (new split running the picker):

```conf
keybind = cmd+g=new_split:zsh -ic asm
```

## Keys

| Key | Action |
| --- | --- |
| type | fuzzy filter across folder, title, path and agent |
| ↑ / ↓ | move |
| Enter | select, `cd` and resume |
| Esc | clear filter, then quit |
| Ctrl-C | quit |

## Notes

- opencode sessions are read read-only from `~/.local/share/opencode/opencode.db`
  (or `$XDG_DATA_HOME/opencode/opencode.db`), covering both the legacy `session`
  and current `session_v2` tables.
- Claude Code sessions are read from `~/.claude/projects` (or
  `$CLAUDE_CONFIG_DIR/projects`). Sessions deleted by Claude's 30-day cleanup
  simply won't appear.
- Directories that no longer exist are still listed with a `gone` badge. They
  stay selectable so you can see what is missing; the wrapper's `cd` fails
  loudly rather than silently resuming somewhere wrong.
- Set `ASM_OPENCODE_DB` or `ASM_CLAUDE_PROJECTS` to point at non-default
  locations (useful for tests and alternate installs).

## Development

```sh
make test
make vet
make build
```

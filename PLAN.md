# agent-session-manager (`asm`) — Implementation Plan

**Date:** 2026-11-08
**Repo:** git@github.com:jkuhn12/agent-session-manager.git (exists on GitHub, currently empty)
**Local folder:** `~/git/agent-session-manager` (currently empty, not yet a git repo)

## Goal

A small Go TUI + zsh wrapper that lists **all opencode and Claude Code sessions across every project folder**, sorted by last activity, with fuzzy search. Selecting a session `cd`s into its repo folder and resumes it with the right agent. Solves the "laptop restarted, I forgot which sessions I had open and what state they were in" problem.

## Confirmed research findings (verified on this machine)

### OpenCode (v2.0.31)
- Sessions live in SQLite: `~/.local/share/opencode/opencode.db` (respect `$XDG_DATA_HOME`, fallback `~/.local/share/opencode`).
- **Two session tables** (schema is mid-migration):
  - `session` — legacy, 95 rows, titles like "New session - 2026-10-30T..."
  - `session_v2` — current, 162 rows, real auto-generated titles (e.g. "Agent session manager research and implementation plan")
  - Overlap: 2 ids only in `session`, 69 only in `session_v2` → **query both, dedupe by id, prefer v2 row**.
- Key columns: `id` (`ses_...`), `directory` (absolute project path), `title`, `time_created` / `time_updated` (epoch ms), `parent_id` (NULL = top-level; non-NULL = subagent/sub-sessions, exclude), `time_archived` (exclude non-NULL).
- Resume: `opencode --session <id>` / `opencode -s <id>` (also `-c` = most recent). Always run from the session's own directory.
- 95+ sessions across 18 directories exist today; several directories no longer exist on disk (`yawa-weather`, `rust-calculator`, `AX Tracker` with a space, etc.) → mark "gone" and still list them.
- Bonus data: legacy `message` + `part` tables and the current `session_message` table contain message text (last-user-message previews), `todo` table has open todos, session row has `model`/`cost`/token counts. Note: `session_v2` sessions store messages in `session_message`; only legacy `session` rows use `message`/`part`.

### Claude Code (v2.0.37 installed, no sessions yet)
- Sessions are JSONL files: `~/.claude/projects/<munged-path>/<session-uuid>.jsonl`, munged path = cwd with `/` → `-` (munged name is ambiguous — e.g. `AX-Tracker` vs `AX Tracker` — so recover the real cwd by reading the `cwd` field from a line in the file).
- Each line: `type` (`user` / `assistant` / `system` / `summary` / `custom-title` / ...), `sessionId`, `cwd`, ISO-8601 `timestamp`, `gitBranch`, `isSidechain`. Subagents live in sibling `<session-id>/subagents/*.jsonl` — exclude those.
- Title: use `custom-title` / `summary` lines if present, else first user text message (truncated), else session start date.
- Last activity: file mtime is sufficient (JSONL is append-per-turn).
- Resume: `claude --resume <sessionId>` / `claude -r <id>` (also `-c` = most recent, `--fork-session` to fork). Run from the session's project directory.
- Caveat: Claude Code deletes transcripts after ~30 days by default (`cleanupPeriodDays` setting) — the tool just reflects what's on disk.
- **Untestable end-to-end until the user creates a real claude session** — cover with fixture tests, smoke-test later.

### The `cd` constraint (important)
A child process cannot `cd` its parent shell. Standard solution (zoxide/fzf pattern): the TUI binary prints the selection as a machine-readable line to stdout; a small **zsh wrapper function** parses it, `cd`s, and launches the agent. Works in ghostty + zsh exactly as needed. Run the agent as a child (not `exec`) so exiting the agent drops you into a shell already cd'd into the repo.

## Decisions (confirmed with user)

| Decision | Choice |
|---|---|
| Language / stack | **Go + Bubble Tea** (+ Lip Gloss, bubbles; `modernc.org/sqlite` pure-Go driver) |
| Providers | **Both opencode + Claude Code now** (claude fixture-tested; smoke-tested when a real session exists) |
| After select | **cd + auto-resume** in the same terminal (agent runs as child process, shell survives) |
| Layout | **Flat list**, one row per top-level session, sorted by last-updated desc, fuzzy filter across folder + title |
| Scope | **Everything found in session storage** (all directories incl. `~/git` itself and `~`; missing folders get a "gone" badge) |
| Binary name | `asm` (repo: agent-session-manager; trivial to rename if it ever collides) |

## Architecture

```
~/git/agent-session-manager/
├── cmd/asm/main.go            # entrypoint, subcommands: pick (default), list, doctor, init
├── internal/provider/
│   ├── provider.go            # Provider interface + Session struct
│   ├── opencode.go            # SQLite reader (read-only, WAL-safe)
│   └── claude.go              # JSONL scanner
├── internal/tui/              # Bubble Tea picker model + styles
├── internal/merge/            # merge, dedupe, sort providers' results
├── testdata/                  # fixture DB + fixture claude jsonl files
├── go.mod / Makefile / README.md / LICENSE (MIT) / .gitignore
```

### Provider interface
```go
type Session struct {
    ID          string    // "ses_..." or claude UUID
    Agent       string    // "opencode" | "claude"
    Directory   string    // absolute project path
    Title       string
    TimeCreated time.Time
    TimeUpdated time.Time // sort key
    DirExists   bool
}

type Provider interface {
    Name() string
    Available() bool          // storage present?
    Sessions() ([]Session, error)  // top-level, non-archived only
}
```
- One provider failing/missing must never kill the picker — degrade and report in `asm doctor`.
- Claude provider reads titles/cwd **lazily** (only for rows visible/selected) so scanning hundreds of files stays instant; mtime + stat for the initial list.

### Result protocol (TUI → wrapper)
- TUI renders on **/dev/tty** (fzf pattern, keeps stdout clean), prints on selection: `<directory>\t<agent>\t<session-id>` (tab-separated survives spaces like `AX Tracker`).
- Cancel (Esc/Ctrl-C): print nothing, exit non-zero.

### zsh wrapper (installed via `asm init` which prints the snippet; README explains)
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
# optional keybind: bindkey -s '^G' 'asm^M'
```
- Ghostty needs no special config (runs in whatever split/tab the shell is in); optionally document `keybind = cmd+g:new_split:zsh -ic asm` for a dedicated picker keybind.

### TUI details
- One row: `[oc]/[cc] badge · folder name (dim path) · title · relative time (2h, 3d)`; missing dir → dim row + `gone` badge (still selectable; wrapper `cd` will fail loudly — acceptable).
- `bubbles/list` with built-in fuzzy filtering; ↑/↓ navigate, type to filter, Enter select, Esc quit.
- Preview pane (stretch goal in M5, data is available): last user message snippet, message count, model, cost, created vs updated.

## Milestones

- **M0 — Repo setup**
  - `git init`, `git remote add origin git@github.com:jkuhn12/agent-session-manager.git`, branch `main`.
  - Copy this plan into the repo as `PLAN.md` (user wants it in the repo folder).
  - Scaffold: `go.mod` (module `github.com/jkuhn12/agent-session-manager`), dirs, `.gitignore`, `LICENSE` (MIT), `Makefile`, README stub. First commit + push.
- **M1 — opencode provider + `asm list`**
  - Read-only SQLite (`file:...?mode=ro`; WAL allows concurrent reads with the running opencode server).
  - UNION of `session_v2` + legacy `session` (ids not in v2), exclude `parent_id != NULL` and archived, prefer v2 titles.
  - Defensive: if `session_v2` missing, legacy only; surface errors via doctor, never panic.
  - `asm list` prints a table (and `--json`) — test against the real DB (95+ sessions).
- **M2 — TUI picker**
  - Bubble Tea flat list as specified, /dev/tty rendering, result protocol on stdout.
- **M3 — Shell wrapper + end-to-end (opencode)**
  - `asm init` snippet, wrapper function, manual E2E: pick a session from another directory → cd + `opencode -s <id>` resumes in that terminal.
- **M4 — Claude Code provider**
  - JSONL scan under `~/.claude/projects/`, exclude `subagents/`, mtime sort, lazy cwd/title extraction, merged into the list by the same sort.
  - Fixture tests (synthetic jsonl files in `testdata/`). Smoke test when the user creates their first real claude session.
- **M5 — Polish**
  - Preview pane (last user message / todos / cost), relative-time formatting, colors, README with usage + demo GIF, optional GitHub Actions (build + test).

## Testing strategy
- **Unit:** provider tests against `testdata` fixtures (tiny synthetic SQLite DB built in-test via SQL; sample claude jsonl lines incl. `custom-title`, `summary`, tool-result-only user lines, sidechain files to exclude).
- **Merge tests:** dedupe, interleave sort, missing-dir flagging.
- **Integration (manual):** real `opencode.db` via `asm list`; full wrapper flow in ghostty/zsh; resume this very session as the acceptance test.
- **Claude:** fixture tests now; smoke later (cannot create claude sessions without spending the user's quota).

## Risks & mitigations
- **opencode schema drift** (v1→v2 tables observed; v3 possible): defensive table detection, `asm doctor` reports what it found; queries per-table, errors degrade not crash.
- **DB locked by running server:** read-only + WAL = concurrent readers OK; add a short retry on `SQLITE_BUSY`.
- **TUI stdout pollution:** render to /dev/tty, result only on stdout (fzf pattern).
- **Claude format drift:** format is undocumented-but-stable per current docs; parser tolerant of unknown line types (only reads known fields).
- **Huge session counts:** stat-first listing, lazy detail loading; current machine has ~160 sessions — trivial, but design for 1000s.
- **Directories that no longer exist:** listed with `gone` badge; selection still allowed, wrapper `cd` fails visibly rather than silently.
- **Resume from wrong directory:** wrapper always `cd`s into the session's directory *before* invoking the agent (the tested path for both `opencode -s` and `claude --resume`).

## Out of scope / future ideas
- Detecting *currently open/attached* sessions (would need opencode server API polling) — last-updated sorting already covers the practical need.
- Creating new sessions from the picker (`n` key = cd + start fresh agent), deleting/archiving sessions, session previews of full transcripts.
- Homebrew tap distribution (`go install ...@latest` works from day one).

## Implementation status (2026-11-08)

All milestones M0–M5 are implemented:

- `asm pick` (default), `asm list [--json]`, `asm doctor`, `asm init`, `asm version`.
- opencode provider reads both `session_v2` and legacy `session`, dedupes by id,
  excludes subagents/archived rows, and tolerates missing tables/columns.
  Previews use `session_message` (v2) or `message`/`part` (legacy).
- Claude provider scans `<projects>/<project>/*.jsonl`, skips nested
  `subagents/`, derives cwd from transcript lines (munged-name fallback), and
  applies custom-title → summary → first-user-message title precedence.
- Flat Bubble Tea list with fuzzy filtering, relative times, `gone` badges and
  a cached preview pane; renders on `/dev/tty`, prints only
  `<directory>\t<agent>\t<session-id>` on stdout.
- zsh wrapper via `asm init`; verified end-to-end with a stand-in agent binary
  (`cd` into the session directory, then `opencode -s <id>`).
- Unit tests for both providers, merge, TUI and CLI; GitHub Actions runs
  gofmt/vet/race tests/build.

Known limitations at this date:
- No real Claude Code sessions exist on this machine yet, so the provider is
  fixture-tested only; the first real session should be a quick smoke test.
- The preview pane calls the provider synchronously on selection (cached per
  session). This is instant for opencode; a huge Claude transcript could take a
  few milliseconds.
- `asm` is currently built/tested with Go 1.26 because modernc's latest
  `x/sys` dependency requires it.

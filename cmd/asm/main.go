// Command asm lists opencode and Claude Code sessions across every project
// folder and resumes the one you pick.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jkuhn12/agent-session-manager/internal/merge"
	"github.com/jkuhn12/agent-session-manager/internal/provider"
	"github.com/jkuhn12/agent-session-manager/internal/tui"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const (
	usageText = `asm — pick and resume opencode / Claude Code sessions

Usage:
  asm [pick] [flags]      interactive picker (default)
  asm list [--json]       print sessions, newest first
  asm doctor              show where sessions are read from
  asm init                print the zsh wrapper function
  asm version             print the version

Picker flags:
  --no-preview            disable the detail preview pane
  --title string          override the picker header

The picker prints "<directory>\t<agent>\t<session-id>" on selection. Install
the zsh wrapper from "asm init" so the shell can cd and launch the agent.`

	zshSnippet = `# Add this to ~/.zshrc, then run: exec zsh
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
# optional grep-able keybind for zsh: bindkey -s '^G' 'asm^M'

# Optional ghostty keybind (new split running the picker), in
# ~/.config/ghostty/config:
# keybind = cmd+g=new_split:zsh -ic asm`
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entrypoint. It returns a process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runPick(nil, stdout, stderr)
	}

	switch args[0] {
	case "pick":
		return runPick(args[1:], stdout, stderr)
	case "list", "ls":
		return runList(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "asm %s\n", version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, usageText)
		return 0
	default:
		if strings.HasPrefix(args[0], "-") {
			// Allow `asm --no-preview` as shorthand for the default command.
			return runPick(args, stdout, stderr)
		}
		fmt.Fprintf(stderr, "asm: unknown command %q\n\n%s\n", args[0], usageText)
		return 2
	}
}

func newProviders() []provider.Provider {
	return []provider.Provider{
		provider.NewOpenCode(),
		provider.NewClaude(),
	}
}

func collectSessions(stderr io.Writer) []provider.Session {
	sessions, errs := merge.Sessions(newProviders())
	for _, err := range errs {
		fmt.Fprintf(stderr, "asm: warning: %v\n", err)
	}
	return sessions
}

func previewers() map[string]provider.Previewer {
	return map[string]provider.Previewer{
		provider.AgentOpenCode: provider.NewOpenCode(),
		provider.AgentClaude:   provider.NewClaude(),
	}
}

func runPick(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pick", flag.ContinueOnError)
	fs.SetOutput(stderr)
	noPreview := fs.Bool("no-preview", false, "disable the detail preview pane")
	title := fs.String("title", "", "override the picker header")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, usageText)
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "asm pick: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	sessions := collectSessions(stderr)
	if len(sessions) == 0 {
		fmt.Fprintln(stderr, "asm: no sessions found (is opencode or Claude Code installed?)")
		return 1
	}

	options := tui.Options{Title: *title}
	if !*noPreview {
		lookup := previewers()
		options.Preview = func(session provider.Session) provider.Preview {
			previewer, ok := lookup[session.Agent]
			if !ok {
				return provider.Preview{}
			}
			preview, err := previewer.Preview(session.ID)
			if err != nil {
				return provider.Preview{}
			}
			return preview
		}
	}

	selection, err := tui.Run(sessions, options)
	if err != nil {
		if errors.Is(err, tui.ErrCanceled) {
			return 130
		}
		fmt.Fprintf(stderr, "asm: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "%s\t%s\t%s\n", selection.Directory, selection.Agent, selection.SessionID)
	return 0
}

func runList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON instead of a table")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, usageText)
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "asm list: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	sessions := collectSessions(stderr)

	if *asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(sessions); err != nil {
			fmt.Fprintf(stderr, "asm: encode sessions: %v\n", err)
			return 1
		}
		return 0
	}

	if len(sessions) == 0 {
		fmt.Fprintln(stderr, "asm: no sessions found")
		return 0
	}

	now := time.Now()
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tUPDATED\tFOLDER\tTITLE\tDIRECTORY")
	for _, session := range sessions {
		status := ""
		if !session.DirExists {
			status = "  (gone)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s%s\n",
			agentLabel(session.Agent),
			humanAge(now, session.TimeUpdated),
			sanitizeCell(session.Folder()),
			sanitizeCell(provider.Truncate(session.Title, 70)),
			sanitizeCell(session.Directory),
			status,
		)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintf(stderr, "asm: write table: %v\n", err)
		return 1
	}
	return 0
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, usageText)
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "asm doctor: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	fmt.Fprintf(stdout, "asm %s (%s/%s, %s)\n\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())

	printDiagnostic(stdout, "info", "opencode binary", foundString(provider.CommandExists("opencode")))
	printDiagnostic(stdout, "info", "claude binary", foundString(provider.CommandExists("claude")))
	fmt.Fprintln(stdout)

	providers := newProviders()
	for i, p := range providers {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		fmt.Fprintf(stdout, "%s provider\n", p.Name())
		diagnoser, ok := p.(provider.Diagnoser)
		if !ok {
			printDiagnostic(stdout, "info", "diagnostics", "not supported")
			continue
		}
		diagnostics := diagnoser.Diagnostics()
		if len(diagnostics) == 0 {
			printDiagnostic(stdout, "info", "diagnostics", "none")
		}
		for _, diagnostic := range diagnostics {
			printDiagnostic(stdout, diagnostic.Level, diagnostic.Key, diagnostic.Value)
		}
	}

	sessions, errs := merge.Sessions(providers)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "merged")
	printDiagnostic(stdout, "info", "sessions", fmt.Sprintf("%d", len(sessions)))
	missing := make([]string, 0)
	seenMissing := make(map[string]bool)
	for _, session := range sessions {
		if !session.DirExists && !seenMissing[session.Directory] {
			seenMissing[session.Directory] = true
			missing = append(missing, session.Directory)
		}
	}
	printDiagnostic(stdout, "info", "missing directories", fmt.Sprintf("%d", len(missing)))
	for i, dir := range missing {
		if i >= 5 {
			printDiagnostic(stdout, "info", "…", fmt.Sprintf("%d more", len(missing)-i))
			break
		}
		printDiagnostic(stdout, "warn", "gone", dir)
	}
	for _, err := range errs {
		printDiagnostic(stdout, "error", "provider", err.Error())
	}
	return 0
}

func runInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, usageText)
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "asm init: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	fmt.Fprintln(stdout, zshSnippet)
	return 0
}

func printDiagnostic(w io.Writer, level, key, value string) {
	if value == "" {
		value = "-"
	}
	fmt.Fprintf(w, "  %-5s %-34s %s\n", level, key, value)
}

func foundString(found bool) string {
	if found {
		return "found"
	}
	return "not found"
}

func agentLabel(agent string) string {
	switch agent {
	case provider.AgentOpenCode:
		return "opencode"
	case provider.AgentClaude:
		return "claude"
	default:
		return agent
	}
}

// humanAge renders a compact relative age such as "5m" or "3d".
func humanAge(now, then time.Time) string {
	if then.IsZero() {
		return "-"
	}
	delta := now.Sub(then)
	if delta < 0 {
		delta = 0
	}
	switch {
	case delta < time.Minute:
		return "just now"
	case delta < time.Hour:
		return fmt.Sprintf("%dm", int(delta/time.Minute))
	case delta < 24*time.Hour:
		return fmt.Sprintf("%dh", int(delta/time.Hour))
	case delta < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(delta/(24*time.Hour)))
	case delta < 30*24*time.Hour:
		return fmt.Sprintf("%dw", int(delta/(7*24*time.Hour)))
	case delta < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(delta/(30*24*time.Hour)))
	default:
		return fmt.Sprintf("%dy", int(delta/(365*24*time.Hour)))
	}
}

// sanitizeCell strips tabs/newlines so a path or title cannot break the table.
func sanitizeCell(value string) string {
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}

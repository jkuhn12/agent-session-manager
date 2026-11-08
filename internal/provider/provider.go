// Package provider defines the common session model shared by every agent
// integration (opencode, Claude Code) and the interfaces the picker consumes.
package provider

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const (
	AgentOpenCode = "opencode"
	AgentClaude   = "claude"
)

// Session is one resumable, top-level agent session.
//
// Providers populate everything except DirExists; merge.Sessions checks the
// filesystem once after collecting results so each provider does not have to.
type Session struct {
	ID          string    `json:"id"`
	Agent       string    `json:"agent"`
	Directory   string    `json:"directory"`
	Title       string    `json:"title"`
	TimeCreated time.Time `json:"time_created"`
	TimeUpdated time.Time `json:"time_updated"`
	DirExists   bool      `json:"dir_exists"`
}

// Folder returns the last path element of the session directory, falling back
// to the full path for roots.
func (s Session) Folder() string {
	if s.Directory == "" {
		return "?"
	}
	base := filepath.Base(s.Directory)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return s.Directory
	}
	return base
}

// Preview is optional detail shown for the highlighted session. Every field is
// best-effort: providers may return a zero Preview with a nil error when the
// underlying store does not expose that information.
type Preview struct {
	LastUserMessage string  `json:"last_user_message,omitempty"`
	MessageCount    int     `json:"message_count"`
	Model           string  `json:"model,omitempty"`
	CostUSD         float64 `json:"cost_usd,omitempty"`
	TodosOpen       int     `json:"todos_open,omitempty"`
	TodosTotal      int     `json:"todos_total,omitempty"`
}

// Provider loads top-level, non-archived sessions from one agent's storage.
type Provider interface {
	// Name is the stable agent identifier used in the result protocol.
	Name() string
	// Available reports whether the agent's storage location exists.
	Available() bool
	// Sessions returns top-level, non-archived sessions. Implementations may
	// return partial results together with an error.
	Sessions() ([]Session, error)
}

// Previewer is implemented by providers that can load detail for one session.
// It is intentionally separate from Provider: the list path must stay cheap.
type Previewer interface {
	Preview(sessionID string) (Preview, error)
}

// Diagnostic is a single human-readable fact reported by `asm doctor`.
type Diagnostic struct {
	Level string // "ok", "info", "warn" or "error"
	Key   string
	Value string
}

// Diagnoser is implemented by providers that can describe their storage.
type Diagnoser interface {
	Diagnostics() []Diagnostic
}

// HomeDir returns the current user's home directory, with a $HOME fallback.
func HomeDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return os.Getenv("HOME")
}

// ExpandHome expands a leading "~" or "~/" in path.
func ExpandHome(path string) string {
	switch {
	case path == "~":
		return HomeDir()
	case strings.HasPrefix(path, "~/"):
		return filepath.Join(HomeDir(), path[2:])
	default:
		return path
	}
}

// DataHome returns $XDG_DATA_HOME or ~/.local/share.
func DataHome() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(HomeDir(), ".local", "share")
}

// DirExists reports whether path exists and is a directory.
func DirExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// FileExists reports whether path exists and is a regular file.
func FileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// CommandExists reports whether name can be found on $PATH.
func CommandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// EnvOr returns the environment variable name, or fallback when it is unset.
func EnvOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// CleanTitle collapses whitespace and trims a session title.
func CleanTitle(title string) string {
	return strings.Join(strings.Fields(title), " ")
}

// Truncate collapses whitespace and shortens s to at most max runes, adding an
// ellipsis when something was cut.
func Truncate(s string, max int) string {
	s = CleanTitle(s)
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return strings.TrimRightFunc(string(runes[:max-1]), unicode.IsSpace) + "…"
}

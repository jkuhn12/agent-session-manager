package tui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jkuhn12/agent-session-manager/internal/provider"
)

// SessionItem adapts a provider.Session to list.Item.
type SessionItem struct {
	provider.Session
}

// Title returns the session title, or a placeholder for untitled sessions.
func (i SessionItem) Title() string {
	if title := provider.CleanTitle(i.Session.Title); title != "" {
		return title
	}
	return "Untitled session"
}

// Description returns the full project directory.
func (i SessionItem) Description() string {
	if i.Directory == "" {
		return "?"
	}
	return i.Directory
}

// FilterValue makes every searchable field part of the fuzzy match.
func (i SessionItem) FilterValue() string {
	return i.Folder() + " " + i.Title() + " " + i.Directory + " " + i.Agent
}

// rowStyles holds the styles used by the single-line delegate. Styles contain
// no hardcoded ANSI escapes; lipgloss honours NO_COLOR and terminal profiles.
type rowStyles struct {
	badgeOpen   lipgloss.Style
	badgeClaude lipgloss.Style
	badgeOther  lipgloss.Style

	folder lipgloss.Style
	path   lipgloss.Style
	title  lipgloss.Style
	sep    lipgloss.Style
	time   lipgloss.Style
	gone   lipgloss.Style

	normal   lipgloss.Style
	selected lipgloss.Style
}

var dimColor = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#8B8B8B"}

func newRowStyles() rowStyles {
	badge := lipgloss.NewStyle().Bold(true)
	return rowStyles{
		badgeOpen:   badge.Foreground(lipgloss.AdaptiveColor{Light: "#0B7285", Dark: "#63E6BE"}),
		badgeClaude: badge.Foreground(lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FFB86C"}),
		badgeOther:  badge.Foreground(dimColor),
		folder:      lipgloss.NewStyle().Bold(true),
		path:        lipgloss.NewStyle().Foreground(dimColor),
		title:       lipgloss.NewStyle(),
		sep:         lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#5C5C5C"}),
		time:        lipgloss.NewStyle().Foreground(dimColor),
		gone:        lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#E0AF68"}),
		normal:      lipgloss.NewStyle(),
		selected: lipgloss.NewStyle().
			Background(lipgloss.AdaptiveColor{Light: "#D6E4FF", Dark: "#1F3A5F"}).
			Foreground(lipgloss.AdaptiveColor{Light: "#0B1F33", Dark: "#F2F2F2"}).
			Bold(true),
	}
}

// itemDelegate renders one session per row.
type itemDelegate struct {
	styles rowStyles
}

func newItemDelegate() itemDelegate {
	return itemDelegate{styles: newRowStyles()}
}

func (d itemDelegate) Height() int  { return 1 }
func (d itemDelegate) Spacing() int { return 0 }

func (d itemDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

// Render draws a single row, never wider than the list.
func (d itemDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	sessionItem, ok := item.(SessionItem)
	if !ok {
		return
	}

	width := m.Width()
	if width <= 0 {
		return
	}

	// Keep a small gutter so the highlight reads as a row, shrinking it away
	// on very narrow terminals.
	pad := 1
	contentWidth := width - 2*pad
	if contentWidth < 12 {
		pad = 0
		contentWidth = width
	}
	if contentWidth <= 0 {
		return
	}

	plain, styled := d.buildRow(sessionItem.Session, contentWidth)
	if index == m.Index() {
		fmt.Fprint(w, d.styles.selected.Width(width).Padding(0, pad).Render(plain)) //nolint:errcheck
		return
	}
	fmt.Fprint(w, d.styles.normal.Width(width).Padding(0, pad).Render(styled)) //nolint:errcheck
}

// buildRow returns both a plain and a styled version of the row. The plain
// version is used for the selected row so the highlight background styles the
// whole line (nested ANSI resets would punch holes in it otherwise).
func (d itemDelegate) buildRow(s provider.Session, width int) (plain, styled string) {
	badgePlain := "[" + agentBadge(s.Agent) + "]"
	badgeStyled := d.badgeStyle(s.Agent).Render(badgePlain)

	dirDisplay := compactHomePath(s.Directory)
	if dirDisplay == "" {
		dirDisplay = "?"
	}
	title := SessionItem{Session: s}.Title()

	rightPlain := relativeTime(time.Now(), s.TimeUpdated)
	rightStyled := ""
	if rightPlain != "" {
		rightStyled = d.styles.time.Render(rightPlain)
	}
	if !s.DirExists {
		goneStyled := d.styles.gone.Render("gone")
		if rightPlain != "" {
			rightPlain += " gone"
			rightStyled += " " + goneStyled
		} else {
			rightPlain = "gone"
			rightStyled = goneStyled
		}
	}
	rightWidth := lipgloss.Width(rightPlain)

	const separator = " · "
	separatorWidth := lipgloss.Width(separator)
	// badge + separator + directory + separator + gap + right side
	fixed := lipgloss.Width(badgePlain) + 2*separatorWidth + rightWidth + 1
	available := width - fixed

	if available < 10 {
		// Extremely narrow: keep the badge, the title and the right side.
		budget := width - lipgloss.Width(badgePlain) - separatorWidth - rightWidth - 1
		if budget < 1 {
			budget = 1
		}
		titlePlain := truncatePlain(title, budget)
		leftPlain := badgePlain + separator + titlePlain
		leftStyled := badgeStyled + d.styles.sep.Render(separator) + d.styles.title.Render(titlePlain)
		gap := max(1, width-lipgloss.Width(leftPlain)-rightWidth)
		return leftPlain + strings.Repeat(" ", gap) + rightPlain,
			leftStyled + strings.Repeat(" ", gap) + rightStyled
	}

	// Show a compact directory (~/git/project) instead of the folder plus the
	// full path, which duplicated most of the same text. Keep the whole
	// directory visible when possible, reserving at least 12 cells for title.
	dirWidth := lipgloss.Width(dirDisplay)
	if dirWidth > available-12 {
		dirWidth = max(8, available-12)
	}
	titleWidth := max(1, available-dirWidth)

	dirPlain := truncateLeftPlain(dirDisplay, dirWidth)
	titlePlain := truncatePlain(title, titleWidth)
	dirPrefix, dirBase := splitDirDisplay(dirPlain)

	leftPlain := badgePlain + separator + dirPlain + separator + titlePlain
	leftStyled := badgeStyled + d.styles.sep.Render(separator) +
		d.styles.path.Render(dirPrefix) + d.styles.folder.Render(dirBase) +
		d.styles.sep.Render(separator) + d.styles.title.Render(titlePlain)

	gap := width - lipgloss.Width(leftPlain) - rightWidth
	if gap < 1 {
		gap = 1
	}
	spaces := strings.Repeat(" ", gap)
	return leftPlain + spaces + rightPlain, leftStyled + spaces + rightStyled
}

// compactHomePath replaces the user's home prefix with "~" so rows stay short.
func compactHomePath(dir string) string {
	return provider.CompactHome(dir)
}

// splitDirDisplay splits a compact directory at the last slash so the parent
// can be dimmed while the project folder stays bold.
func splitDirDisplay(dir string) (prefix, base string) {
	if dir == "" {
		return "", ""
	}
	if dir == "~" {
		return "", "~"
	}
	index := strings.LastIndex(dir, "/")
	switch {
	case index < 0:
		return "", dir
	case index == 0:
		return "/", dir[1:]
	default:
		return dir[:index+1], dir[index+1:]
	}
}

func (d itemDelegate) badgeStyle(agent string) lipgloss.Style {
	switch agent {
	case provider.AgentOpenCode:
		return d.styles.badgeOpen
	case provider.AgentClaude:
		return d.styles.badgeClaude
	default:
		return d.styles.badgeOther
	}
}

func agentBadge(agent string) string {
	switch agent {
	case provider.AgentOpenCode:
		return "oc"
	case provider.AgentClaude:
		return "cc"
	default:
		runes := []rune(agent)
		if len(runes) >= 2 {
			return strings.ToLower(string(runes[:2]))
		}
		if len(runes) == 1 {
			return strings.ToLower(string(runes[0]))
		}
		return "??"
	}
}

// relativeTime formats how long ago t was, using the compact forms shown in
// the session list. A zero time produces an empty string.
func relativeTime(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	days := int(d / (24 * time.Hour))
	switch {
	case days < 7:
		return fmt.Sprintf("%dd", days)
	case days < 30:
		return fmt.Sprintf("%dw", days/7)
	case days < 365:
		return fmt.Sprintf("%dmo", days/30)
	default:
		return fmt.Sprintf("%dy", days/365)
	}
}

// truncatePlain shortens s to at most width terminal cells, adding an ellipsis.
// The input is expected to be plain text (styling is applied after truncation).
func truncatePlain(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}

	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > width-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return strings.TrimRight(b.String(), " ") + "…"
}

// truncateLeftPlain keeps the tail of s, which is more useful for long paths.
func truncateLeftPlain(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}

	runes := []rune(s)
	used := 0
	start := len(runes)
	for start > 0 {
		rw := lipgloss.Width(string(runes[start-1]))
		if used+rw > width-1 {
			break
		}
		used += rw
		start--
	}
	return "…" + string(runes[start:])
}

// splitPlain splits s into a prefix that fits width and the remainder.
func splitPlain(s string, width int) (string, string) {
	if width <= 0 {
		return "", s
	}
	runes := []rune(s)
	used := 0
	i := 0
	for i < len(runes) {
		rw := lipgloss.Width(string(runes[i]))
		if used+rw > width {
			if i == 0 {
				i++
			}
			break
		}
		used += rw
		i++
	}
	return string(runes[:i]), string(runes[i:])
}

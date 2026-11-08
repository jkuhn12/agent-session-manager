// Package tui implements the interactive session picker. It only renders to
// its terminal; callers are responsible for printing the returned selection.
package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jkuhn12/agent-session-manager/internal/provider"
)

// ErrCanceled is returned by Run when the user dismisses the picker.
var ErrCanceled = errors.New("selection canceled")

// Options configures the picker.
type Options struct {
	// Title is the header title; defaults to "Agent Sessions".
	Title string

	// Preview loads detail for the highlighted session. When nil the preview
	// pane is disabled.
	Preview func(provider.Session) provider.Preview
}

// Selection is the session chosen by the user.
type Selection struct {
	Directory string
	Agent     string
	SessionID string
}

const (
	defaultWidth  = 80
	defaultHeight = 24

	headerHeight      = 1
	footerHeight      = 1
	previewPaneHeight = 5
	// Only show the preview when there is still room for a useful list.
	minPreviewHeight = previewPaneHeight + headerHeight + footerHeight + 4
)

var (
	headerTitleStyle = lipgloss.NewStyle().Bold(true)
	headerMetaStyle  = lipgloss.NewStyle().Foreground(dimColor)
	footerStyle      = lipgloss.NewStyle().Foreground(dimColor)
	previewRuleStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#4D4D4D"})
	previewMsgStyle  = lipgloss.NewStyle()
	previewMetaStyle = lipgloss.NewStyle().Foreground(dimColor)
)

// Model is the outer Bubble Tea model. It owns the list and the final result.
type Model struct {
	sessions []provider.Session
	options  Options
	list     list.Model

	width         int
	height        int
	previewHeight int

	previewCache map[string]provider.Preview
	previewKey   string

	result   Selection
	selected bool
	canceled bool
}

// NewModel builds the picker model. It is exported so tests can drive it
// without opening a terminal.
func NewModel(sessions []provider.Session, options Options) Model {
	if options.Title == "" {
		options.Title = "Agent Sessions"
	}

	items := make([]list.Item, len(sessions))
	for i, session := range sessions {
		items[i] = SessionItem{Session: session}
	}

	l := list.New(items, newItemDelegate(), defaultWidth, defaultHeight)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowFilter(false)
	l.SetShowPagination(false)
	l.SetShowHelp(false)
	l.SetStatusBarItemName("session", "sessions")
	// The outer model owns all quit keybindings.
	l.DisableQuitKeybindings()

	m := Model{
		sessions:     sessions,
		options:      options,
		list:         l,
		width:        defaultWidth,
		height:       defaultHeight,
		previewCache: make(map[string]provider.Preview),
	}
	m.layout()
	m.refreshPreview()
	return m
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		m.refreshPreview()
		return m, nil

	case tea.KeyMsg:
		// Ctrl-C always cancels, even mid-filter.
		if msg.String() == "ctrl+c" {
			m.canceled = true
			return m, tea.Quit
		}

		if m.list.FilterState() == list.Filtering {
			switch msg.String() {
			case "esc":
				m.list.ResetFilter()
				m.refreshPreview()
				return m, nil
			case "enter":
				return m.selectCurrent()
			case "up":
				m.list.CursorUp()
				m.refreshPreview()
				return m, nil
			case "down":
				m.list.CursorDown()
				m.refreshPreview()
				return m, nil
			}
		} else {
			switch msg.String() {
			case "esc":
				if m.list.FilterState() == list.FilterApplied {
					// Esc first clears an accepted filter.
					m.list.ResetFilter()
					m.refreshPreview()
					return m, nil
				}
				m.canceled = true
				return m, tea.Quit
			case "enter":
				return m.selectCurrent()
			}

			// Typing starts (or extends) a filter, fzf style, instead of
			// requiring the "/" key.
			if isPrintableKey(msg) && len(m.sessions) > 0 {
				if m.list.FilterState() == list.Unfiltered {
					// Seed the filtered set synchronously so the first frame
					// does not flash an empty list.
					m.list.SetFilterText("")
				}
				m.list.SetFilterState(list.Filtering)
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	m.refreshPreview()
	return m, cmd
}

// View implements tea.Model.
func (m Model) View() string {
	width := max(1, m.width)

	parts := []string{
		m.renderHeader(width),
		m.list.View(),
	}
	if m.previewHeight > 0 {
		parts = append(parts, m.renderPreview(width))
	}
	parts = append(parts, m.renderFooter(width))
	return strings.Join(parts, "\n")
}

// Result reports the chosen session, if the user made one.
func (m Model) Result() (Selection, bool) {
	return m.result, m.selected
}

// Canceled reports whether the user dismissed the picker.
func (m Model) Canceled() bool {
	return m.canceled
}

// Run opens /dev/tty (falling back to stdin/stdout), runs the picker and
// returns the selection. It never prints the selection itself; stdout stays
// clean for the caller's result protocol.
func Run(sessions []provider.Session, options Options) (Selection, error) {
	model := NewModel(sessions, options)

	var programOptions []tea.ProgramOption
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		defer tty.Close() //nolint:errcheck
		programOptions = []tea.ProgramOption{
			tea.WithInput(tty),
			tea.WithOutput(tty),
			tea.WithAltScreen(),
		}
	} else {
		programOptions = []tea.ProgramOption{
			tea.WithInput(os.Stdin),
			tea.WithOutput(os.Stdout),
			tea.WithAltScreen(),
		}
	}

	final, err := tea.NewProgram(model, programOptions...).Run()
	if err != nil {
		return Selection{}, err
	}

	finalModel, ok := final.(Model)
	if !ok || finalModel.canceled {
		return Selection{}, ErrCanceled
	}
	if selection, ok := finalModel.Result(); ok {
		return selection, nil
	}
	return Selection{}, ErrCanceled
}

func (m *Model) layout() {
	width := m.width
	if width <= 0 {
		width = defaultWidth
	}
	height := m.height
	if height <= 0 {
		height = defaultHeight
	}
	m.width, m.height = width, height

	m.previewHeight = 0
	if m.options.Preview != nil && height >= minPreviewHeight {
		m.previewHeight = previewPaneHeight
	}

	listHeight := height - headerHeight - footerHeight - m.previewHeight
	if listHeight < 1 {
		m.previewHeight = 0
		listHeight = height - headerHeight - footerHeight
	}
	if listHeight < 1 {
		listHeight = 1
	}
	m.list.SetSize(width, listHeight)
}

func (m Model) selectCurrent() (tea.Model, tea.Cmd) {
	if item, ok := m.list.SelectedItem().(SessionItem); ok {
		m.result = Selection{
			Directory: item.Directory,
			Agent:     item.Agent,
			SessionID: item.ID,
		}
		m.selected = true
		return m, tea.Quit
	}
	m.canceled = true
	return m, tea.Quit
}

func (m Model) renderHeader(width int) string {
	title := truncatePlain(m.options.Title, width)
	titleWidth := lipgloss.Width(title)

	total := len(m.sessions)
	visible := len(m.list.VisibleItems())
	meta := ""
	if m.list.FilterState() != list.Unfiltered {
		meta = fmt.Sprintf("%d/%d filtered", visible, total)
		if query := strings.TrimSpace(m.list.FilterValue()); query != "" {
			meta += " · " + query
		}
	} else if total == 1 {
		meta = "1 session"
	} else {
		meta = fmt.Sprintf("%d sessions", total)
	}
	meta = truncatePlain(meta, max(0, width-titleWidth-2))

	if meta == "" {
		return headerTitleStyle.Render(title)
	}
	return headerTitleStyle.Render(title) + "  " + headerMetaStyle.Render(meta)
}

func (m Model) renderFooter(width int) string {
	var text string
	switch {
	case len(m.sessions) == 0:
		text = "enter/esc quit · ctrl+c quit"
	case m.list.FilterState() == list.Filtering:
		text = "↑/↓ move · enter select · esc clear filter · ctrl+c quit"
	case m.list.FilterState() == list.FilterApplied:
		text = "type to refine · enter select · esc clear filter · ctrl+c quit"
	default:
		text = "type to filter · ↑/↓ move · enter select · esc quit · ctrl+c quit"
	}
	return footerStyle.Render(truncatePlain(text, width))
}

func (m Model) renderPreview(width int) string {
	lines := make([]string, 0, m.previewHeight)

	rule := "─ preview "
	if fill := width - lipgloss.Width(rule); fill > 0 {
		rule += strings.Repeat("─", fill)
	}
	lines = append(lines, previewRuleStyle.Render(truncatePlain(rule, width)))

	session, ok := m.currentSession()
	if !ok {
		lines = append(lines, previewMetaStyle.Render("  No session selected"))
		return padPreview(lines, m.previewHeight)
	}

	key := previewKey(session)
	preview, cached := m.previewCache[key]
	if !cached && m.options.Preview != nil {
		preview = m.options.Preview(session)
		m.previewCache[key] = preview
	}

	textWidth := max(1, width-2)
	messageLines := wrapPlain(preview.LastUserMessage, textWidth, 2)
	if len(messageLines) == 0 {
		lines = append(lines, previewMetaStyle.Render("  No user messages yet"))
	} else {
		for _, line := range messageLines {
			lines = append(lines, previewMsgStyle.Render("  "+line))
		}
	}
	// The message always reserves two lines so the pane height is stable.
	for i := len(messageLines); i < 2 && len(lines) < m.previewHeight-2; i++ {
		lines = append(lines, "")
	}

	lines = append(lines, previewMetaStyle.Render("  "+truncatePlain(previewStats(preview), textWidth)))
	lines = append(lines, previewMetaStyle.Render("  "+truncatePlain(previewDates(session), textWidth)))

	return padPreview(lines, m.previewHeight)
}

func (m Model) currentSession() (provider.Session, bool) {
	item, ok := m.list.SelectedItem().(SessionItem)
	if !ok {
		return provider.Session{}, false
	}
	return item.Session, true
}

func (m *Model) refreshPreview() {
	if m.options.Preview == nil {
		m.previewKey = ""
		return
	}
	session, ok := m.currentSession()
	if !ok {
		m.previewKey = ""
		return
	}
	key := previewKey(session)
	if key == m.previewKey {
		return
	}
	m.previewKey = key
	if _, ok := m.previewCache[key]; !ok {
		m.previewCache[key] = m.options.Preview(session)
	}
}

func previewKey(session provider.Session) string {
	return session.Agent + "\x00" + session.ID
}

func previewStats(preview provider.Preview) string {
	messages := fmt.Sprintf("%d messages", preview.MessageCount)
	if preview.MessageCount == 1 {
		messages = "1 message"
	}
	parts := []string{messages}
	if preview.Model != "" {
		parts = append(parts, preview.Model)
	}
	if preview.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", preview.CostUSD))
	}
	if preview.TodosTotal > 0 {
		parts = append(parts, fmt.Sprintf("todos %d/%d", preview.TodosOpen, preview.TodosTotal))
	}
	return strings.Join(parts, " · ")
}

func previewDates(session provider.Session) string {
	var parts []string
	if !session.TimeCreated.IsZero() {
		parts = append(parts, "created "+session.TimeCreated.Local().Format("2006-01-02 15:04"))
	}
	if !session.TimeUpdated.IsZero() {
		parts = append(parts, "updated "+session.TimeUpdated.Local().Format("2006-01-02 15:04"))
	}
	return strings.Join(parts, " · ")
}

// padPreview forces the pane to an exact line count.
func padPreview(lines []string, height int) string {
	if height <= 0 {
		return ""
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// wrapPlain word-wraps plain text to width, keeping at most maxLines lines
// and marking truncation with an ellipsis.
func wrapPlain(text string, width, maxLines int) []string {
	text = provider.CleanTitle(text)
	if text == "" || maxLines <= 0 {
		return nil
	}
	if width <= 0 {
		width = 1
	}

	var lines []string
	current := ""
	for _, word := range strings.Fields(text) {
		for lipgloss.Width(word) > width {
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
			part, rest := splitPlain(word, width)
			lines = append(lines, part)
			word = rest
		}
		if current == "" {
			current = word
			continue
		}
		candidate := current + " " + word
		if lipgloss.Width(candidate) <= width {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = word
	}
	if current != "" {
		lines = append(lines, current)
	}

	if len(lines) > maxLines {
		lines = lines[:maxLines]
		last := lines[maxLines-1]
		if lipgloss.Width(last)+1 <= width {
			last += "…"
		} else {
			last = truncatePlain(last, width)
		}
		lines[maxLines-1] = last
	}
	return lines
}

func isPrintableKey(msg tea.KeyMsg) bool {
	if msg.Alt {
		return false
	}
	return msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace
}

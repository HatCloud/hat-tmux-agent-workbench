package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Settings panel (root list for settings sub-pages) ──────────────────────

type settingsEntry struct {
	Title    string
	Subtitle string
	Mode     paletteMode
}

type settingsPanelModel struct {
	entries     []settingsEntry
	selected    int
	width       int
	height      int
	openMode    paletteMode // non-zero when an entry was chosen; parent reads and clears
	requestBack bool
}

func newSettingsPanelModel() *settingsPanelModel {
	return &settingsPanelModel{
		entries: []settingsEntry{
			{
				Title:    "General",
				Subtitle: "Common settings (notifications, …)",
				Mode:     paletteModeGeneral,
			},
			{
				Title:    "Status Bar",
				Subtitle: "Manage tmux bottom status bar modules",
				Mode:     paletteModeStatusRight,
			},
			{
				Title:    "Window Title",
				Subtitle: "Configure what is shown in window tab names",
				Mode:     paletteModeWindowTitle,
			},
		},
	}
}

func (m *settingsPanelModel) Init() tea.Cmd { return nil }

func (m *settingsPanelModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "h", "H":
			m.requestBack = true
		case "up", "k", "K", "ctrl+k", "alt+k":
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j", "J", "ctrl+j", "alt+j":
			if m.selected < len(m.entries)-1 {
				m.selected++
			}
		case "enter", " ", "l", "L":
			if m.selected >= 0 && m.selected < len(m.entries) {
				m.openMode = m.entries[m.selected].Mode
			}
		}
	}
	return m, nil
}

func (m *settingsPanelModel) View() string {
	return m.render(newPaletteStyles(), m.width, m.height)
}

func (m *settingsPanelModel) render(styles paletteStyles, width, height int) string {
	if width <= 0 {
		width = 96
	}
	if height <= 0 {
		height = 28
	}
	header := lipgloss.JoinVertical(lipgloss.Left,
		styles.title.Render("Settings"),
		styles.meta.Render("Select a settings category"),
	)

	lines := []string{}
	for idx, entry := range m.entries {
		rowStyle := styles.item.Width(maxInt(24, width-2))
		titleStyle := styles.itemTitle
		metaStyle := styles.itemSubtitle
		fillStyle := lipgloss.NewStyle()
		arrowStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
		if idx == m.selected {
			selectedBG := lipgloss.Color("238")
			rowStyle = styles.selectedItem.Width(maxInt(24, width-2))
			titleStyle = titleStyle.Background(selectedBG).Foreground(lipgloss.Color("230"))
			metaStyle = styles.selectedSubtle.Background(selectedBG)
			fillStyle = fillStyle.Background(selectedBG)
			arrowStyle = arrowStyle.Background(selectedBG).Foreground(lipgloss.Color("250"))
		}
		arrow := arrowStyle.Render("›")
		innerWidth := maxInt(22, width-2)
		titleText := truncate(entry.Title, maxInt(8, innerWidth-lipgloss.Width(arrow)-1))
		gapWidth := maxInt(1, innerWidth-lipgloss.Width(titleText)-lipgloss.Width(arrow))
		titleRow := lipgloss.JoinHorizontal(lipgloss.Left,
			titleStyle.Render(titleText),
			fillStyle.Render(strings.Repeat(" ", gapWidth)),
			arrow,
		)
		subtitleText := truncate(entry.Subtitle, innerWidth)
		subtitleGap := maxInt(0, innerWidth-lipgloss.Width(subtitleText))
		subtitleRow := lipgloss.JoinHorizontal(lipgloss.Left,
			metaStyle.Render(subtitleText),
			fillStyle.Render(strings.Repeat(" ", subtitleGap)),
		)
		lines = append(lines, rowStyle.Render(lipgloss.JoinVertical(lipgloss.Left, titleRow, subtitleRow)))
	}

	bodyHeight := maxInt(8, height-7)
	body := lipgloss.NewStyle().Height(bodyHeight).Render(strings.Join(lines, "\n"))

	renderSegments := func(pairs [][2]string) string {
		return renderShortcutPairs(
			func(v string) string { return styles.shortcutKey.Render(v) },
			func(v string) string { return styles.shortcutText.Render(v) },
			"   ", pairs)
	}
	footer := pickRenderedShortcutFooter(width, renderSegments,
		[][2]string{{"J/K", "move"}, {"Enter", "open"}, {"Esc", "back"}},
		[][2]string{{"J/K", "move"}, {"Enter", "open"}},
	)

	view := lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", footer)
	return lipgloss.NewStyle().Width(width).Height(height).Padding(0, 1).Render(view)
}

// ── Window Title panel ─────────────────────────────────────────────────────

type windowTitleEntry struct {
	Key      string
	Title    string
	Subtitle string
	Enabled  bool
}

type windowTitlePanelModel struct {
	entries     []windowTitleEntry
	selected    int
	width       int
	height      int
	status      string
	statusUntil time.Time
	requestBack bool
}

func newWindowTitlePanelModel() *windowTitlePanelModel {
	m := &windowTitlePanelModel{}
	m.reload()
	return m
}

func (m *windowTitlePanelModel) reload() {
	cfg := loadAppConfig()
	m.entries = []windowTitleEntry{
		{
			Key:      windowNameOptionStatus,
			Title:    "Show status",
			Subtitle: "Show [B]/[I]/[?]/[L]/[E] status prefix in window tab name",
			Enabled:  windowNameShowStatus(cfg),
		},
		{
			Key:      windowNameOptionPath,
			Title:    "Show directory name",
			Subtitle: "Include project dir in window tab name",
			Enabled:  windowNameShowPath(cfg),
		},
		{
			Key:      windowNameOptionModel,
			Title:    "Show model name",
			Subtitle: "Append [sonnet/opus/…] to window tab name",
			Enabled:  windowNameShowModel(cfg),
		},
	}
	m.selected = clampInt(m.selected, 0, maxInt(0, len(m.entries)-1))
}

func (m *windowTitlePanelModel) currentStatus() string {
	if m.status == "" {
		return ""
	}
	if !m.statusUntil.IsZero() && time.Now().After(m.statusUntil) {
		m.status = ""
		return ""
	}
	return m.status
}

func (m *windowTitlePanelModel) setStatus(text string, duration time.Duration) {
	m.status = text
	m.statusUntil = time.Now().Add(duration)
}

func (m *windowTitlePanelModel) Init() tea.Cmd { return nil }

func (m *windowTitlePanelModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "h", "H":
			m.requestBack = true
		case "up", "k", "K", "ctrl+k", "alt+k":
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j", "J", "ctrl+j", "alt+j":
			if m.selected < len(m.entries)-1 {
				m.selected++
			}
		case "enter", " ", "l", "L":
			m.toggleSelected()
		}
	}
	return m, nil
}

func (m *windowTitlePanelModel) toggleSelected() {
	if m.selected < 0 || m.selected >= len(m.entries) {
		return
	}
	entry := m.entries[m.selected]
	if err := toggleWindowNameOption(entry.Key); err != nil {
		m.setStatus(err.Error(), 1500*time.Millisecond)
		return
	}
	_ = paletteTmuxRunner("refresh-client", "-S")
	m.reload()
	if m.selected < len(m.entries) {
		updated := m.entries[m.selected]
		verb := "disabled"
		if updated.Enabled {
			verb = "enabled"
		}
		m.setStatus(fmt.Sprintf("%s %s", updated.Title, verb), 1500*time.Millisecond)
	}
}

func (m *windowTitlePanelModel) View() string {
	return m.render(newPaletteStyles(), m.width, m.height)
}

func (m *windowTitlePanelModel) render(styles paletteStyles, width, height int) string {
	if width <= 0 {
		width = 96
	}
	if height <= 0 {
		height = 28
	}
	header := lipgloss.JoinVertical(lipgloss.Left,
		styles.title.Render("Window Title"),
		styles.meta.Render("Configure what is shown in tmux window tab names"),
	)

	lines := []string{}
	for idx, entry := range m.entries {
		rowStyle := styles.item.Width(maxInt(24, width-2))
		titleStyle := styles.itemTitle
		metaStyle := styles.itemSubtitle
		detailStyle := styles.meta
		fillStyle := lipgloss.NewStyle()
		badgeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Background(lipgloss.Color("241")).Padding(0, 1).Bold(true)
		badgeLabel := "OFF"
		if entry.Enabled {
			badgeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Background(lipgloss.Color("150")).Padding(0, 1).Bold(true)
			badgeLabel = "ON"
		}
		if idx == m.selected {
			selectedBG := lipgloss.Color("238")
			rowStyle = styles.selectedItem.Width(maxInt(24, width-2))
			titleStyle = titleStyle.Background(selectedBG).Foreground(lipgloss.Color("230"))
			metaStyle = styles.selectedSubtle.Background(selectedBG)
			detailStyle = styles.selectedSubtle.Background(selectedBG)
			fillStyle = fillStyle.Background(selectedBG)
		}
		badge := badgeStyle.Render(badgeLabel)
		innerWidth := maxInt(22, width-2)
		titleText := truncate(entry.Title, maxInt(8, innerWidth-lipgloss.Width(badge)-1))
		gapWidth := maxInt(1, innerWidth-lipgloss.Width(titleText)-lipgloss.Width(badge))
		titleRow := lipgloss.JoinHorizontal(lipgloss.Left,
			titleStyle.Render(titleText),
			fillStyle.Render(strings.Repeat(" ", gapWidth)),
			badge,
		)
		subtitleText := truncate(entry.Subtitle, innerWidth)
		subtitleGap := maxInt(0, innerWidth-lipgloss.Width(subtitleText))
		subtitleRow := lipgloss.JoinHorizontal(lipgloss.Left,
			metaStyle.Render(subtitleText),
			fillStyle.Render(strings.Repeat(" ", subtitleGap)),
		)
		stateText := ""
		if entry.Enabled {
			stateText = "Active — shown in the window tab name"
		} else {
			stateText = "Inactive — hidden from the window tab name"
		}
		stateText = truncate(stateText, innerWidth)
		stateGap := maxInt(0, innerWidth-lipgloss.Width(stateText))
		stateRow := lipgloss.JoinHorizontal(lipgloss.Left,
			detailStyle.Render(stateText),
			fillStyle.Render(strings.Repeat(" ", stateGap)),
		)
		lines = append(lines, rowStyle.Render(lipgloss.JoinVertical(lipgloss.Left, titleRow, subtitleRow, stateRow)))
	}

	bodyHeight := maxInt(8, height-7)
	body := lipgloss.NewStyle().Height(bodyHeight).Render(strings.Join(lines, "\n"))

	status := strings.TrimSpace(m.currentStatus())
	renderSegments := func(pairs [][2]string) string {
		return renderShortcutPairs(
			func(v string) string { return styles.shortcutKey.Render(v) },
			func(v string) string { return styles.shortcutText.Render(v) },
			"   ", pairs)
	}
	footer := pickRenderedShortcutFooter(width, renderSegments,
		[][2]string{{"J/K", "move"}, {"Enter", "toggle"}, {"Esc", "back"}},
		[][2]string{{"J/K", "move"}, {"Enter", "toggle"}},
	)
	if status != "" {
		statusText := styles.statusBad.Render(truncate(status, maxInt(12, minInt(24, width/3))))
		if lipgloss.Width(footer)+2+lipgloss.Width(statusText) <= width {
			gap := width - lipgloss.Width(footer) - lipgloss.Width(statusText)
			if gap < 2 {
				gap = 2
			}
			footer = footer + strings.Repeat(" ", gap) + statusText
		}
	}
	footer = lipgloss.NewStyle().Width(width).Render(footer)

	view := lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", footer)
	return lipgloss.NewStyle().Width(width).Height(height).Padding(0, 1).Render(view)
}

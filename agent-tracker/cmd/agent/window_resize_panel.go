package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	windowResizeOptionAutoResize     = "auto_resize"
	windowResizeOptionOrientation    = "orientation"
	windowResizeOptionMainRatio      = "main_ratio"
	windowResizeOptionThirdPane      = "third_pane"
	windowResizeOptionSideRatio      = "side_ratio"
	windowResizeOptionStatusPosition = "status_position"
)

type windowResizeEntry struct {
	Key      string
	Title    string
	Subtitle string
	Enabled  bool
	Value    string
	Values   []string
}

type windowResizePanelModel struct {
	entries     []windowResizeEntry
	windowID    string
	selected    int
	width       int
	height      int
	status      string
	statusUntil time.Time
	requestBack bool
}

func newWindowResizePanelModel(windowIDs ...string) *windowResizePanelModel {
	m := &windowResizePanelModel{}
	if len(windowIDs) > 0 {
		m.windowID = strings.TrimSpace(windowIDs[0])
	}
	m.reload()
	return m
}

func (m *windowResizePanelModel) reload() {
	cfg := loadAppConfig()
	orientation := layoutOrientationSetting(cfg)
	if m.windowID != "" {
		if current := tmuxWindowOption(m.windowID, "@agent_orientation"); current == "landscape" || current == "portrait" {
			orientation = current
		}
	}
	m.entries = []windowResizeEntry{
		{
			Key:      windowResizeOptionAutoResize,
			Title:    "Auto orientation on resize",
			Subtitle: "Reflow only when the window crosses portrait/landscape thresholds",
			Enabled:  layoutAutoResizeSetting(cfg),
		},
		{
			Key:      windowResizeOptionOrientation,
			Title:    "Window orientation",
			Subtitle: "Switch now; also set the fixed default used while auto-resize is off",
			Value:    orientation,
			Values:   []string{"landscape", "portrait"},
		},
		{
			Key:      windowResizeOptionMainRatio,
			Title:    "Agent / side ratio",
			Subtitle: "Agent left/top versus git side; manual dragging remains respected",
			Value:    layoutMainRatioSetting(cfg),
			Values:   []string{"50:50", "55:45", "60:40", "65:35", "70:30"},
		},
		{
			Key:      windowResizeOptionThirdPane,
			Title:    "Third run pane",
			Subtitle: "Add a shell below git in newly created agent windows",
			Enabled:  layoutThirdPaneSetting(cfg),
		},
		{
			Key:      windowResizeOptionSideRatio,
			Title:    "Git / run ratio",
			Subtitle: "Top git versus bottom run height when the third pane is enabled",
			Value:    layoutSideRatioSetting(cfg),
			Values:   []string{"50:50", "60:40", "70:30", "75:25", "80:20"},
		},
		{
			Key:      windowResizeOptionStatusPosition,
			Title:    "Status bar position",
			Subtitle: "auto follows the current layout orientation",
			Value:    statusPositionSetting(cfg),
			Values:   []string{"auto", "top", "bottom"},
		},
	}
	m.selected = clampInt(m.selected, 0, maxInt(0, len(m.entries)-1))
}

func (m *windowResizePanelModel) Init() tea.Cmd { return nil }

func (m *windowResizePanelModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch paletteKeyString(msg) {
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

func (m *windowResizePanelModel) selectedEntryKey() string {
	if m.selected < 0 || m.selected >= len(m.entries) {
		return ""
	}
	return m.entries[m.selected].Key
}

func (m *windowResizePanelModel) currentStatus() string {
	if m.status == "" {
		return ""
	}
	if !m.statusUntil.IsZero() && time.Now().After(m.statusUntil) {
		m.status = ""
		return ""
	}
	return m.status
}

func (m *windowResizePanelModel) setStatus(text string, duration time.Duration) {
	m.status = text
	m.statusUntil = time.Now().Add(duration)
}

func (m *windowResizePanelModel) toggleSelected() {
	if m.selected < 0 || m.selected >= len(m.entries) {
		return
	}
	entry := m.entries[m.selected]
	var err error
	switch entry.Key {
	case windowResizeOptionAutoResize:
		err = toggleLayoutAutoResize()
	case windowResizeOptionOrientation:
		next := nextInCycle(entry.Value, []string{"landscape", "portrait"})
		err = setLayoutOrientation(next)
		if err == nil && m.windowID != "" {
			script := filepath.Join(homeDir(), ".hat-config", "tmux", "scripts", "reflow_agent_layout.sh")
			_, err = runCommandOutput(10*time.Second, script, m.windowID, next)
			if err == nil {
				_ = runTmux("set-option", "-w", "-t", m.windowID, "@agent_orientation_mode", layoutDefaultSetting(loadAppConfig()))
			}
		}
	case windowResizeOptionMainRatio:
		_, err = cycleLayoutMainPercent()
	case windowResizeOptionThirdPane:
		err = toggleLayoutThirdPane()
	case windowResizeOptionSideRatio:
		_, err = cycleLayoutSideTopPercent()
	case windowResizeOptionStatusPosition:
		_, err = cycleStatusPosition()
		if err == nil {
			err = exec.Command(filepath.Join(homeDir(), ".hat-config", "tmux", "scripts", "update_status_position.sh")).Run()
		}
	}
	if err != nil {
		m.setStatus(err.Error(), 2*time.Second)
		return
	}
	m.reload()
	updated := m.entries[m.selected]
	if updated.Values != nil {
		m.setStatus(fmt.Sprintf("%s: %s", updated.Title, updated.Value), 1500*time.Millisecond)
	} else {
		verb := "disabled"
		if updated.Enabled {
			verb = "enabled"
		}
		m.setStatus(fmt.Sprintf("%s %s", updated.Title, verb), 1500*time.Millisecond)
	}
}

func (m *windowResizePanelModel) View() string {
	return m.render(newPaletteStyles(), m.width, m.height)
}

func (m *windowResizePanelModel) render(styles paletteStyles, width, height int) string {
	if width <= 0 {
		width = 96
	}
	if height <= 0 {
		height = 28
	}
	header := lipgloss.JoinVertical(lipgloss.Left,
		styles.title.Render("Window & Resize"),
		styles.meta.Render("Current orientation and defaults for future layout changes"),
	)

	lines := make([]string, 0, len(m.entries))
	for idx, entry := range m.entries {
		rowStyle := styles.item.Width(maxInt(24, width-2))
		titleStyle := styles.itemTitle
		metaStyle := styles.itemSubtitle
		detailStyle := styles.meta
		fillStyle := lipgloss.NewStyle()
		badgeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Background(lipgloss.Color("241")).Padding(0, 1).Bold(true)
		badgeLabel := "OFF"
		if entry.Values != nil {
			bg := lipgloss.Color("110")
			if entry.Value == "auto" {
				bg = lipgloss.Color("245")
			}
			badgeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Background(bg).Padding(0, 1).Bold(true)
			badgeLabel = strings.ToUpper(entry.Value)
		} else if entry.Enabled {
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
		subtitleRow := lipgloss.JoinHorizontal(lipgloss.Left,
			metaStyle.Render(subtitleText),
			fillStyle.Render(strings.Repeat(" ", maxInt(0, innerWidth-lipgloss.Width(subtitleText)))),
		)
		stateText := truncate(windowResizeEntryState(entry), innerWidth)
		stateRow := lipgloss.JoinHorizontal(lipgloss.Left,
			detailStyle.Render(stateText),
			fillStyle.Render(strings.Repeat(" ", maxInt(0, innerWidth-lipgloss.Width(stateText)))),
		)
		lines = append(lines, rowStyle.Render(lipgloss.JoinVertical(lipgloss.Left, titleRow, subtitleRow, stateRow)))
	}

	body := lipgloss.NewStyle().Height(maxInt(8, height-7)).Render(strings.Join(lines, "\n"))
	renderSegments := func(pairs [][2]string) string {
		return renderShortcutPairs(
			func(v string) string { return styles.shortcutKey.Render(v) },
			func(v string) string { return styles.shortcutText.Render(v) },
			"   ", pairs)
	}
	footer := pickRenderedShortcutFooter(width, renderSegments,
		[][2]string{{"J/K", "move"}, {"Enter", "change"}, {"Esc", "back"}},
		[][2]string{{"Enter", "change"}, {"Esc", "back"}},
	)
	if status := strings.TrimSpace(m.currentStatus()); status != "" {
		statusText := styles.statusBad.Render(truncate(status, maxInt(12, minInt(28, width/3))))
		if lipgloss.Width(footer)+2+lipgloss.Width(statusText) <= width {
			footer += strings.Repeat(" ", width-lipgloss.Width(footer)-lipgloss.Width(statusText)) + statusText
		}
	}
	footer = lipgloss.NewStyle().Width(width).Render(footer)
	view := lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", footer)
	return lipgloss.NewStyle().Width(width).Height(height).Padding(0, 1).Render(view)
}

func windowResizeEntryState(entry windowResizeEntry) string {
	switch entry.Key {
	case windowResizeOptionAutoResize:
		if entry.Enabled {
			return "Orientation reflows on threshold crossing; divider drags remain untouched"
		}
		return "No background layout changes; manual pane sizes remain untouched"
	case windowResizeOptionOrientation:
		return "Current window is " + entry.Value + "; Enter switches it immediately"
	case windowResizeOptionMainRatio:
		return "Applied only when creating a window or changing orientation"
	case windowResizeOptionThirdPane:
		if entry.Enabled {
			return "New windows contain ai + git + run; existing windows are not mutated"
		}
		return "New windows contain ai + git only"
	case windowResizeOptionSideRatio:
		return "Used by three-pane windows when created or reflowed"
	case windowResizeOptionStatusPosition:
		if entry.Value == "auto" {
			return "Status line follows the current orientation"
		}
		return "Status line pinned to " + entry.Value
	}
	return ""
}

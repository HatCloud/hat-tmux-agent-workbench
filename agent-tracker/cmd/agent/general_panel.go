package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/david/agent-tracker/internal/ipc"
	"github.com/david/agent-tracker/internal/paths"
)

// ── General panel (common, daemon-wide settings) ───────────────────────────

const (
	generalOptionNotifications  = "notifications"
	generalOptionNotifyGroup    = "notification_group"
	generalOptionLayoutDefault  = "layout_default"
	generalOptionStatusPosition = "status_position"
	generalOptionIconSet        = "icon_set"
	generalOptionTimerTimezone  = "timer_timezone"
	generalOptionPollInterval   = "poll_interval"
	generalOptionNewAgentPrompt = "new_agent_prompt"
	generalOptionStripDate      = "strip_date_prefix"
	generalOptionWindowNavSize  = "window_nav_size"
	generalOptionAutoRetry      = "auto_retry"
	generalOptionAutoRetryMax   = "auto_retry_max"
)

type generalEntry struct {
	Key      string
	Title    string
	Subtitle string
	Enabled  bool     // boolean toggle 用（Values 为空时）
	Value    string   // 多值设置的当前值（Values 非空时有效）
	Values   []string // 多值设置的循环选项；非空 → 三态/多态而非 ON/OFF
	Editable bool     // Enter 打开自由文本编辑器
}

type generalPanelModel struct {
	entries     []generalEntry
	selected    int
	width       int
	height      int
	status      string
	statusUntil time.Time
	requestBack bool
	tzEditing   bool
	tzInput     []rune
	tzCursor    int
	// editKey identifies which editable field the text editor is bound to
	// (generalOptionTimerTimezone or generalOptionPollInterval).
	editKey string
}

func newGeneralPanelModel() *generalPanelModel {
	m := &generalPanelModel{}
	m.reload()
	return m
}

func (m *generalPanelModel) reload() {
	cfg := loadAppConfig()
	m.entries = []generalEntry{
		{
			Key:      generalOptionNotifications,
			Title:    "Notifications",
			Subtitle: "Push a system notification when an agent goes idle or needs input",
			Enabled:  notificationsEnabledSetting(),
		},
		{
			Key:      generalOptionNotifyGroup,
			Title:    "Notification grouping",
			Subtitle: "single = newest replaces older; per_window = one notification per window",
			Value:    notificationGroupModeSetting(),
			Values:   []string{"single", "per_window"},
		},
		{
			Key:      generalOptionLayoutDefault,
			Title:    "Default layout",
			Subtitle: "Orientation for newly created agent windows",
			Value:    layoutDefaultSetting(cfg),
			Values:   []string{"auto", "landscape", "portrait"},
		},
		{
			Key:      generalOptionStatusPosition,
			Title:    "Status bar position",
			Subtitle: "Where the tmux status line sits (auto = follow layout orientation)",
			Value:    statusPositionSetting(cfg),
			Values:   []string{"auto", "top", "bottom"},
		},
		{
			Key:      generalOptionIconSet,
			Title:    "Icon set",
			Subtitle: "Status bar glyphs: nerd (Nerd Font) / emoji / ascii (plain text)",
			Value:    iconSetSetting(cfg),
			Values:   []string{"nerd", "emoji", "ascii"},
		},
		{
			Key:      generalOptionTimerTimezone,
			Title:    "Timer timezone",
			Subtitle: "Space = system auto; Enter = set IANA timezone or UTC offset",
			Value:    timerTimezoneSetting(cfg),
			Editable: true,
		},
		{
			Key:      generalOptionPollInterval,
			Title:    "Poll interval",
			Subtitle: "Enter = cycle 1s/3s/10s; Space = type a custom interval",
			Value:    pollIntervalSetting(cfg),
			Values:   []string{"1s", "3s", "10s"},
			Editable: true,
		},
		{
			Key:      generalOptionNewAgentPrompt,
			Title:    "New agent prompt",
			Subtitle: "Ask for a title before `prefix ]` creates the window",
			Enabled:  newAgentPromptSetting(cfg),
		},
		{
			Key:      generalOptionStripDate,
			Title:    "Strip date prefix",
			Subtitle: "Drop a leading YYYY-MM-DD- from window names/titles",
			Enabled:  stripDatePrefixSetting(cfg),
		},
		{
			Key:      generalOptionWindowNavSize,
			Title:    "Window nav size",
			Subtitle: "Width of the `prefix w` window navigator popup",
			Value:    windowNavSizeSetting(cfg),
			Values:   []string{"standard", "wide", "full"},
		},
		{
			Key:      generalOptionAutoRetry,
			Title:    "Auto-retry on error",
			Subtitle: "Auto-resend when an agent stops on a 5xx/529 overloaded error",
			Enabled:  autoRetrySetting(cfg),
		},
		{
			Key:      generalOptionAutoRetryMax,
			Title:    "Auto-retry max",
			Subtitle: "Cap on consecutive auto-retries for the same error",
			Value:    strconv.Itoa(autoRetryMaxSetting(cfg)),
			Values:   []string{"3", "5", "10"},
		},
	}
	m.selected = clampInt(m.selected, 0, maxInt(0, len(m.entries)-1))
}

// notificationsEnabledSetting reads the daemon's persisted notification toggle.
// Defaults to true (the daemon's own default) when the settings file or key is
// absent.
func notificationsEnabledSetting() bool {
	data, err := os.ReadFile(paths.SettingsStore())
	if err != nil {
		return true
	}
	var stored struct {
		NotificationsEnabled *bool `json:"notifications_enabled"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		return true
	}
	if stored.NotificationsEnabled == nil {
		return true
	}
	return *stored.NotificationsEnabled
}

// notificationGroupModeSetting reads the daemon's persisted grouping mode,
// defaulting to "single" when absent.
func notificationGroupModeSetting() string {
	data, err := os.ReadFile(paths.SettingsStore())
	if err != nil {
		return "single"
	}
	var stored struct {
		NotificationGroupMode *string `json:"notification_group_mode"`
	}
	if err := json.Unmarshal(data, &stored); err != nil || stored.NotificationGroupMode == nil {
		return "single"
	}
	if strings.TrimSpace(*stored.NotificationGroupMode) == "per_window" {
		return "per_window"
	}
	return "single"
}

// cycleNotificationGroupMode flips single ↔ per_window and tells the daemon to
// persist it (the daemon owns settings.json's notification_group_mode).
func cycleNotificationGroupMode() (string, error) {
	next := nextInCycle(notificationGroupModeSetting(), []string{"single", "per_window"})
	if err := sendTrackerCommand("set_notification_group_mode", &ipc.Envelope{Message: next}); err != nil {
		return "", err
	}
	return next, nil
}

func (m *generalPanelModel) currentStatus() string {
	if m.status == "" {
		return ""
	}
	if !m.statusUntil.IsZero() && time.Now().After(m.statusUntil) {
		m.status = ""
		return ""
	}
	return m.status
}

func (m *generalPanelModel) setStatus(text string, duration time.Duration) {
	m.status = text
	m.statusUntil = time.Now().Add(duration)
}

func (m *generalPanelModel) Init() tea.Cmd { return nil }

func (m *generalPanelModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		key := paletteKeyString(msg)
		if m.tzEditing {
			m.handleEditInput(key)
			return m, nil
		}
		switch key {
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
		case "enter", "l", "L":
			if m.selectedEntryKey() == generalOptionTimerTimezone {
				m.openEditInput(generalOptionTimerTimezone)
			} else {
				m.toggleSelected()
			}
		case " ":
			switch m.selectedEntryKey() {
			case generalOptionTimerTimezone:
				m.saveTimezone("auto")
			case generalOptionPollInterval:
				// Poll interval: Enter cycles the presets; Space opens the free-input
				// editor for a custom value (mirrors the timezone editor).
				m.openEditInput(generalOptionPollInterval)
			default:
				m.toggleSelected()
			}
		}
	}
	return m, nil
}

func (m *generalPanelModel) selectedEntryKey() string {
	if m.selected < 0 || m.selected >= len(m.entries) {
		return ""
	}
	return m.entries[m.selected].Key
}

func (m *generalPanelModel) openEditInput(key string) {
	var value string
	switch key {
	case generalOptionPollInterval:
		value = pollIntervalSetting(loadAppConfig())
	default: // timezone
		value = timerTimezoneSetting(loadAppConfig())
		if value == "auto" {
			value = "UTC+8"
		}
	}
	m.editKey = key
	m.tzInput = []rune(value)
	m.tzCursor = len(m.tzInput)
	m.tzEditing = true
}

func (m *generalPanelModel) handleEditInput(key string) {
	switch key {
	case "esc":
		m.tzEditing = false
	case "enter", "ctrl+s":
		if m.editKey == generalOptionPollInterval {
			m.savePollInterval(string(m.tzInput))
		} else {
			m.saveTimezone(string(m.tzInput))
		}
	default:
		applyPaletteInputKey(key, &m.tzInput, &m.tzCursor, false)
	}
}

func (m *generalPanelModel) savePollInterval(value string) {
	if err := setPollInterval(value); err != nil {
		m.setStatus(err.Error(), 3*time.Second)
		return
	}
	m.tzEditing = false
	m.reload()
	m.setStatus("Poll interval: "+pollIntervalSetting(loadAppConfig()), 2*time.Second)
}

func (m *generalPanelModel) saveTimezone(value string) {
	if err := setTimerTimezone(value); err != nil {
		m.setStatus(err.Error(), 3*time.Second)
		return
	}
	m.tzEditing = false
	m.reload()
	m.setStatus("Timer timezone: "+timerTimezoneSetting(loadAppConfig()), 2*time.Second)
}

func (m *generalPanelModel) toggleSelected() {
	if m.selected < 0 || m.selected >= len(m.entries) {
		return
	}
	entry := m.entries[m.selected]
	switch entry.Key {
	case generalOptionNotifications:
		if err := sendTrackerCommand("notifications_toggle", nil); err != nil {
			m.setStatus(err.Error(), 1500*time.Millisecond)
			return
		}
	case generalOptionNotifyGroup:
		if _, err := cycleNotificationGroupMode(); err != nil {
			m.setStatus(err.Error(), 1500*time.Millisecond)
			return
		}
	case generalOptionLayoutDefault:
		if _, err := cycleLayoutDefault(); err != nil {
			m.setStatus(err.Error(), 1500*time.Millisecond)
			return
		}
	case generalOptionStatusPosition:
		if _, err := cycleStatusPosition(); err != nil {
			m.setStatus(err.Error(), 1500*time.Millisecond)
			return
		}
		// 立即按新策略重定位 status line
		_ = exec.Command(filepath.Join(homeDir(), ".hat-config", "tmux", "scripts", "update_status_position.sh")).Run()
	case generalOptionIconSet:
		if _, err := cycleIconSet(); err != nil {
			m.setStatus(err.Error(), 1500*time.Millisecond)
			return
		}
	case generalOptionPollInterval:
		if _, err := cyclePollInterval(); err != nil {
			m.setStatus(err.Error(), 1500*time.Millisecond)
			return
		}
	case generalOptionNewAgentPrompt:
		if err := toggleNewAgentPrompt(); err != nil {
			m.setStatus(err.Error(), 1500*time.Millisecond)
			return
		}
	case generalOptionStripDate:
		if err := toggleStripDatePrefix(); err != nil {
			m.setStatus(err.Error(), 1500*time.Millisecond)
			return
		}
	case generalOptionWindowNavSize:
		if _, err := cycleWindowNavSize(); err != nil {
			m.setStatus(err.Error(), 1500*time.Millisecond)
			return
		}
	case generalOptionAutoRetry:
		if err := toggleAutoRetry(); err != nil {
			m.setStatus(err.Error(), 1500*time.Millisecond)
			return
		}
	case generalOptionAutoRetryMax:
		if _, err := cycleAutoRetryMax(); err != nil {
			m.setStatus(err.Error(), 1500*time.Millisecond)
			return
		}
	}
	m.reload()
	if m.selected < len(m.entries) {
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
}

func (m *generalPanelModel) View() string {
	return m.render(newPaletteStyles(), m.width, m.height)
}

func (m *generalPanelModel) render(styles paletteStyles, width, height int) string {
	if width <= 0 {
		width = 96
	}
	if height <= 0 {
		height = 28
	}
	header := lipgloss.JoinVertical(lipgloss.Left,
		styles.title.Render("General"),
		styles.meta.Render("Common settings shared across all agents"),
	)

	lines := []string{}
	for idx, entry := range m.entries {
		rowStyle := styles.item.Width(maxInt(24, width-2))
		titleStyle := styles.itemTitle
		metaStyle := styles.itemSubtitle
		detailStyle := styles.meta
		fillStyle := lipgloss.NewStyle()
		var badgeStyle lipgloss.Style
		var badgeLabel string
		if entry.Values != nil || entry.Editable {
			bg := lipgloss.Color("110") // 非 auto 的具体值用蓝色
			if entry.Value == "auto" {
				bg = lipgloss.Color("245") // auto 用中性灰
			}
			badgeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Background(bg).Padding(0, 1).Bold(true)
			badgeLabel = strings.ToUpper(entry.Value)
		} else {
			badgeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Background(lipgloss.Color("241")).Padding(0, 1).Bold(true)
			badgeLabel = "OFF"
			if entry.Enabled {
				badgeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Background(lipgloss.Color("150")).Padding(0, 1).Bold(true)
				badgeLabel = "ON"
			}
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
		switch {
		case entry.Key == generalOptionNotifyGroup:
			if entry.Value == "per_window" {
				stateText = "Each window keeps its own notification"
			} else {
				stateText = "One notification; newer replaces older"
			}
		case entry.Key == generalOptionLayoutDefault:
			if entry.Value == "auto" {
				stateText = "New windows pick orientation by window size"
			} else {
				stateText = "New windows open in " + entry.Value + " layout"
			}
		case entry.Key == generalOptionStatusPosition:
			if entry.Value == "auto" {
				stateText = "Status line follows layout orientation"
			} else {
				stateText = "Status line pinned to " + entry.Value
			}
		case entry.Key == generalOptionIconSet:
			switch entry.Value {
			case "emoji":
				stateText = "Status bar uses emoji glyphs"
			case "ascii":
				stateText = "Status bar uses plain ASCII labels"
			default:
				stateText = "Status bar uses Nerd Font glyphs"
			}
		case entry.Key == generalOptionTimerTimezone:
			if entry.Value == "auto" {
				stateText = "Timer wall-clock follows the system timezone"
			} else {
				stateText = "Timer wall-clock uses " + entry.Value
			}
		case entry.Key == generalOptionPollInterval:
			stateText = "Window naming / status refresh every " + entry.Value
		case entry.Key == generalOptionNewAgentPrompt:
			if entry.Enabled {
				stateText = "`prefix ]` prompts for a title before creating the window"
			} else {
				stateText = "`prefix ]` creates the window directly (no prompt)"
			}
		case entry.Key == generalOptionStripDate:
			if entry.Enabled {
				stateText = "Leading YYYY-MM-DD- is stripped from window names"
			} else {
				stateText = "Window names keep their leading YYYY-MM-DD- date"
			}
		case entry.Key == generalOptionWindowNavSize:
			switch entry.Value {
			case "standard":
				stateText = "`prefix w` popup uses a compact width"
			case "full":
				stateText = "`prefix w` popup fills almost the whole client"
			default:
				stateText = "`prefix w` popup is wide (fits the footer on one line)"
			}
		case entry.Key == generalOptionAutoRetry:
			if entry.Enabled {
				stateText = "Recoverable errors are auto-retried with backoff"
			} else {
				stateText = "Errors just show [E]; no automatic retry"
			}
		case entry.Key == generalOptionAutoRetryMax:
			stateText = "Give up after " + entry.Value + " retries of the same error"
		case entry.Enabled:
			stateText = "Active — system notifications are sent"
		default:
			stateText = "Inactive — no system notifications are sent"
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
	footerKeys := [][2]string{{"J/K", "move"}, {"Enter", "toggle"}, {"Esc", "back"}}
	footerCompact := [][2]string{{"J/K", "move"}, {"Enter", "toggle"}}
	if m.selectedEntryKey() == generalOptionTimerTimezone {
		footerKeys = [][2]string{{"J/K", "move"}, {"Enter", "edit"}, {"Space", "auto"}, {"Esc", "back"}}
		footerCompact = [][2]string{{"Enter", "edit"}, {"Space", "auto"}, {"Esc", "back"}}
	}
	if m.selectedEntryKey() == generalOptionPollInterval {
		footerKeys = [][2]string{{"J/K", "move"}, {"Enter", "cycle"}, {"Space", "custom"}, {"Esc", "back"}}
		footerCompact = [][2]string{{"Enter", "cycle"}, {"Space", "custom"}, {"Esc", "back"}}
	}
	footer := pickRenderedShortcutFooter(width, renderSegments, footerKeys, footerCompact)
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
	base := lipgloss.NewStyle().Width(width).Height(height).Padding(0, 1).Render(view)
	if m.tzEditing {
		return m.renderEditInput(styles, width, height)
	}
	return base
}

func (m *generalPanelModel) renderEditInput(styles paletteStyles, width, height int) string {
	modalWidth := minInt(68, maxInt(40, width-4))
	innerWidth := modalWidth - 4
	value := styles.input.Render(renderInputValue(m.tzInput, m.tzCursor, styles))
	title := "Timer timezone"
	hint := "IANA name or UTC offset"
	example := "Examples: Asia/Shanghai   UTC+8   +08:00"
	if m.editKey == generalOptionPollInterval {
		title = "Poll interval"
		hint = "Duration or seconds"
		example = "Examples: 1s   3s   10s   5   500ms"
	}
	rows := []string{
		styles.panelTitle.Render(title),
		"",
		styles.muted.Render(hint),
		lipgloss.NewStyle().Width(innerWidth).Render(value),
	}
	if status := strings.TrimSpace(m.currentStatus()); status != "" {
		rows = append(rows, styles.statusBad.Render(truncate(status, innerWidth)))
	}
	rows = append(rows,
		"",
		styles.muted.Render(example),
		styles.muted.Render("Enter: save   Esc: cancel"),
	)
	content := lipgloss.JoinVertical(lipgloss.Left, rows...)
	modal := lipgloss.NewStyle().
		Border(paletteModalBorder).
		BorderForeground(lipgloss.Color("63")).
		Width(innerWidth).
		Padding(0, 1).
		Render(content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

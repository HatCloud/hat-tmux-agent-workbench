package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── types ─────────────────────────────────────────────────────────────────────

type windowTimerPanelMode int

const (
	windowTimerPanelModeList windowTimerPanelMode = iota
	windowTimerPanelModeAdd
	windowTimerPanelModeEdit
	windowTimerPanelModeDeleteConfirm
	windowTimerPanelModeSaveSnippet
)

type windowTimerViewMode int

const (
	windowTimerViewActive windowTimerViewMode = iota
	windowTimerViewAll
	windowTimerViewHistory
)

// form field indices
const (
	timerFormFieldContent    = 0
	timerFormFieldTrigger    = 1
	timerFormFieldLoop       = 2
	timerFormFieldMax        = 3
	timerFormFieldSendEnter  = 4
	timerFormFieldAutoDelete = 5
	timerFormFieldCount      = 6
)

// timerFormBoolField reports whether idx is a yes/no toggle field.
func timerFormBoolField(idx int) bool {
	return idx == timerFormFieldSendEnter || idx == timerFormFieldAutoDelete
}

type windowTimerPanelModel struct {
	windowID   string
	windowName string
	timers     []*windowTimer
	selected   int
	mode       windowTimerPanelMode

	// directMode: standalone process (prefix t). esc on the active list quits the
	// program instead of signalling back to a palette host (window nav embed).
	directMode bool

	// 3-pane layout (todo-style); viewMode = which pane has focus.
	viewMode     windowTimerViewMode
	history      []*windowTimerHistoryEntry
	histSelected int

	// other-windows pane: timers from live windows other than the current one.
	otherTimers   []*windowTimer
	otherSelected int
	windowInfo    map[string]windowInfo // windowID → {name, dir, status}

	// which window the add/edit form / delete operates on (cross-window edit in all view)
	formTargetWindowID string
	deleteWindowID     string

	// form state (add/edit)
	formTimerID     string // empty = new, non-empty = editing existing
	formFields      [timerFormFieldCount][]rune
	formCursors     [timerFormFieldCount]int
	formActiveField int

	// delete confirm
	deleteTimerID string

	// save-to-snippet modal (from a history row)
	saveContent     string
	saveFields      [2][]rune // 0 = name, 1 = group
	saveCursors     [2]int
	saveActiveField int

	// embedded snippet picker (Ctrl+P in the add/edit form), returnMode
	picker *snippetPanelModel

	// dimensions
	width  int
	height int

	// signal back to host (palette/window nav). In directMode the runner quits.
	requestBack bool

	// status message
	statusMsg   string
	statusUntil time.Time

	// captured by renderPanes each frame; MouseMsg uses these to hit-test panes.
	// paneMouseGeom stores absolute Y ranges (inclusive top, exclusive bottom) of
	// each pane's first-row area and their max visible row counts.
	thisFirstY, thisListH   int
	otherFirstY, otherListH int
	histFirstY, histListH   int
	leftPaneRightX          int // exclusive X boundary between left column and right column
}

func newWindowTimerPanel(windowID, windowName string) *windowTimerPanelModel {
	m := &windowTimerPanelModel{
		windowID:   windowID,
		windowName: windowName,
	}
	m.reload()
	return m
}

func (m *windowTimerPanelModel) reload() {
	m.timers = timersForWindow(m.windowID)
	if m.selected >= len(m.timers) {
		m.selected = maxInt(0, len(m.timers)-1)
	}
	m.history = timerHistoryAll()
	if m.histSelected >= len(m.history) {
		m.histSelected = maxInt(0, len(m.history)-1)
	}
	m.windowInfo = loadWindowInfo()
	other := make([]*windowTimer, 0)
	for _, t := range loadWindowTimers() {
		if t.WindowID == m.windowID {
			continue // current window's timers live in the This Window pane
		}
		if _, live := m.windowInfo[t.WindowID]; !live {
			continue // skip timers whose window has ended
		}
		other = append(other, t)
	}
	sort.SliceStable(other, func(i, j int) bool {
		ni, nj := m.windowInfo[other[i].WindowID].name, m.windowInfo[other[j].WindowID].name
		if ni != nj {
			return ni < nj
		}
		return timerTriggerDisplay(other[i]) < timerTriggerDisplay(other[j])
	})
	m.otherTimers = other
	if m.otherSelected >= len(m.otherTimers) {
		m.otherSelected = maxInt(0, len(m.otherTimers)-1)
	}
}

// windowInfo carries the display attributes the Other Windows pane shows per
// timer: window name, abbreviated directory (@agent_dir), and agent status.
type windowInfo struct {
	name   string
	dir    string
	status string // "busy" | "idle" | "asking" | "limited" | "error" | ""
}

// loadWindowInfo maps window_id → windowInfo for all live tmux windows. Status is
// derived purely from the window-name status prefix the daemon writes, so
// no tracker-cache read is needed. Windows absent from this map have ended.
func loadWindowInfo() map[string]windowInfo {
	info := map[string]windowInfo{}
	out, err := runTmuxOutput("list-windows", "-a", "-F", "#{window_id}|#{window_name}|#{@agent_dir}")
	if err != nil {
		return info
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 2 {
			continue
		}
		raw := parts[1]
		status := ""
		switch {
		case strings.HasPrefix(raw, "[B] "):
			status = "busy"
		case strings.HasPrefix(raw, "[?] "):
			status = "asking"
		case strings.HasPrefix(raw, "[L] "):
			status = "limited"
		case strings.HasPrefix(raw, "[E] "):
			status = "error"
		case strings.HasPrefix(raw, "[I] "):
			status = "idle"
		}
		dir := ""
		if len(parts) >= 3 {
			dir = parts[2]
		}
		info[parts[0]] = windowInfo{name: stripStatusPrefix(raw), dir: dir, status: status}
	}
	return info
}

// statusIcon renders a colored status glyph for the Other Windows pane.
func statusIcon(status string) string {
	switch status {
	case "busy":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("82")).Render("●")
	case "asking":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("?")
	case "limited":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render("L")
	case "error":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render("E")
	case "idle":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("110")).Render("○")
	}
	return " "
}

func (m *windowTimerPanelModel) setStatus(msg string) {
	m.statusMsg = msg
	m.statusUntil = time.Now().Add(3 * time.Second)
}

func (m *windowTimerPanelModel) currentStatus() string {
	if m.statusMsg == "" {
		return ""
	}
	if time.Now().After(m.statusUntil) {
		m.statusMsg = ""
		return ""
	}
	return m.statusMsg
}

// ── BubbleTea ─────────────────────────────────────────────────────────────────

func (m *windowTimerPanelModel) Init() tea.Cmd { return nil }

func (m *windowTimerPanelModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.picker != nil {
			m.picker.width = msg.Width
			m.picker.height = msg.Height
		}
	case tea.KeyMsg:
		// Embedded snippet picker intercepts keys first; its esc/selection must
		// NOT bubble up as this panel's requestBack (which would quit in directMode).
		if m.picker != nil {
			model, cmd := m.picker.Update(msg)
			if updated, ok := model.(*snippetPanelModel); ok {
				m.picker = updated
			}
			if m.picker.requestBack {
				if m.picker.chosenContent != "" {
					chosen := []rune(m.picker.chosenContent)
					m.formFields[timerFormFieldContent] = chosen
					m.formCursors[timerFormFieldContent] = len(chosen)
				}
				m.picker = nil // back to the add/edit form
				return m, nil
			}
			return m, cmd
		}
		model, cmd := m.handleKey(paletteKeyString(msg))
		// directMode: only the top-level active list sets requestBack → quit.
		if m.directMode && m.requestBack {
			return m, tea.Quit
		}
		return model, cmd
	case tea.MouseMsg:
		if m.picker != nil {
			model, cmd := m.picker.Update(msg)
			if updated, ok := model.(*snippetPanelModel); ok {
				m.picker = updated
			}
			return m, cmd
		}
		return m.handleMouse(msg)
	}
	return m, nil
}

func (m *windowTimerPanelModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mode != windowTimerPanelModeList {
		return m, nil
	}
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	// Wheel: scroll the currently focused pane's selection.
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		return m.handleKey("k")
	case tea.MouseButtonWheelDown:
		return m.handleKey("j")
	case tea.MouseButtonLeft:
	default:
		return m, nil
	}
	// Left click: pick a pane by (X, Y) region, switch focus, activate row.
	inLeft := msg.X < m.leftPaneRightX
	if inLeft {
		// This Window (top) or Other Windows (bottom)
		if rel := msg.Y - m.thisFirstY; rel >= 0 && rel < m.thisListH && rel < len(m.timers) {
			m.viewMode = windowTimerViewActive
			m.selected = rel
			m.toggleSelected()
			return m, nil
		}
		if rel := msg.Y - m.otherFirstY; rel >= 0 && rel < m.otherListH && rel < len(m.otherTimers) {
			m.viewMode = windowTimerViewAll
			m.otherSelected = rel
			m.toggleSelectedAll()
			return m, nil
		}
	} else {
		// History (right column)
		if rel := msg.Y - m.histFirstY; rel >= 0 && rel < m.histListH && rel < len(m.history) {
			m.viewMode = windowTimerViewHistory
			m.histSelected = rel
			m.recreateFromHistory()
			return m, nil
		}
	}
	return m, nil
}

func (m *windowTimerPanelModel) handleKey(key string) (tea.Model, tea.Cmd) {
	switch m.mode {
	case windowTimerPanelModeList:
		return m.handleListKey(key)
	case windowTimerPanelModeAdd, windowTimerPanelModeEdit:
		return m.handleFormKey(key)
	case windowTimerPanelModeDeleteConfirm:
		return m.handleDeleteKey(key)
	case windowTimerPanelModeSaveSnippet:
		return m.handleSaveKey(key)
	}
	return m, nil
}

// cycleView moves pane focus This Window → Other Windows → History → This Window.
func (m *windowTimerPanelModel) cycleView() {
	m.viewMode = (m.viewMode + 1) % 3
	m.reload()
}

func (m *windowTimerPanelModel) handleListKey(key string) (tea.Model, tea.Cmd) {
	switch m.viewMode {
	case windowTimerViewAll:
		return m.handleAllKey(key)
	case windowTimerViewHistory:
		return m.handleHistoryKey(key)
	}
	switch key {
	case "esc", "g", "G", "q":
		m.requestBack = true // top level → back (quits in directMode)
	case "tab", "shift+tab":
		m.cycleView()
	case "j", "J", "down":
		if len(m.timers) > 0 {
			m.selected = (m.selected + 1) % len(m.timers)
		}
	case "k", "K", "up":
		if len(m.timers) > 0 {
			m.selected = (m.selected - 1 + len(m.timers)) % len(m.timers)
		}
	case "enter", " ":
		m.toggleSelected()
	case "a", "A":
		m.openAddForm()
	case "r", "R":
		m.quickAddContinue()
	case "e", "E":
		m.openEditForm()
	case "x", "X":
		m.openDeleteConfirm()
	}
	return m, nil
}

// statusForAdded reports a freshly added timer, distinguishing the dormant
// quota state ("--" next-fire) from a scheduled one.
func (m *windowTimerPanelModel) statusForAdded(t *windowTimer) {
	switch {
	case t.TriggerMode == windowTimerTriggerQuota && t.NextFireAt.IsZero():
		m.setStatus("timer added (dormant until a limit is hit)")
	case t.NextFireAt.IsZero():
		m.setStatus("timer added")
	default:
		m.setStatus("timer added · next " + formatWindowTimerTime(t.NextFireAt, "01/02 15:04") + " " + timerTimezoneSetting(loadAppConfig()))
	}
}

// quickAddContinue adds the canned "continue after quota reset" timer for the
// current window: content "continue", trigger reset, send Enter, one-shot.
// Created before any limit is hit it stays dormant until a limit stamp wakes it.
func (m *windowTimerPanelModel) quickAddContinue() {
	for _, t := range m.timers {
		if t.Enabled && t.TriggerMode == windowTimerTriggerQuota && t.Content == "continue" {
			m.setStatus("continue-after-reset timer already exists")
			return
		}
	}
	t, err := addWindowTimer(m.windowID, "continue", "reset", "", "0", true, false)
	if err != nil {
		m.setStatus("error: " + err.Error())
		return
	}
	m.statusForAdded(t)
	m.reload()
}

// copyOtherTimerToCurrent clones the selected other-window timer onto the
// current window, round-tripping its config through the same string forms the
// add form accepts.
func (m *windowTimerPanelModel) copyOtherTimerToCurrent() {
	src := m.selectedAllTimer()
	if src == nil {
		return
	}
	t, err := addWindowTimer(m.windowID, src.Content, timerTriggerInput(src),
		timerLoopInput(src), strconv.Itoa(src.MaxExecutions), src.SendEnter, src.DeleteOnDone)
	if err != nil {
		m.setStatus("copy failed: " + err.Error())
		return
	}
	m.statusForAdded(t)
	m.reload()
}

// copyHistoryToCurrent clones the selected history entry straight into an
// active timer on the current window (Enter merely prefills the add form).
func (m *windowTimerPanelModel) copyHistoryToCurrent() {
	e := m.selectedHistory()
	if e == nil {
		return
	}
	max := e.Max
	if max == "" {
		max = "0"
	}
	t, err := addWindowTimer(m.windowID, e.Content, e.Trigger, e.Loop, max, e.SendEnter, false)
	if err != nil {
		m.setStatus("copy failed: " + err.Error())
		return
	}
	m.statusForAdded(t)
	m.reload()
}

// handleHistoryKey: history view. esc/h here returns to the active view (never
// sets requestBack), so directMode multi-level esc does not collapse to a quit.
func (m *windowTimerPanelModel) handleHistoryKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "g", "G", "q":
		m.requestBack = true
	case "tab", "shift+tab":
		m.cycleView()
	case "j", "J", "down":
		if len(m.history) > 0 {
			m.histSelected = (m.histSelected + 1) % len(m.history)
		}
	case "k", "K", "up":
		if len(m.history) > 0 {
			m.histSelected = (m.histSelected - 1 + len(m.history)) % len(m.history)
		}
	case "enter", " ":
		m.recreateFromHistory()
	case "v", "V":
		m.copyHistoryToCurrent()
	case "s", "S":
		m.openSaveSnippet()
	case "x", "X", "d", "D":
		m.deleteSelectedHistory()
	}
	return m, nil
}

// handleAllKey: Other Windows pane. Operations act on the selected timer's own
// WindowID (cross-window). Tab cycles pane focus; esc exits the panel.
func (m *windowTimerPanelModel) handleAllKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "g", "G", "q":
		m.requestBack = true
	case "tab", "shift+tab":
		m.cycleView()
	case "j", "J", "down":
		if len(m.otherTimers) > 0 {
			m.otherSelected = (m.otherSelected + 1) % len(m.otherTimers)
		}
	case "k", "K", "up":
		if len(m.otherTimers) > 0 {
			m.otherSelected = (m.otherSelected - 1 + len(m.otherTimers)) % len(m.otherTimers)
		}
	case "enter", " ":
		m.toggleSelectedAll()
	case "v", "V":
		m.copyOtherTimerToCurrent()
	case "e", "E":
		m.openEditFormAll()
	case "x", "X":
		m.openDeleteConfirmAll()
	case "a", "A":
		m.openAddForm() // new timers default to the current window
	}
	return m, nil
}

func (m *windowTimerPanelModel) selectedAllTimer() *windowTimer {
	if m.otherSelected < 0 || m.otherSelected >= len(m.otherTimers) {
		return nil
	}
	return m.otherTimers[m.otherSelected]
}

func (m *windowTimerPanelModel) toggleSelectedAll() {
	t := m.selectedAllTimer()
	if t == nil {
		return
	}
	if err := toggleWindowTimer(t.ID, t.WindowID); err != nil {
		m.setStatus("toggle failed: " + err.Error())
	}
	m.reload()
}

func (m *windowTimerPanelModel) openEditFormAll() {
	t := m.selectedAllTimer()
	if t == nil {
		return
	}
	m.formTargetWindowID = t.WindowID
	m.prefillEditForm(t)
}

func (m *windowTimerPanelModel) openDeleteConfirmAll() {
	t := m.selectedAllTimer()
	if t == nil {
		return
	}
	m.deleteTimerID = t.ID
	m.deleteWindowID = t.WindowID
	m.mode = windowTimerPanelModeDeleteConfirm
}

func (m *windowTimerPanelModel) selectedHistory() *windowTimerHistoryEntry {
	if m.histSelected < 0 || m.histSelected >= len(m.history) {
		return nil
	}
	return m.history[m.histSelected]
}

// recreateFromHistory opens the Add form prefilled from the selected history entry.
func (m *windowTimerPanelModel) recreateFromHistory() {
	e := m.selectedHistory()
	if e == nil {
		return
	}
	m.openAddForm()
	set := func(idx int, v string) {
		m.formFields[idx] = []rune(v)
		m.formCursors[idx] = len(v)
	}
	set(timerFormFieldContent, e.Content)
	set(timerFormFieldTrigger, e.Trigger)
	set(timerFormFieldLoop, e.Loop)
	set(timerFormFieldMax, e.Max)
	if e.SendEnter {
		m.formFields[timerFormFieldSendEnter] = []rune("yes")
	} else {
		m.formFields[timerFormFieldSendEnter] = []rune("no")
	}
}

func (m *windowTimerPanelModel) deleteSelectedHistory() {
	e := m.selectedHistory()
	if e == nil {
		return
	}
	if err := deleteTimerHistoryCombo(e.Content, e.Trigger, e.Loop, e.Max, e.SendEnter); err != nil {
		m.setStatus("delete failed: " + err.Error())
	} else {
		m.setStatus("history entry removed")
	}
	m.reload()
}

func (m *windowTimerPanelModel) openSaveSnippet() {
	e := m.selectedHistory()
	if e == nil {
		return
	}
	m.saveContent = e.Content
	name := defaultSnippetNameFromContent(e.Content)
	m.saveFields[0] = []rune(name)
	m.saveCursors[0] = len(name)
	m.saveFields[1] = []rune("timer")
	m.saveCursors[1] = len("timer")
	m.saveActiveField = 0
	m.mode = windowTimerPanelModeSaveSnippet
}

func (m *windowTimerPanelModel) handleSaveKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = windowTimerPanelModeList
	case "tab", "down", "ctrl+j":
		m.saveActiveField = (m.saveActiveField + 1) % 2
	case "shift+tab", "up", "ctrl+k":
		m.saveActiveField = (m.saveActiveField - 1 + 2) % 2
	case "ctrl+s":
		m.submitSaveSnippet()
	case "enter":
		if m.saveActiveField == 0 {
			m.saveActiveField = 1
		} else {
			m.submitSaveSnippet()
		}
	default:
		applyPaletteInputKey(key, &m.saveFields[m.saveActiveField], &m.saveCursors[m.saveActiveField], false)
	}
	return m, nil
}

func (m *windowTimerPanelModel) submitSaveSnippet() {
	name := strings.TrimSpace(string(m.saveFields[0]))
	group := strings.TrimSpace(string(m.saveFields[1]))
	if name == "" {
		m.setStatus("name cannot be empty")
		m.saveActiveField = 0
		return
	}
	if err := addSnippet(group, name, "", m.saveContent); err != nil {
		m.setStatus("save failed: " + err.Error())
		return
	}
	m.setStatus("saved to snippet: " + group + "/" + name)
	m.mode = windowTimerPanelModeList
}

// defaultSnippetNameFromContent derives a safe snippet name from a content's
// first line (lowercase, alnum/dash, capped).
func defaultSnippetNameFromContent(content string) string {
	line := content
	if i := strings.IndexByte(content, '\n'); i >= 0 {
		line = content[:i]
	}
	line = strings.ToLower(strings.TrimSpace(line))
	var b strings.Builder
	for _, r := range line {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '/':
			b.WriteByte('-')
		}
		if b.Len() >= 32 {
			break
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "snippet"
	}
	return name
}

func (m *windowTimerPanelModel) handleFormKey(key string) (tea.Model, tea.Cmd) {
	// Ctrl+P opens the embedded snippet picker (returnMode) to fill Content.
	// Handled before the default text-input path so it isn't typed into a field.
	if key == "ctrl+p" {
		m.picker = newSnippetPanelModel(true, "timer")
		m.picker.width = m.width
		m.picker.height = m.height
		return m, nil
	}
	// Boolean toggle fields (Send Enter / Auto delete) are not text input.
	if timerFormBoolField(m.formActiveField) {
		switch key {
		case "esc":
			m.mode = windowTimerPanelModeList
			return m, nil
		case "tab", "down", "ctrl+j", "enter":
			if m.formActiveField == timerFormFieldCount-1 {
				m.submitForm()
			} else {
				m.formActiveField++
			}
			return m, nil
		case "shift+tab", "up", "ctrl+k":
			m.formActiveField--
			return m, nil
		case " ", "y", "Y", "n", "N":
			m.toggleBoolField(m.formActiveField)
			return m, nil
		case "ctrl+s", "alt+enter":
			m.submitForm()
			return m, nil
		}
		return m, nil
	}

	switch key {
	case "esc":
		m.mode = windowTimerPanelModeList
	case "tab", "down", "ctrl+j":
		m.formActiveField = (m.formActiveField + 1) % timerFormFieldCount
	case "shift+tab", "up", "ctrl+k":
		m.formActiveField = (m.formActiveField - 1 + timerFormFieldCount) % timerFormFieldCount
	case "enter":
		if m.formActiveField < timerFormFieldCount-1 {
			m.formActiveField++
		} else {
			m.submitForm()
		}
	case "ctrl+s", "alt+enter":
		m.submitForm()
	default:
		field := &m.formFields[m.formActiveField]
		cursor := &m.formCursors[m.formActiveField]
		applyPaletteInputKey(key, field, cursor, false)
	}
	return m, nil
}

func (m *windowTimerPanelModel) toggleBoolField(idx int) {
	if string(m.formFields[idx]) == "yes" {
		m.formFields[idx] = []rune("no")
	} else {
		m.formFields[idx] = []rune("yes")
	}
}

func (m *windowTimerPanelModel) handleDeleteKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y", "Y":
		if err := deleteWindowTimer(m.deleteTimerID, m.deleteWindowID); err != nil {
			m.setStatus("delete failed: " + err.Error())
		} else {
			m.setStatus("timer deleted")
		}
		m.mode = windowTimerPanelModeList
		m.reload()
	case "n", "N", "esc", "g":
		m.mode = windowTimerPanelModeList
	}
	return m, nil
}

func (m *windowTimerPanelModel) toggleSelected() {
	if m.selected < 0 || m.selected >= len(m.timers) {
		return
	}
	t := m.timers[m.selected]
	if err := toggleWindowTimer(t.ID, m.windowID); err != nil {
		m.setStatus("toggle failed: " + err.Error())
	} else {
		if t.Enabled {
			m.setStatus("timer disabled")
		} else {
			m.setStatus("timer enabled")
		}
	}
	m.reload()
}

func (m *windowTimerPanelModel) openAddForm() {
	m.mode = windowTimerPanelModeAdd
	m.formTimerID = ""
	m.formTargetWindowID = m.windowID
	m.formActiveField = 0
	for i := range m.formFields {
		m.formFields[i] = nil
		m.formCursors[i] = 0
	}
	// Defaults: send Enter = yes, auto delete = no
	m.formFields[timerFormFieldSendEnter] = []rune("yes")
	m.formFields[timerFormFieldAutoDelete] = []rune("no")
}

func (m *windowTimerPanelModel) openEditForm() {
	if m.selected < 0 || m.selected >= len(m.timers) {
		return
	}
	m.formTargetWindowID = m.windowID
	m.prefillEditForm(m.timers[m.selected])
}

// prefillEditForm fills the form fields from t and enters Edit mode.
func (m *windowTimerPanelModel) prefillEditForm(t *windowTimer) {
	m.mode = windowTimerPanelModeEdit
	m.formTimerID = t.ID
	m.formActiveField = 0

	set := func(idx int, v string) {
		runes := []rune(v)
		m.formFields[idx] = runes
		m.formCursors[idx] = len(runes) // rune count — len(v) would be bytes and overshoot on CJK
	}
	set(timerFormFieldContent, t.Content)
	set(timerFormFieldTrigger, timerTriggerInput(t))
	set(timerFormFieldLoop, timerLoopInput(t))
	maxStr := "0"
	if t.MaxExecutions != 0 {
		maxStr = fmt.Sprintf("%d", t.MaxExecutions)
	}
	set(timerFormFieldMax, maxStr)
	yesNo := func(idx int, v bool) {
		if v {
			m.formFields[idx] = []rune("yes")
		} else {
			m.formFields[idx] = []rune("no")
		}
	}
	yesNo(timerFormFieldSendEnter, t.SendEnter)
	yesNo(timerFormFieldAutoDelete, t.DeleteOnDone)
}

func (m *windowTimerPanelModel) openDeleteConfirm() {
	if m.selected < 0 || m.selected >= len(m.timers) {
		return
	}
	m.deleteTimerID = m.timers[m.selected].ID
	m.deleteWindowID = m.windowID
	m.mode = windowTimerPanelModeDeleteConfirm
}

func (m *windowTimerPanelModel) submitForm() {
	content := strings.TrimSpace(string(m.formFields[timerFormFieldContent]))
	triggerStr := strings.TrimSpace(string(m.formFields[timerFormFieldTrigger]))
	loopStr := strings.TrimSpace(string(m.formFields[timerFormFieldLoop]))
	maxStr := strings.TrimSpace(string(m.formFields[timerFormFieldMax]))
	sendEnterStr := strings.TrimSpace(string(m.formFields[timerFormFieldSendEnter]))
	if maxStr == "" {
		maxStr = "0"
	}
	sendEnter := sendEnterStr != "no"
	autoDelete := strings.TrimSpace(string(m.formFields[timerFormFieldAutoDelete])) == "yes"

	if content == "" {
		m.setStatus("content cannot be empty")
		m.formActiveField = timerFormFieldContent
		return
	}
	if triggerStr == "" {
		m.setStatus("trigger cannot be empty (e.g. 5m, 13:10 or reset)")
		m.formActiveField = timerFormFieldTrigger
		return
	}

	var err error
	if m.mode == windowTimerPanelModeAdd {
		var t *windowTimer
		t, err = addWindowTimer(m.windowID, content, triggerStr, loopStr, maxStr, sendEnter, autoDelete)
		if err == nil {
			m.statusForAdded(t)
		}
	} else {
		err = updateWindowTimer(m.formTimerID, m.formTargetWindowID, content, triggerStr, loopStr, maxStr, sendEnter, autoDelete)
		if err == nil {
			m.setStatus("timer updated")
		}
	}
	if err != nil {
		m.setStatus("error: " + err.Error())
		return
	}
	m.mode = windowTimerPanelModeList
	m.reload()
}

// ── view ──────────────────────────────────────────────────────────────────────

func (m *windowTimerPanelModel) View() string {
	styles := newPaletteStyles()
	width := m.width
	height := m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 26
	}
	// Embedded picker takes over the whole view while open.
	if m.picker != nil {
		m.picker.width = width
		m.picker.height = height
		return m.picker.render(styles, width, height)
	}
	base := m.renderPanes(styles, width, height)
	switch m.mode {
	case windowTimerPanelModeAdd, windowTimerPanelModeEdit:
		return m.overlayFormModal(base, styles, width, height)
	case windowTimerPanelModeDeleteConfirm:
		return m.overlayDeleteModal(base, styles, width, height)
	case windowTimerPanelModeSaveSnippet:
		return m.overlaySaveSnippetModal(base, styles, width, height)
	default:
		return base
	}
}

// renderPanes lays out the todo-style 3-pane view: left column = This Window
// (top) + Other Windows (bottom), right column = History. The focused pane
// (m.viewMode) gets a highlighted label.
func (m *windowTimerPanelModel) renderPanes(styles paletteStyles, width, height int) string {
	inner := width - 2
	timezone := timerTimezoneSetting(loadAppConfig())
	titleRow := styles.title.Render("Timers " + timezone + " — " + truncateWidth(m.windowName, maxInt(8, inner-50)))
	if st := m.currentStatus(); st != "" {
		titleRow += "  " + styles.statusBad.Render(st)
	}
	meta := styles.muted.Render(fmt.Sprintf("This Window %d   Other Windows %d   History %d   ·  Tab switches pane",
		len(m.timers), len(m.otherTimers), len(m.history)))

	var keys [][2]string
	switch m.viewMode {
	case windowTimerViewAll:
		keys = [][2]string{{"j/k", "nav"}, {"Tab", "pane"}, {"Enter", "toggle"}, {"v", "copy here"}, {"e", "edit"}, {"x", "del"}, {"g/Esc", "back"}}
	case windowTimerViewHistory:
		keys = [][2]string{{"j/k", "nav"}, {"Tab", "pane"}, {"Enter", "recreate"}, {"v", "copy here"}, {"s", "★snippet"}, {"x", "del"}, {"g/Esc", "back"}}
	default:
		keys = [][2]string{{"j/k", "nav"}, {"Tab", "pane"}, {"Enter", "toggle"}, {"a", "add"}, {"r", "continue@reset"}, {"e", "edit"}, {"x", "del"}, {"g/Esc", "back"}}
	}
	renderSeg := func(pairs [][2]string) string {
		return renderShortcutPairs(
			func(v string) string { return styles.shortcutKey.Render(v) },
			func(v string) string { return styles.shortcutText.Render(v) },
			"   ", pairs,
		)
	}
	footer, footerLines := renderWrappedShortcutFooter(inner, renderSeg, 2, keys)

	bodyH := maxInt(6, height-3-footerLines)
	leftW := inner * 58 / 100
	rightW := inner - leftW - 1
	upperH := bodyH / 2
	lowerH := bodyH - upperH

	// Absolute Y layout: title(0) + meta(1) + blank(2) + body starts at 3.
	// Each pane wraps its label (row 0) + list. Padding(0,1) shifts content by 1 col.
	bodyStartY := 3
	m.thisFirstY = bodyStartY + 1 + 1       // label + column header
	m.thisListH = maxInt(0, (upperH-2)-1)   // renderTimerList reserves 1 for column header
	m.otherFirstY = bodyStartY + upperH + 1 // label only (no column header)
	m.otherListH = maxInt(0, lowerH-2)
	m.histFirstY = bodyStartY + 1 // label only
	m.histListH = maxInt(0, bodyH-2)
	m.leftPaneRightX = 1 + leftW // Padding(0,1) → +1

	thisPane := m.renderPane(styles, "This Window", m.renderTimerList(styles, leftW-2, upperH-2), leftW, upperH, m.viewMode == windowTimerViewActive)
	otherPane := m.renderPane(styles, "Other Windows", m.renderOtherList(styles, leftW-2, lowerH-2), leftW, lowerH, m.viewMode == windowTimerViewAll)
	leftCol := lipgloss.JoinVertical(lipgloss.Left, thisPane, otherPane)
	histPane := m.renderPane(styles, "History", m.renderHistoryList(styles, rightW-2, bodyH-2), rightW, bodyH, m.viewMode == windowTimerViewHistory)
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(leftW).Render(leftCol),
		" ",
		lipgloss.NewStyle().Width(rightW).Render(histPane),
	)
	view := lipgloss.JoinVertical(lipgloss.Left, titleRow, meta, "", body, footer)
	return lipgloss.NewStyle().Width(width).Height(height).Padding(0, 1).Render(view)
}

func (m *windowTimerPanelModel) renderPane(styles paletteStyles, label, body string, width, height int, focused bool) string {
	var head string
	if focused {
		head = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(lipgloss.Color("150")).Bold(true).Padding(0, 1).Render(label)
	} else {
		head = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true).Render(label)
	}
	content := lipgloss.JoinVertical(lipgloss.Left, head, body)
	return lipgloss.NewStyle().Width(width).Height(height).Render(content)
}

// renderOtherList renders the Other Windows pane: per timer, the owning window's
// status icon + name + dir, plus trigger and content.
func (m *windowTimerPanelModel) renderOtherList(styles paletteStyles, width, height int) string {
	if len(m.otherTimers) == 0 {
		return lipgloss.NewStyle().Width(width).Height(height).Render(styles.muted.Render("No timers in other windows."))
	}
	nameW := 12
	dirW := maxInt(6, (width-nameW-22)/2)
	contentWidth := maxInt(8, width-nameW-dirW-14)
	var lines []string
	for i, t := range m.otherTimers {
		if i >= maxInt(1, height) {
			break
		}
		info := m.windowInfo[t.WindowID]
		name := info.name
		if name == "" {
			name = t.WindowID
		}
		nameStr := padRight(truncateWidth(name, nameW-1), nameW)
		dirStr := padRight(truncateWidth(info.dir, dirW-1), dirW)
		trigStr := padRight(timerTriggerDisplay(t), 7)
		contentStr := truncateWidth(t.Content, contentWidth)
		enabled := "○"
		if t.Enabled {
			enabled = lipgloss.NewStyle().Foreground(lipgloss.Color("82")).Render("◆")
		}
		row := statusIcon(info.status) + " " +
			lipgloss.NewStyle().Foreground(lipgloss.Color("180")).Render(nameStr) +
			styles.muted.Render(dirStr) +
			enabled + " " +
			lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(trigStr) +
			styles.panelText.Render(contentStr)
		if i == m.otherSelected && m.viewMode == windowTimerViewAll {
			row = lipgloss.NewStyle().Width(width).Background(lipgloss.Color("238")).Render("›" + row)
		} else {
			row = lipgloss.NewStyle().Width(width).Render(" " + row)
		}
		lines = append(lines, row)
	}
	return strings.Join(lines, "\n")
}

func (m *windowTimerPanelModel) renderTimerList(styles paletteStyles, width, height int) string {
	if len(m.timers) == 0 {
		msg := styles.muted.Render("No timers. Press 'a' to add one.")
		return lipgloss.NewStyle().Width(width).Height(height).Render(msg)
	}

	hdrMuted := styles.muted
	contentWidth := maxInt(10, width-47)
	header := hdrMuted.Render(padRight("St", 3)) +
		hdrMuted.Render(padRight("Trigger", 8)) +
		hdrMuted.Render(padRight("Content", contentWidth)) +
		hdrMuted.Render(padRight("Loop", 12)) +
		hdrMuted.Render(padRight("Exec", 7)) +
		hdrMuted.Render("Next")
	header = lipgloss.NewStyle().Width(width).Render(header)

	listHeight := maxInt(1, height-1)
	var lines []string
	lines = append(lines, header)
	for i, t := range m.timers {
		if i >= listHeight {
			break
		}
		lines = append(lines, m.renderTimerRow(styles, t, i == m.selected, width))
	}
	return strings.Join(lines, "\n")
}

func (m *windowTimerPanelModel) renderTimerRow(styles paletteStyles, t *windowTimer, selected bool, width int) string {
	var stIcon string
	if t.Enabled {
		stIcon = "●"
	} else {
		stIcon = "○"
	}
	stStr := padRight(stIcon, 3)

	contentWidth := maxInt(10, width-47)
	triggerStr := padRight(timerTriggerDisplay(t), 8)
	contentStr := padRight(truncateWidth(t.Content, contentWidth), contentWidth)
	loopStr := padRight(timerLoopDisplay(t), 12)
	execStr := padRight(timerExecDisplay(t), 7)

	var nextStr string
	if !t.NextFireAt.IsZero() && t.Enabled {
		nextStr = formatWindowTimerTime(t.NextFireAt, "01/02 15:04")
	} else {
		nextStr = "--"
	}

	if selected {
		bg := lipgloss.Color("238")
		fg := lipgloss.Color("230")
		base := lipgloss.NewStyle().Background(bg)
		var enableFg lipgloss.Color
		if t.Enabled {
			enableFg = lipgloss.Color("82")
		} else {
			enableFg = lipgloss.Color("240")
		}
		out := "› " +
			base.Foreground(enableFg).Render(stStr) +
			base.Foreground(lipgloss.Color("214")).Render(triggerStr) +
			base.Foreground(fg).Bold(true).Render(contentStr) +
			base.Foreground(lipgloss.Color("110")).Render(loopStr) +
			base.Foreground(lipgloss.Color("244")).Render(execStr) +
			base.Foreground(lipgloss.Color("150")).Render(nextStr)
		return lipgloss.NewStyle().Width(width).Background(bg).Render(out)
	}

	var enableStyle lipgloss.Style
	if t.Enabled {
		enableStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	} else {
		enableStyle = styles.muted
	}
	out := "  " +
		enableStyle.Render(stStr) +
		lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(triggerStr) +
		styles.panelText.Render(contentStr) +
		lipgloss.NewStyle().Foreground(lipgloss.Color("110")).Render(loopStr) +
		styles.muted.Render(execStr) +
		lipgloss.NewStyle().Foreground(lipgloss.Color("150")).Render(nextStr)
	return lipgloss.NewStyle().Width(width).Render(out)
}

// overlayFormModal renders the add/edit form as a centered modal over the list.
func (m *windowTimerPanelModel) overlayFormModal(base string, styles paletteStyles, width, height int) string {
	isEdit := m.mode == windowTimerPanelModeEdit
	title := "Add Timer"
	if isEdit {
		title = "Edit Timer"
	}

	triggerRaw := strings.TrimSpace(string(m.formFields[timerFormFieldTrigger]))
	trigMode, _, _, _ := parseTrigger(triggerRaw)

	type fieldDef struct {
		label  string
		hint   string
		idx    int
		isBool bool
	}
	fields := []fieldDef{
		{"Content", "text to type into the pane", timerFormFieldContent, false},
		{"Trigger", "5m  1h30m  13:10  reset", timerFormFieldTrigger, false},
		{"Loop", loopHintForMode(trigMode), timerFormFieldLoop, false},
		{"Max exec", "0 = unlimited", timerFormFieldMax, false},
		{"Send Enter", "Space/y/n to toggle", timerFormFieldSendEnter, true},
		{"Auto del", "delete after done / max reached", timerFormFieldAutoDelete, true},
	}

	modalWidth := minInt(66, width-4)
	innerWidth := modalWidth - 4 // 2 border + 2 padding

	var rows []string
	titleLine := styles.panelTitle.Render(title + " — " + truncateWidth(m.windowName, innerWidth-12))
	if st := m.currentStatus(); st != "" {
		titleLine += "  " + styles.statusBad.Render(st)
	}
	rows = append(rows, titleLine, "")

	for _, f := range fields {
		active := m.formActiveField == f.idx
		marker := "  "
		if active {
			marker = "› "
		}
		labelStyle := styles.muted
		if active {
			labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
		}
		labelPad := padRight(f.label+":", 11)

		var valueStr string
		if f.isBool {
			v := string(m.formFields[f.idx])
			if v == "" {
				v = "yes"
			}
			if v == "yes" {
				valueStr = lipgloss.NewStyle().Foreground(lipgloss.Color("82")).Render("● yes")
			} else {
				valueStr = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("○ no")
			}
		} else {
			runes := m.formFields[f.idx]
			cursor := m.formCursors[f.idx]
			valueStr = styles.input.Render(renderInputValue(runes, cursor, styles))
		}

		row := marker + labelStyle.Render(labelPad) + " " + valueStr
		if f.hint != "" && active && !f.isBool {
			row += "  " + styles.muted.Render("("+f.hint+")")
		}
		rows = append(rows, row)
	}

	rows = append(rows, "", styles.muted.Render("Tab: next   Ctrl+P: from snippet   Ctrl+S: save   Esc: cancel"))

	content := lipgloss.JoinVertical(lipgloss.Left, rows...)
	modal := lipgloss.NewStyle().
		Border(paletteModalBorder).
		BorderForeground(lipgloss.Color("63")).
		Width(innerWidth).
		Padding(0, 1).
		Render(content)

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

// overlayDeleteModal renders the delete confirmation as a centered modal over the list.
func (m *windowTimerPanelModel) overlayDeleteModal(base string, styles paletteStyles, width, height int) string {
	var toDelete *windowTimer
	for _, t := range m.timers {
		if t.ID == m.deleteTimerID {
			toDelete = t
			break
		}
	}

	var rows []string
	rows = append(rows, styles.panelTitle.Render("Delete Timer"), "")
	if toDelete != nil {
		rows = append(rows,
			styles.panelText.Render("Content: "+truncateWidth(toDelete.Content, 40)),
			styles.panelText.Render("Trigger: "+timerTriggerDisplay(toDelete)),
			"",
		)
	}
	rows = append(rows, styles.statusBad.Render("Delete this timer?  y / n"))

	content := lipgloss.JoinVertical(lipgloss.Left, rows...)
	modal := lipgloss.NewStyle().
		Border(paletteModalBorder).
		BorderForeground(lipgloss.Color("196")).
		Width(50).
		Padding(0, 1).
		Render(content)

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

// renderHistory renders the per-window content history list.
func (m *windowTimerPanelModel) renderHistoryList(styles paletteStyles, width, height int) string {
	if len(m.history) == 0 {
		msg := styles.muted.Render("No history yet. Created/edited timers are remembered here.")
		return lipgloss.NewStyle().Width(width).Height(height).Render(msg)
	}
	contentWidth := maxInt(10, width-26)
	var lines []string
	for i, e := range m.history {
		if i >= height {
			break
		}
		meta := padRight(e.Trigger, 8)
		if e.Loop != "" {
			meta = padRight(e.Trigger+"·"+e.Loop, 8)
		}
		contentStr := padRight(truncateWidth(e.Content, contentWidth), contentWidth)
		used := "×" + strconv.Itoa(e.UseCount)
		if i == m.histSelected {
			bg := lipgloss.Color("238")
			base := lipgloss.NewStyle().Background(bg)
			out := "› " +
				base.Foreground(lipgloss.Color("214")).Render(padRight(meta, 10)) +
				base.Foreground(lipgloss.Color("230")).Bold(true).Render(contentStr) +
				base.Foreground(lipgloss.Color("244")).Render(used)
			lines = append(lines, lipgloss.NewStyle().Width(width).Background(bg).Render(out))
			continue
		}
		out := "  " +
			lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(padRight(meta, 10)) +
			styles.panelText.Render(contentStr) +
			styles.muted.Render(used)
		lines = append(lines, lipgloss.NewStyle().Width(width).Render(out))
	}
	return strings.Join(lines, "\n")
}

// overlaySaveSnippetModal renders the "save history entry to snippet" form.
func (m *windowTimerPanelModel) overlaySaveSnippetModal(base string, styles paletteStyles, width, height int) string {
	labels := []string{"Name", "Group"}
	hints := []string{"snippet name (no / . _)", "default: timer"}
	var rows []string
	titleLine := styles.panelTitle.Render("Save to Snippet")
	if st := m.currentStatus(); st != "" {
		titleLine += "  " + styles.statusBad.Render(st)
	}
	rows = append(rows, titleLine, "")
	for i := 0; i < 2; i++ {
		active := m.saveActiveField == i
		marker := "  "
		if active {
			marker = "› "
		}
		labelStyle := styles.muted
		if active {
			labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
		}
		valueStr := styles.input.Render(renderInputValue(m.saveFields[i], m.saveCursors[i], styles))
		row := marker + labelStyle.Render(padRight(labels[i]+":", 8)) + " " + valueStr
		if active && hints[i] != "" {
			row += "  " + styles.muted.Render("("+hints[i]+")")
		}
		rows = append(rows, row)
	}
	rows = append(rows, "", styles.muted.Render("Tab: next   Ctrl+S/Enter: save   Esc: cancel"))
	content := lipgloss.JoinVertical(lipgloss.Left, rows...)
	modal := lipgloss.NewStyle().Border(paletteModalBorder).BorderForeground(lipgloss.Color("63")).Width(minInt(60, width-4)).Padding(0, 1).Render(content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func padRight(s string, w int) string {
	cw := lipgloss.Width(s)
	if cw >= w {
		return s
	}
	return s + strings.Repeat(" ", w-cw)
}

func loopHintForMode(mode windowTimerTriggerMode) string {
	switch mode {
	case windowTimerTriggerTime:
		return "daily  or empty (none)"
	case windowTimerTriggerQuota:
		return "reset (each quota reset)  or empty (once)"
	}
	return "5m  1h  or empty (none)"
}

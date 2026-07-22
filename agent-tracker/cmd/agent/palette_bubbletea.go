package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var paletteModalBorder = lipgloss.Border{
	Top:         "─",
	Bottom:      "─",
	Left:        "│",
	Right:       "│",
	TopLeft:     "┌",
	TopRight:    "┐",
	BottomLeft:  "└",
	BottomRight: "┘",
}

var paletteTmuxRunner = runTmux
var paletteTmuxOutput = runTmuxOutput

type paletteRuntime struct {
	windowID           string
	agentID            string
	openMode           string // mode name to open directly, e.g. "windows"
	startupMessage     string
	currentPath        string
	currentSessionName string
	currentWindowName  string
}

type paletteModel struct {
	runtime                 *paletteRuntime
	state                   paletteUIState
	actions                 []paletteAction
	openedAt                time.Time
	quickSecondaryEscCloses bool
	width                   int
	height                  int
	result                  paletteResult
	pendingInitCmd          tea.Cmd // cmd from sub-panel opened at launch, returned by Init()
	todo                    *todoPanelModel
	activity                *activityMonitorBT
	status                  *statusRightPanelModel
	settings                *settingsPanelModel
	general                 *generalPanelModel
	windowResize            *windowResizePanelModel
	windowTitle             *windowTitlePanelModel
	windowNav               *windowNavPanelModel
	snippet                 *snippetPanelModel
}

type paletteStyles struct {
	title          lipgloss.Style
	meta           lipgloss.Style
	searchBox      lipgloss.Style
	searchPrompt   lipgloss.Style
	input          lipgloss.Style
	inputCursor    lipgloss.Style
	item           lipgloss.Style
	selectedItem   lipgloss.Style
	sectionLabel   lipgloss.Style
	selectedLabel  lipgloss.Style
	itemTitle      lipgloss.Style
	itemSubtitle   lipgloss.Style
	selectedSubtle lipgloss.Style
	panelTitle     lipgloss.Style
	panelText      lipgloss.Style
	muted          lipgloss.Style
	footer         lipgloss.Style
	keyword        lipgloss.Style
	modal          lipgloss.Style
	modalTitle     lipgloss.Style
	modalBody      lipgloss.Style
	modalHint      lipgloss.Style
	statusBad      lipgloss.Style
	statLabel      lipgloss.Style
	statValue      lipgloss.Style
	todoCheck      lipgloss.Style
	todoCheckDone  lipgloss.Style
	panelTextDone  lipgloss.Style
	shortcutKey    lipgloss.Style
	shortcutText   lipgloss.Style
}

type paletteTodoPreviewItem struct {
	Title string
	Done  bool
}

type paletteTodoPreviewSection struct {
	Title string
	Lead  string
	Items []paletteTodoPreviewItem
	Empty string
}

func runBubbleTeaPalette(args []string) error {
	runtime, err := loadPaletteRuntime(args)
	if err != nil {
		return err
	}
	initialMode := paletteModeList
	if runtime.openMode != "" {
		if m := paletteOpenModeToState(runtime.openMode); m != paletteModeList {
			initialMode = m
		}
	}
	state := paletteUIState{Mode: initialMode, Message: runtime.startupMessage}
	for {
		model := newPaletteModel(runtime, state)
		finalModel, err := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
		if err != nil {
			return err
		}
		final, ok := finalModel.(*paletteModel)
		if !ok {
			return fmt.Errorf("unexpected palette model type")
		}
		state = final.result.State
		switch final.result.Kind {
		case paletteResultClose:
			return nil
		case paletteResultOpenActivityMonitor:
			err := runtime.runActivityMonitor()
			if errors.Is(err, errClosePalette) {
				return nil
			}
			state.Mode = paletteModeList
			state.Message = paletteMessageForError(err)
			continue
		case paletteResultOpenSnippets:
			state.Mode = paletteModeSnippets
			state.Filter = nil
			state.FilterCursor = 0
			state.Selected = 0
			state.Message = ""
			continue
		case paletteResultRunAction:
			reopen, message, err := runtime.execute(final.result)
			if err != nil {
				if reopen {
					state.Mode = paletteModeList
					state.Message = err.Error()
					continue
				}
				return err
			}
			if !reopen {
				return nil
			}
			state.Mode = paletteModeList
			state.Message = message
			continue
		default:
			return nil
		}
	}
}

// paletteOpenModeToState maps --open flag values to the corresponding paletteMode.
func paletteOpenModeToState(name string) paletteMode {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "activity":
		return paletteModeActivity
	case "settings":
		return paletteModeSettings
	case "general":
		return paletteModeGeneral
	case "window-resize", "windowresize", "resize":
		return paletteModeWindowResize
	case "window-title", "windowtitle":
		return paletteModeWindowTitle
	case "status", "statusright", "status-right":
		return paletteModeStatusRight
	case "windows", "window-nav", "windownav":
		return paletteModeWindowNav
	case "snippets":
		return paletteModeSnippets
	case "todos":
		return paletteModeTodos
	}
	return paletteModeList
}

func loadPaletteRuntime(args []string) (*paletteRuntime, error) {
	fs := flag.NewFlagSet("agent palette", flag.ContinueOnError)
	var windowID string
	var agentID string
	var currentPath string
	var currentSessionName string
	var currentWindowName string
	var openMode string
	fs.StringVar(&windowID, "window", "", "window id")
	fs.StringVar(&agentID, "agent-id", "", "agent id")
	fs.StringVar(&currentPath, "path", "", "current pane path")
	fs.StringVar(&currentSessionName, "session-name", "", "current session name")
	fs.StringVar(&currentWindowName, "window-name", "", "current window name")
	fs.StringVar(&openMode, "open", "", "open directly to sub-panel: windows, activity, settings, general, window-resize, window-title, status")
	fs.SetOutput(nil)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	runtime := &paletteRuntime{
		windowID:           firstNonEmpty(windowID, os.Getenv("AGENT_PALETTE_WINDOW_ID")),
		agentID:            firstNonEmpty(agentID, os.Getenv("AGENT_PALETTE_AGENT_ID")),
		openMode:           firstNonEmpty(openMode, os.Getenv("AGENT_PALETTE_OPEN_MODE")),
		currentPath:        firstNonEmpty(currentPath, os.Getenv("AGENT_PALETTE_PATH")),
		currentSessionName: firstNonEmpty(currentSessionName, os.Getenv("AGENT_PALETTE_SESSION_NAME")),
		currentWindowName:  firstNonEmpty(currentWindowName, os.Getenv("AGENT_PALETTE_WINDOW_NAME")),
	}
	logPaletteLaunchIfMalformed(runtime)
	if looksLikeTmuxFormatLiteral(runtime.agentID) {
		runtime.agentID = ""
	}
	if err := runtime.reload(); err != nil {
		return nil, err
	}
	return runtime, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func logPaletteLaunchIfMalformed(runtime *paletteRuntime) {
	if runtime == nil {
		return
	}
	values := []string{
		runtime.windowID,
		runtime.agentID,
		runtime.currentPath,
		runtime.currentSessionName,
		runtime.currentWindowName,
	}
	for _, value := range values {
		if strings.Contains(value, "#{") {
			file, err := os.OpenFile("/tmp/agent-palette-launch.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return
			}
			defer file.Close()
			_, _ = fmt.Fprintf(file, "%s window=%q agent=%q path=%q session=%q window_name=%q args=%q\n",
				time.Now().Format(time.RFC3339Nano),
				runtime.windowID,
				runtime.agentID,
				runtime.currentPath,
				runtime.currentSessionName,
				runtime.currentWindowName,
				os.Args,
			)
			return
		}
	}
}

func (r *paletteRuntime) reload() error {
	r.startupMessage = ""
	if looksLikeTmuxFormatLiteral(r.agentID) {
		r.agentID = ""
	}
	tmuxValue := func(target string, format string) string {
		args := []string{"display-message", "-p"}
		if strings.TrimSpace(target) != "" {
			args = append(args, "-t", strings.TrimSpace(target))
		}
		args = append(args, format)
		out, err := runTmuxOutput(args...)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}
	if strings.TrimSpace(r.windowID) == "" {
		r.windowID = tmuxValue("", "#{window_id}")
	}
	if strings.TrimSpace(r.currentPath) == "" {
		r.currentPath = tmuxValue(r.windowID, "#{pane_current_path}")
	}
	if strings.TrimSpace(r.currentSessionName) == "" {
		r.currentSessionName = tmuxValue(r.windowID, "#{session_name}")
	}
	if strings.TrimSpace(r.currentWindowName) == "" {
		r.currentWindowName = tmuxValue(r.windowID, "#{window_name}")
	}
	return nil
}

func (r *paletteRuntime) buildActions() []paletteAction {
	actions := []paletteAction{
		{
			Section:  "Navigation",
			Title:    "Windows",
			Subtitle: "Browse and switch between all session windows",
			Keywords: []string{"window", "switch", "navigate", "session", "tab"},
			Kind:     paletteActionOpenWindowNav,
		},
	}
	actions = append(actions,
		paletteAction{
			Section:  "System",
			Title:    "Activity Monitor",
			Subtitle: "View CPU, memory and process usage",
			Keywords: []string{"activity", "monitor", "cpu", "memory", "processes", "top", "ps"},
			Kind:     paletteActionOpenActivityMonitor,
		},
		paletteAction{
			Section:  "System",
			Title:    "Paste snippet",
			Subtitle: "Search and paste a snippet into the current pane",
			Keywords: []string{"snippet", "paste", "template", "text", "insert"},
			Kind:     paletteActionOpenSnippets,
		},
		paletteAction{
			Section:  "System",
			Title:    "Todos",
			Subtitle: "Manage window/global todos",
			Keywords: []string{"todo", "task", "checklist", "manage"},
			Kind:     paletteActionOpenTodos,
		},
		paletteAction{
			Section:  "System",
			Title:    "Reload tmux config",
			Subtitle: "Source ~/.tmux.conf",
			Keywords: []string{"tmux", "reload", "config", "source", "refresh"},
			Kind:     paletteActionReloadTmuxConfig,
		},
		paletteAction{
			Section:  "System",
			Title:    "Settings",
			Subtitle: "General, window/resize, status bar, and title configuration",
			Keywords: []string{"settings", "tmux", "resize", "layout", "status", "bottom", "window", "title", "model", "display"},
			Kind:     paletteActionOpenSettings,
		},
	)
	return actions
}

func (r *paletteRuntime) runActivityMonitor() error {
	return runBubbleTeaActivityMonitor(r.windowID)
}

func (r *paletteRuntime) execute(result paletteResult) (bool, string, error) {
	action := result.Action
	switch action.Kind {
	case paletteActionReloadTmuxConfig:
		return false, "", paletteTmuxRunner("source-file", os.Getenv("HOME")+"/.tmux.conf")
	default:
		return false, "", nil
	}
}

func statusRightModuleLabel(module string) string {
	switch module {
	case statusRightModuleCPU:
		return "CPU"
	case statusRightModuleNetwork:
		return "Network"
	case statusRightModuleMemory:
		return "Memory"
	case statusRightModuleMemoryTotals:
		return "Tmux Memory"
	case statusRightModuleTodoPreview:
		return "Todo Preview"
	case statusRightModuleTodos:
		return "Todos"
	case statusRightModuleFlashMoe:
		return "Flash-MoE"
	case statusRightModuleHost:
		return "Host"
	default:
		return module
	}
}

func statusRightModuleDescription(module string) string {
	switch module {
	case statusRightModuleCPU:
		return "CPU usage"
	case statusRightModuleNetwork:
		return "network throughput"
	case statusRightModuleMemory:
		return "pane memory stats"
	case statusRightModuleMemoryTotals:
		return "window, session, and total tmux memory"
	case statusRightModuleTodoPreview:
		return "append the first open window todo to Todos"
	case statusRightModuleTodos:
		return "todo count"
	case statusRightModuleFlashMoe:
		return "Flash-MoE status"
	case statusRightModuleHost:
		return "hostname"
	default:
		return module
	}
}

func togglePaletteStatusRightModule(module string) error {
	if err := toggleStatusRightModule(module); err != nil {
		return err
	}
	return paletteTmuxRunner("refresh-client", "-S")
}

func newPaletteModel(runtime *paletteRuntime, state paletteUIState) *paletteModel {
	if state.Mode == 0 {
		state.Mode = paletteModeList
	}
	state.FilterCursor = clampInt(state.FilterCursor, 0, len(state.Filter))
	model := &paletteModel{runtime: runtime, state: state, actions: runtime.buildActions(), openedAt: time.Now()}
	if state.Mode == paletteModeTodos {
		_ = model.openTodosPanel()
	}
	if state.Mode == paletteModeActivity {
		cmd, _ := model.openActivityPanel()
		model.pendingInitCmd = cmd
	}
	if state.Mode == paletteModeStatusRight {
		model.openStatusRightPanel()
	}
	if state.Mode == paletteModeWindowNav {
		model.openWindowNavPanel()
	}
	if state.Mode == paletteModeGeneral {
		model.openGeneralPanel()
	}
	if state.Mode == paletteModeWindowResize {
		model.openWindowResizePanel()
	}
	return model
}

func (m *paletteModel) Init() tea.Cmd {
	cmd := m.pendingInitCmd
	m.pendingInitCmd = nil
	return cmd
}

func (m *paletteModel) noteSecondaryPageOpen() {
	m.quickSecondaryEscCloses = time.Since(m.openedAt) <= 800*time.Millisecond
}

func (m *paletteModel) closePalette() (tea.Model, tea.Cmd) {
	m.result = paletteResult{Kind: paletteResultClose, State: m.state}
	return m, tea.Quit
}

func (m *paletteModel) openTodosPanel() error {
	m.noteSecondaryPageOpen()
	sessionID, windowID := getCurrentTmuxScopeInfo()
	if m.todo == nil {
		panel, err := newTodoPanelModel(sessionID, windowID)
		if err != nil {
			return err
		}
		m.todo = panel
	} else {
		m.todo.sessionID = strings.TrimSpace(sessionID)
		m.todo.windowID = strings.TrimSpace(windowID)
		m.todo.reloadEntries()
		m.todo.clampSelections()
		m.todo.setFocusedPane(todoPanelPaneWindow)
		m.todo.mode = todoPanelModeList
	}
	m.todo.showAltHints = false
	m.state.Mode = paletteModeTodos
	m.state.Message = ""
	m.state.ShowAltHints = false
	return nil
}

func (m *paletteModel) openSnippetsPanel() {
	m.noteSecondaryPageOpen()
	if m.snippet == nil {
		m.snippet = newSnippetPanelModel(false, "")
	} else {
		m.snippet.requestBack = false
		m.snippet.reloadKeepingPath("")
	}
	m.snippet.width = m.width
	m.snippet.height = m.height
	m.state.Mode = paletteModeSnippets
	m.state.Message = ""
	m.state.ShowAltHints = false
}

func (m *paletteModel) openActivityPanel() (tea.Cmd, error) {
	m.noteSecondaryPageOpen()
	if m.activity == nil {
		m.activity = newActivityMonitorModel(m.runtime.windowID, true)
	} else {
		m.activity.windowID = strings.TrimSpace(m.runtime.windowID)
		m.activity.requestBack = false
		m.activity.requestClose = false
	}
	m.activity.width = m.width
	m.activity.height = m.height
	m.activity.showAltHints = false
	m.state.Mode = paletteModeActivity
	m.state.Message = ""
	m.state.ShowAltHints = false
	if !m.activity.refreshInFlight {
		return tea.Batch(
			activityRequestRefreshBT(true, m.activity.refreshedAt.IsZero(), m.activity),
			activityTickCmd(),
		), nil
	}
	return nil, nil
}

func (m *paletteModel) openStatusRightPanel() {
	m.noteSecondaryPageOpen()
	if m.status == nil {
		m.status = newStatusRightPanelModel()
	} else {
		m.status.reload()
		m.status.requestBack = false
	}
	m.status.showAltHints = false
	m.state.Mode = paletteModeStatusRight
	m.state.Message = ""
	m.state.ShowAltHints = false
}

func (m *paletteModel) openSettingsPanel() {
	m.noteSecondaryPageOpen()
	if m.settings == nil {
		m.settings = newSettingsPanelModel()
	} else {
		m.settings.requestBack = false
		m.settings.openMode = 0
	}
	m.settings.width = m.width
	m.settings.height = m.height
	m.state.Mode = paletteModeSettings
	m.state.Message = ""
	m.state.ShowAltHints = false
}

func (m *paletteModel) openGeneralPanel() {
	m.noteSecondaryPageOpen()
	if m.general == nil {
		m.general = newGeneralPanelModel()
	} else {
		m.general.reload()
		m.general.requestBack = false
	}
	m.general.width = m.width
	m.general.height = m.height
	m.state.Mode = paletteModeGeneral
	m.state.Message = ""
	m.state.ShowAltHints = false
}

func (m *paletteModel) openWindowResizePanel() {
	m.noteSecondaryPageOpen()
	if m.windowResize == nil {
		m.windowResize = newWindowResizePanelModel(m.runtime.windowID)
	} else {
		m.windowResize.windowID = strings.TrimSpace(m.runtime.windowID)
		m.windowResize.reload()
		m.windowResize.requestBack = false
	}
	m.windowResize.width = m.width
	m.windowResize.height = m.height
	m.state.Mode = paletteModeWindowResize
	m.state.Message = ""
	m.state.ShowAltHints = false
}

func (m *paletteModel) openWindowTitlePanel() {
	m.noteSecondaryPageOpen()
	if m.windowTitle == nil {
		m.windowTitle = newWindowTitlePanelModel()
	} else {
		m.windowTitle.reload()
		m.windowTitle.requestBack = false
	}
	m.windowTitle.width = m.width
	m.windowTitle.height = m.height
	m.state.Mode = paletteModeWindowTitle
	m.state.Message = ""
	m.state.ShowAltHints = false
}

func (m *paletteModel) openWindowNavPanel() {
	m.noteSecondaryPageOpen()
	if m.windowNav == nil {
		m.windowNav = newWindowNavPanelModel()
	} else {
		m.windowNav.requestBack = false
		m.windowNav.requestClose = false
		m.windowNav.refresh()
	}
	m.windowNav.width = m.width
	m.windowNav.height = m.height
	m.windowNav.directMode = false
	m.state.Mode = paletteModeWindowNav
	m.state.Message = ""
	m.state.ShowAltHints = false
}

func (m *paletteModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.todo != nil {
			m.todo.width = msg.Width
			m.todo.height = msg.Height
		}
		if m.activity != nil {
			m.activity.width = msg.Width
			m.activity.height = msg.Height
		}
		if m.status != nil {
			m.status.width = msg.Width
			m.status.height = msg.Height
		}
		if m.settings != nil {
			m.settings.width = msg.Width
			m.settings.height = msg.Height
		}
		if m.windowTitle != nil {
			m.windowTitle.width = msg.Width
			m.windowTitle.height = msg.Height
		}
		if m.general != nil {
			m.general.width = msg.Width
			m.general.height = msg.Height
		}
		if m.windowResize != nil {
			m.windowResize.width = msg.Width
			m.windowResize.height = msg.Height
		}
		if m.windowNav != nil {
			m.windowNav.width = msg.Width
			m.windowNav.height = msg.Height
		}
		if m.snippet != nil {
			m.snippet.width = msg.Width
			m.snippet.height = msg.Height
		}
	case tea.KeyMsg:
		if m.state.Mode != paletteModeActivity && m.state.Mode != paletteModeTodos && m.state.Mode != paletteModeStatusRight {
			if isAltFooterToggleKey(msg) {
				m.state.ShowAltHints = !m.state.ShowAltHints
				return m, nil
			}
			m.state.ShowAltHints = false
		}
		key := paletteKeyString(msg)
		if key == "alt+s" {
			if time.Since(m.openedAt) < 250*time.Millisecond {
				return m, nil
			}
			return m.closePalette()
		}
		if key == "esc" && m.quickSecondaryEscCloses {
			switch m.state.Mode {
			case paletteModeTodos:
				if m.todo != nil && m.todo.mode == todoPanelModeList {
					return m.closePalette()
				}
			case paletteModeActivity:
				return m.closePalette()
			case paletteModeStatusRight:
				return m.closePalette()
			case paletteModeSnippets:
				if m.snippet != nil && m.snippet.mode == snippetPanelModeList {
					return m.closePalette()
				}
			case paletteModeSettings:
				return m.closePalette()
			case paletteModeWindowNav:
				return m.closePalette()
			}
		}
		if m.state.Mode == paletteModeActivity {
			if m.activity == nil {
				cmd, err := m.openActivityPanel()
				if err != nil {
					m.state.Mode = paletteModeList
					m.state.Message = err.Error()
					return m, nil
				}
				return m, cmd
			}
			model, cmd := m.activity.Update(msg)
			if updated, ok := model.(*activityMonitorBT); ok {
				m.activity = updated
			}
			if m.activity.requestClose {
				m.result = paletteResult{Kind: paletteResultClose, State: m.state}
				return m, tea.Quit
			}
			if m.activity.requestBack {
				m.activity.requestBack = false
				m.state.Mode = paletteModeList
				m.state.Message = m.activity.currentStatus()
				return m, nil
			}
			return m, cmd
		}
		if m.state.Mode == paletteModeTodos {
			if key == "esc" && m.todo != nil && m.todo.mode == todoPanelModeList {
				m.state.Mode = paletteModeList
				m.state.Message = m.todo.currentStatus()
				return m, nil
			}
			if m.todo == nil {
				if err := m.openTodosPanel(); err != nil {
					m.state.Mode = paletteModeList
					m.state.Message = err.Error()
					return m, nil
				}
			}
			model, cmd := m.todo.Update(msg)
			if updated, ok := model.(*todoPanelModel); ok {
				m.todo = updated
			}
			return m, cmd
		}
		if m.state.Mode == paletteModeSettings {
			if m.settings == nil {
				m.openSettingsPanel()
			}
			model, cmd := m.settings.Update(msg)
			if updated, ok := model.(*settingsPanelModel); ok {
				m.settings = updated
			}
			if m.settings.requestBack {
				m.settings.requestBack = false
				m.state.Mode = paletteModeList
				return m, nil
			}
			if m.settings.openMode != 0 {
				opened := m.settings.openMode
				m.settings.openMode = 0
				switch opened {
				case paletteModeGeneral:
					m.openGeneralPanel()
				case paletteModeWindowResize:
					m.openWindowResizePanel()
				case paletteModeStatusRight:
					m.openStatusRightPanel()
				case paletteModeWindowTitle:
					m.openWindowTitlePanel()
				}
				return m, nil
			}
			return m, cmd
		}
		if m.state.Mode == paletteModeGeneral {
			if m.general == nil {
				m.openGeneralPanel()
			}
			model, cmd := m.general.Update(msg)
			if updated, ok := model.(*generalPanelModel); ok {
				m.general = updated
			}
			if m.general.requestBack {
				m.general.requestBack = false
				m.state.Mode = paletteModeSettings
				return m, nil
			}
			return m, cmd
		}
		if m.state.Mode == paletteModeWindowResize {
			if m.windowResize == nil {
				m.openWindowResizePanel()
			}
			model, cmd := m.windowResize.Update(msg)
			if updated, ok := model.(*windowResizePanelModel); ok {
				m.windowResize = updated
			}
			if m.windowResize.requestBack {
				m.windowResize.requestBack = false
				m.state.Mode = paletteModeSettings
				return m, nil
			}
			return m, cmd
		}
		if m.state.Mode == paletteModeWindowTitle {
			if m.windowTitle == nil {
				m.openWindowTitlePanel()
			}
			model, cmd := m.windowTitle.Update(msg)
			if updated, ok := model.(*windowTitlePanelModel); ok {
				m.windowTitle = updated
			}
			if m.windowTitle.requestBack {
				m.windowTitle.requestBack = false
				m.state.Mode = paletteModeSettings
				return m, nil
			}
			return m, cmd
		}
		if m.state.Mode == paletteModeStatusRight {
			if m.status == nil {
				m.openStatusRightPanel()
			}
			model, cmd := m.status.Update(msg)
			if updated, ok := model.(*statusRightPanelModel); ok {
				m.status = updated
			}
			if m.status.requestBack {
				m.status.requestBack = false
				m.state.Mode = paletteModeSettings // was paletteModeList
				m.state.Message = m.status.currentStatus()
				return m, nil
			}
			return m, cmd
		}
		if m.state.Mode == paletteModeWindowNav {
			if m.windowNav == nil {
				m.openWindowNavPanel()
			}
			model, cmd := m.windowNav.Update(msg)
			if updated, ok := model.(*windowNavPanelModel); ok {
				m.windowNav = updated
			}
			if m.windowNav.requestClose {
				m.result = paletteResult{Kind: paletteResultClose, State: m.state}
				return m, tea.Quit
			}
			if m.windowNav.requestBack {
				m.windowNav.requestBack = false
				m.state.Mode = paletteModeList
				return m, nil
			}
			return m, cmd
		}
		if m.state.Mode == paletteModeSnippets {
			if m.snippet == nil {
				m.openSnippetsPanel()
			}
			model, cmd := m.snippet.Update(msg)
			if updated, ok := model.(*snippetPanelModel); ok {
				m.snippet = updated
			}
			if m.snippet.requestClose {
				m.snippet.requestClose = false
				return m.closePalette()
			}
			if m.snippet.requestBack {
				m.snippet.requestBack = false
				m.state.Mode = paletteModeList
				m.state.Message = m.snippet.currentStatus()
				return m, nil
			}
			return m, cmd
		}
		return m.updateList(key)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	if m.state.Mode == paletteModeActivity && m.activity != nil {
		model, cmd := m.activity.Update(msg)
		if updated, ok := model.(*activityMonitorBT); ok {
			m.activity = updated
		}
		if m.activity.requestClose {
			m.result = paletteResult{Kind: paletteResultClose, State: m.state}
			return m, tea.Quit
		}
		if m.activity.requestBack {
			m.activity.requestBack = false
			m.state.Mode = paletteModeList
			m.state.Message = m.activity.currentStatus()
			return m, nil
		}
		return m, cmd
	}
	if m.state.Mode == paletteModeTodos && m.todo != nil {
		model, cmd := m.todo.Update(msg)
		if updated, ok := model.(*todoPanelModel); ok {
			m.todo = updated
		}
		return m, cmd
	}
	if m.state.Mode == paletteModeStatusRight && m.status != nil {
		model, cmd := m.status.Update(msg)
		if updated, ok := model.(*statusRightPanelModel); ok {
			m.status = updated
		}
		if m.status.requestBack {
			m.status.requestBack = false
			m.state.Mode = paletteModeSettings
			m.state.Message = m.status.currentStatus()
			return m, nil
		}
		return m, cmd
	}
	if m.state.Mode == paletteModeWindowNav && m.windowNav != nil {
		model, cmd := m.windowNav.Update(msg)
		if updated, ok := model.(*windowNavPanelModel); ok {
			m.windowNav = updated
		}
		if m.windowNav.requestClose {
			m.result = paletteResult{Kind: paletteResultClose, State: m.state}
			return m, tea.Quit
		}
		if m.windowNav.requestBack {
			m.windowNav.requestBack = false
			m.state.Mode = paletteModeList
			return m, nil
		}
		return m, cmd
	}
	return m, nil
}

// handleMouse routes mouse events by state.Mode: sub-panels handle their own
// modes, and the main command list is hit-tested against the actions block.
func (m *paletteModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch m.state.Mode {
	case paletteModeActivity:
		if m.activity == nil {
			return m, nil
		}
		model, cmd := m.activity.Update(msg)
		if updated, ok := model.(*activityMonitorBT); ok {
			m.activity = updated
		}
		if m.activity.requestClose {
			m.result = paletteResult{Kind: paletteResultClose, State: m.state}
			return m, tea.Quit
		}
		if m.activity.requestBack {
			m.activity.requestBack = false
			m.state.Mode = paletteModeList
			m.state.Message = m.activity.currentStatus()
			return m, nil
		}
		return m, cmd
	case paletteModeTodos:
		if m.todo == nil {
			return m, nil
		}
		model, cmd := m.todo.Update(msg)
		if updated, ok := model.(*todoPanelModel); ok {
			m.todo = updated
		}
		return m, cmd
	case paletteModeWindowNav:
		if m.windowNav == nil {
			return m, nil
		}
		model, cmd := m.windowNav.Update(msg)
		if updated, ok := model.(*windowNavPanelModel); ok {
			m.windowNav = updated
		}
		if m.windowNav.requestClose {
			m.result = paletteResult{Kind: paletteResultClose, State: m.state}
			return m, tea.Quit
		}
		if m.windowNav.requestBack {
			m.windowNav.requestBack = false
			m.state.Mode = paletteModeList
			return m, nil
		}
		return m, cmd
	case paletteModeSnippets:
		if m.snippet == nil {
			return m, nil
		}
		model, cmd := m.snippet.Update(msg)
		if updated, ok := model.(*snippetPanelModel); ok {
			m.snippet = updated
		}
		if m.snippet.requestClose {
			m.snippet.requestClose = false
			return m.closePalette()
		}
		if m.snippet.requestBack {
			m.snippet.requestBack = false
			m.state.Mode = paletteModeList
			m.state.Message = m.snippet.currentStatus()
			return m, nil
		}
		return m, cmd
	case paletteModeList:
		return m.handleListMouse(msg)
	}
	return m, nil
}

// handleListMouse maps clicks in the main command list to action activation.
// Layout: header(1 or 2 lines) blank filterLine blank body blank footer.
// Body starts with "N commands" + blank; each action then takes 2 lines
// (title + subtitle) with no separator.
func (m *paletteModel) handleListMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	actions := m.filteredActions()
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if len(actions) == 0 {
			return m, nil
		}
		m.state.Selected = (clampInt(m.state.Selected, 0, len(actions)-1) - 1 + len(actions)) % len(actions)
		return m, nil
	case tea.MouseButtonWheelDown:
		if len(actions) == 0 {
			return m, nil
		}
		m.state.Selected = (clampInt(m.state.Selected, 0, len(actions)-1) + 1) % len(actions)
		return m, nil
	case tea.MouseButtonLeft:
	default:
		return m, nil
	}
	headerLines := 1
	if m.runtime.currentSessionName != "" || m.runtime.currentWindowName != "" {
		headerLines = 2
	}
	firstActionY := headerLines + 5 // headerLines + blank + filter + blank + "N commands" + blank
	rel := msg.Y - firstActionY
	if rel < 0 {
		return m, nil
	}
	idx := m.state.ActionOffset + rel/2
	if idx < 0 || idx >= len(actions) {
		return m, nil
	}
	m.state.Selected = idx
	return m.updateList("enter")
}

func (m *paletteModel) updateList(key string) (tea.Model, tea.Cmd) {
	if key == "esc" || key == "ctrl+c" || key == "alt+n" {
		if m.state.SearchActive {
			m.state.Filter = nil
			m.state.FilterCursor = 0
			m.state.SearchActive = false
			m.state.Selected = 0
			m.state.ActionOffset = 0
			return m, nil
		}
		m.result = paletteResult{Kind: paletteResultClose, State: m.state}
		return m, tea.Quit
	}
	if key == "alt+a" {
		cmd, err := m.openActivityPanel()
		if err != nil {
			m.state.Message = err.Error()
			return m, nil
		}
		return m, cmd
	}
	if key == "alt+p" {
		m.openSnippetsPanel()
		return m, nil
	}
	if key == "alt+t" {
		if err := m.openTodosPanel(); err != nil {
			m.state.Message = err.Error()
		}
		return m, nil
	}
	actions := m.filteredActions()
	navigate := func(delta int) {
		if len(actions) == 0 {
			m.state.Selected = 0
			return
		}
		next := clampInt(m.state.Selected, 0, len(actions)-1) + delta
		if next < 0 {
			next = len(actions) - 1
		} else if next >= len(actions) {
			next = 0
		}
		m.state.Selected = next
	}
	if m.state.SearchActive {
		switch key {
		case "enter", "alt+i":
			m.state.SearchActive = false
			return m, nil
		case "ctrl+k", "alt+k", "up":
			navigate(-1)
			return m, nil
		case "ctrl+j", "alt+j", "down":
			navigate(1)
			return m, nil
		case "ctrl+n", "left":
			m.state.FilterCursor = clampInt(m.state.FilterCursor-1, 0, len(m.state.Filter))
			return m, nil
		case "ctrl+i", "tab", "right":
			m.state.FilterCursor = clampInt(m.state.FilterCursor+1, 0, len(m.state.Filter))
			return m, nil
		}
		if applyPaletteInputKey(key, &m.state.Filter, &m.state.FilterCursor, false) {
			m.state.Selected = 0
			m.state.ActionOffset = 0
			m.state.Message = ""
		}
		return m, nil
	}
	switch key {
	case "k", "K", "ctrl+k", "alt+k", "up":
		navigate(-1)
		return m, nil
	case "j", "J", "ctrl+j", "alt+j", "down":
		navigate(1)
		return m, nil
	case "f", "F":
		m.state.SearchActive = true
		return m, nil
	case "h", "H":
		m.result = paletteResult{Kind: paletteResultClose, State: m.state}
		return m, tea.Quit
	case "l", "L", "enter", "alt+i":
		if len(actions) == 0 || m.state.Selected < 0 || m.state.Selected >= len(actions) {
			return m, nil
		}
		return m.selectAction(actions[m.state.Selected])
	}
	return m, nil
}

func (m *paletteModel) selectAction(action paletteAction) (tea.Model, tea.Cmd) {
	switch action.Kind {
	case paletteActionOpenActivityMonitor:
		cmd, err := m.openActivityPanel()
		if err != nil {
			m.state.Message = err.Error()
			return m, nil
		}
		return m, cmd
	case paletteActionOpenSnippets:
		m.openSnippetsPanel()
		return m, nil
	case paletteActionOpenTodos:
		if err := m.openTodosPanel(); err != nil {
			m.state.Message = err.Error()
		}
		return m, nil
	case paletteActionOpenStatusRight:
		m.openStatusRightPanel()
		return m, nil
	case paletteActionOpenSettings:
		m.openSettingsPanel()
		return m, nil
	case paletteActionOpenWindowNav:
		m.openWindowNavPanel()
		return m, nil
	default:
		m.state.Mode = paletteModeList
		m.result = paletteResult{Kind: paletteResultRunAction, Action: action, State: m.state}
		return m, tea.Quit
	}
}

func (m *paletteModel) View() string {
	width := m.width
	height := m.height
	if width <= 0 {
		width = 96
	}
	if height <= 0 {
		height = 28
	}
	if width < 48 || height < 14 {
		return "Window too small for command palette"
	}
	styles := newPaletteStyles()
	if m.state.Mode == paletteModeActivity {
		if m.activity != nil {
			m.activity.width = width
			m.activity.height = height
			return m.activity.View()
		}
		return styles.muted.Render("Activity monitor unavailable")
	}
	if m.state.Mode == paletteModeSnippets {
		if m.snippet != nil {
			m.snippet.width = width
			m.snippet.height = height
			return m.snippet.render(styles, width, height)
		}
		return styles.muted.Render("Snippet panel unavailable")
	}
	if m.state.Mode == paletteModeTodos {
		if m.todo != nil {
			m.todo.width = width
			m.todo.height = height
			return m.todo.View()
		}
		return styles.muted.Render("Todo panel unavailable")
	}
	if m.state.Mode == paletteModeSettings {
		if m.settings != nil {
			m.settings.width = width
			m.settings.height = height
			return m.settings.render(styles, width, height)
		}
		return styles.muted.Render("Settings unavailable")
	}
	if m.state.Mode == paletteModeGeneral {
		if m.general != nil {
			m.general.width = width
			m.general.height = height
			return m.general.render(styles, width, height)
		}
		return styles.muted.Render("General settings unavailable")
	}
	if m.state.Mode == paletteModeWindowResize {
		if m.windowResize != nil {
			m.windowResize.width = width
			m.windowResize.height = height
			return m.windowResize.render(styles, width, height)
		}
		return styles.muted.Render("Window & Resize settings unavailable")
	}
	if m.state.Mode == paletteModeWindowTitle {
		if m.windowTitle != nil {
			m.windowTitle.width = width
			m.windowTitle.height = height
			return m.windowTitle.render(styles, width, height)
		}
		return styles.muted.Render("Window title settings unavailable")
	}
	if m.state.Mode == paletteModeStatusRight {
		if m.status != nil {
			m.status.width = width
			m.status.height = height
			return m.status.render(styles, width, height)
		}
		return styles.muted.Render("Status panel unavailable")
	}
	if m.state.Mode == paletteModeWindowNav {
		if m.windowNav != nil {
			m.windowNav.width = width
			m.windowNav.height = height
			return m.windowNav.render(styles, width, height)
		}
		return styles.muted.Render("Window navigator unavailable")
	}
	return m.renderListView(styles, width, height)
}

func (m *paletteModel) renderListView(styles paletteStyles, width, height int) string {
	actions := m.filteredActions()
	if len(actions) == 0 {
		m.state.Selected = 0
	} else {
		m.state.Selected = clampInt(m.state.Selected, 0, len(actions)-1)
	}
	title := "Command Palette"
	metaParts := []string{}
	if m.runtime.currentSessionName != "" {
		metaParts = append(metaParts, m.runtime.currentSessionName)
	}
	if m.runtime.currentWindowName != "" {
		metaParts = append(metaParts, m.runtime.currentWindowName)
	}
	header := styles.title.Render(title)
	if len(metaParts) > 0 {
		header = lipgloss.JoinVertical(lipgloss.Left, header, styles.meta.Render(strings.Join(metaParts, "  ·  ")))
	}
	var filterLine string
	if m.state.SearchActive {
		filterLine = styles.searchBox.Width(width).Render(
			lipgloss.JoinHorizontal(lipgloss.Center,
				styles.searchPrompt.Render("SEARCH>"),
				" ",
				styles.input.Render(renderInputValue(m.state.Filter, m.state.FilterCursor, styles)),
			),
		)
	} else if len(m.state.Filter) > 0 {
		filterLine = styles.searchBox.Width(width).Render(
			lipgloss.JoinHorizontal(lipgloss.Center,
				styles.searchPrompt.Render(">"),
				" ",
				styles.meta.Render(string(m.state.Filter)),
				styles.meta.Render("  [F: edit]"),
			),
		)
	} else {
		filterLine = styles.searchBox.Width(width).Render(
			styles.meta.Render("F  search"),
		)
	}
	contentHeight := maxInt(8, height-7)
	listWidth := maxInt(34, width*48/100)
	sidebarWidth := maxInt(28, width-listWidth-3)
	list := m.renderActions(styles, actions, listWidth, contentHeight)
	sidebar := m.renderSidebar(styles, sidebarWidth, contentHeight)
	body := lipgloss.JoinHorizontal(lipgloss.Top, list, strings.Repeat(" ", 3), sidebar)
	footer := renderPaletteFooter(styles, width, m.state.Message, m.state.ShowAltHints)
	view := lipgloss.JoinVertical(lipgloss.Left, header, "", filterLine, "", body, "", footer)
	return lipgloss.NewStyle().Width(width).Height(height).Padding(0, 1).Render(view)
}

func (m *paletteModel) renderActions(styles paletteStyles, actions []paletteAction, width, height int) string {
	entriesPerPage := maxInt(1, (height-2)/3)
	selected := clampInt(m.state.Selected, 0, maxInt(0, len(actions)-1))
	offset := stableListOffset(m.state.ActionOffset, selected, entriesPerPage, len(actions))
	m.state.ActionOffset = offset
	blocks := []string{styles.meta.Render(fmt.Sprintf("%d commands", len(actions))), ""}
	if len(actions) == 0 {
		blocks = append(blocks, styles.muted.Width(width).Render("No matching commands"))
	} else {
		for row := 0; row < entriesPerPage; row++ {
			idx := offset + row
			if idx >= len(actions) {
				break
			}
			action := actions[idx]
			sectionLabel := styles.sectionLabel
			subtle := styles.itemSubtitle
			titleStyle := styles.itemTitle
			box := styles.item
			markerText := "  "
			markerStyle := styles.muted
			rowStyle := lipgloss.NewStyle().Width(maxInt(16, width-2))
			fillStyle := lipgloss.NewStyle()
			if idx == selected {
				selectedBG := lipgloss.Color("238")
				sectionLabel = styles.selectedLabel.Background(selectedBG)
				subtle = styles.selectedSubtle.Background(selectedBG)
				titleStyle = styles.itemTitle.Background(selectedBG).Foreground(lipgloss.Color("230"))
				box = styles.selectedItem
				markerText = "› "
				markerStyle = styles.selectedLabel.Background(selectedBG)
				rowStyle = rowStyle.Background(selectedBG).Foreground(lipgloss.Color("230"))
				fillStyle = fillStyle.Background(selectedBG).Foreground(lipgloss.Color("230"))
			}
			innerWidth := maxInt(16, width-2)
			labelText := strings.ToUpper(action.Section)
			labelWidth := lipgloss.Width(labelText)
			markerWidth := lipgloss.Width(markerText)
			titleWidth := maxInt(10, innerWidth-markerWidth-labelWidth-1)
			titleText := truncate(action.Title, titleWidth)
			gapWidth := maxInt(1, innerWidth-markerWidth-lipgloss.Width(titleText)-labelWidth)
			titleRow := rowStyle.Render(
				markerStyle.Render(markerText) +
					titleStyle.Render(titleText) +
					fillStyle.Render(strings.Repeat(" ", gapWidth)) +
					sectionLabel.Render(labelText),
			)
			subtitleRow := rowStyle.Render(fillStyle.Render(strings.Repeat(" ", markerWidth)) + subtle.Render(truncate(action.Subtitle, maxInt(0, innerWidth-markerWidth))))
			block := lipgloss.JoinVertical(lipgloss.Left, titleRow, subtitleRow)
			blocks = append(blocks, box.Width(width).Render(block))
		}
	}
	content := strings.Join(blocks, "\n")
	return lipgloss.NewStyle().Width(width).Height(height).Render(content)
}

func (m *paletteModel) renderSidebar(styles paletteStyles, width, height int) string {
	lines := []string{}
	lines = append(lines, styles.panelTitle.Render("Context"))
	lines = append(lines, renderPaletteStat(styles, "Context", m.runtime.sidebarContext(), width, 9))
	lines = append(lines, "")
	lines = append(lines, styles.panelTitle.Render("Todo Preview"))
	previewLimit := clampInt((height-6)/4, 1, 3)
	sections := m.runtime.sidebarTodoPreviewSections()
	for idx, section := range sections {
		if idx > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, renderPaletteTodoPreviewSection(styles, section, width, previewLimit)...)
	}
	content := strings.Join(lines, "\n")
	return lipgloss.NewStyle().Width(width).Height(height).Render(content)
}

func (r *paletteRuntime) sidebarContext() string {
	contextParts := []string{}
	if r.currentSessionName != "" {
		contextParts = append(contextParts, r.currentSessionName)
	}
	if r.currentWindowName != "" {
		contextParts = append(contextParts, r.currentWindowName)
	}
	if r.currentPath != "" {
		contextParts = append(contextParts, projectDisplayName(r.currentPath))
	}
	if len(contextParts) == 0 {
		return "No tmux context detected"
	}
	return strings.Join(contextParts, "  ·  ")
}

func (r *paletteRuntime) sidebarTodoPreviewSections() []paletteTodoPreviewSection {
	sections := []paletteTodoPreviewSection{}
	store, err := loadTmuxTodoStore()
	windowID := strings.TrimSpace(r.windowID)
	if err != nil {
		sections = append(sections, paletteTodoPreviewSection{Title: "Window", Empty: "Todo store unavailable"})
		sections = append(sections, paletteTodoPreviewSection{Title: "Global", Empty: "Todo store unavailable"})
	} else {
		windowSection := paletteTodoPreviewSection{Title: "Window", Empty: "No window todos"}
		if windowID == "" {
			windowSection.Empty = "No window context"
		} else {
			windowSection.Items = paletteTmuxTodoPreviewItems(todoItemsForScope(store, todoScopeWindow, windowID))
		}
		sections = append(sections, windowSection)
		sections = append(sections, paletteTodoPreviewSection{
			Title: "Global",
			Items: paletteTmuxTodoPreviewItems(todoItemsForScope(store, todoScopeGlobal, "")),
			Empty: "No global todos",
		})
	}
	return sections
}

func (m *paletteModel) filteredActions() []paletteAction {
	query := strings.ToLower(strings.TrimSpace(string(m.state.Filter)))
	if query == "" {
		return m.actions
	}
	parts := strings.Fields(query)
	filtered := make([]paletteAction, 0, len(m.actions))
	for _, action := range m.actions {
		haystack := strings.ToLower(action.Title)
		matched := true
		for _, part := range parts {
			if !strings.Contains(haystack, part) {
				matched = false
				break
			}
		}
		if matched {
			filtered = append(filtered, action)
		}
	}
	return filtered
}

func newPaletteStyles() paletteStyles {
	return paletteStyles{
		title:          lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")),
		meta:           lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		searchBox:      lipgloss.NewStyle().Background(lipgloss.Color("236")).Padding(0, 1),
		searchPrompt:   lipgloss.NewStyle().Foreground(lipgloss.Color("223")).Bold(true),
		input:          lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		inputCursor:    lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(lipgloss.Color("223")).Bold(true),
		item:           lipgloss.NewStyle().Padding(0, 1).MarginBottom(1),
		selectedItem:   lipgloss.NewStyle().Padding(0, 1).MarginBottom(1).Background(lipgloss.Color("238")).Foreground(lipgloss.Color("230")),
		sectionLabel:   lipgloss.NewStyle().Foreground(lipgloss.Color("180")).Bold(true),
		selectedLabel:  lipgloss.NewStyle().Foreground(lipgloss.Color("223")).Bold(true),
		itemTitle:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")),
		itemSubtitle:   lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		selectedSubtle: lipgloss.NewStyle().Foreground(lipgloss.Color("251")),
		panelTitle:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("223")),
		panelText:      lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		muted:          lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		footer:         lipgloss.NewStyle().Foreground(lipgloss.Color("216")),
		keyword:        lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Background(lipgloss.Color("237")).Padding(0, 1),
		modal:          lipgloss.NewStyle().Border(paletteModalBorder).BorderForeground(lipgloss.Color("223")).Padding(1, 2),
		modalTitle:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")),
		modalBody:      lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		modalHint:      lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		statusBad:      lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
		statLabel:      lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		statValue:      lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		todoCheck:      lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		todoCheckDone:  lipgloss.NewStyle().Foreground(lipgloss.Color("150")),
		panelTextDone:  lipgloss.NewStyle().Foreground(lipgloss.Color("246")),
		shortcutKey:    lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Background(lipgloss.Color("223")).Padding(0, 1).Bold(true),
		shortcutText:   lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
	}
}

func renderInputValue(text []rune, cursor int, styles paletteStyles) string {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(text) {
		cursor = len(text)
	}
	left := string(text[:cursor])
	right := string(text[cursor:])
	cursorChar := " "
	if cursor < len(text) {
		cursorChar = string(text[cursor])
		right = string(text[cursor+1:])
	}
	if len(text) == 0 && cursor == 0 {
		cursorChar = " "
	}
	return left + styles.inputCursor.Render(cursorChar) + right
}

// paletteKeyPastePrefix marks a bracketed-paste event in the panels' string
// key routing. tea.KeyMsg.String() wraps pasted runes in literal brackets
// ("[text]"), so panels encode paste via paletteKeyString instead; the NUL
// prefix cannot be typed, and non-input key handlers simply won't match it.
const paletteKeyPastePrefix = "\x00paste:"

// paletteKeyString stringifies a key event for string-based key routing,
// keeping bracketed-paste content intact (IME commits and terminal pastes
// often arrive as paste events).
func paletteKeyString(msg tea.KeyMsg) string {
	if msg.Paste {
		return paletteKeyPastePrefix + string(msg.Runes)
	}
	return msg.String()
}

func applyPaletteInputKey(key string, text *[]rune, cursor *int, allowEnter bool) bool {
	if text == nil || cursor == nil {
		return false
	}
	// A drifted cursor must never panic the slice ops below (prefill code must
	// keep rune counts, but clamp defensively).
	if *cursor < 0 {
		*cursor = 0
	}
	if *cursor > len(*text) {
		*cursor = len(*text)
	}
	if pasted, ok := strings.CutPrefix(key, paletteKeyPastePrefix); ok {
		var printable []rune
		for _, r := range pasted {
			if r == '\n' || r == '\t' {
				r = ' '
			}
			if r >= 0x20 && r != 0x7f {
				printable = append(printable, r)
			}
		}
		if len(printable) > 0 {
			insertRunes(printable, text, cursor)
		}
		return true
	}
	switch key {
	case "left":
		*cursor = clampInt(*cursor-1, 0, len(*text))
		return true
	case "right":
		*cursor = clampInt(*cursor+1, 0, len(*text))
		return true
	case "backspace", "ctrl+h":
		if *cursor > 0 {
			*text = append((*text)[:*cursor-1], (*text)[*cursor:]...)
			*cursor--
		}
		return true
	case "delete":
		if *cursor < len(*text) {
			*text = append((*text)[:*cursor], (*text)[*cursor+1:]...)
		}
		return true
	case "ctrl+a", "home":
		*cursor = 0
		return true
	case "ctrl+e", "end":
		*cursor = len(*text)
		return true
	case "ctrl+u":
		*text = (*text)[*cursor:]
		*cursor = 0
		return true
	case "ctrl+w":
		start := previousWordBoundary(*text, *cursor)
		*text = append((*text)[:start], (*text)[*cursor:]...)
		*cursor = start
		return true
	case "enter":
		return allowEnter
	case "ctrl+v":
		if clip := readClipboard(); clip != "" {
			insertRunes([]rune(clip), text, cursor)
		}
		return true
	}
	r, ok := paletteRuneFromKey(key)
	if ok {
		insertRunes([]rune{r}, text, cursor)
		return true
	}
	// Multi-rune input: IME composition (e.g. Chinese pinyin → characters).
	runes := []rune(key)
	if len(runes) > 1 {
		var printable []rune
		for _, r := range runes {
			if r >= 0x20 && r != 0x7f {
				printable = append(printable, r)
			}
		}
		if len(printable) > 0 {
			insertRunes(printable, text, cursor)
			return true
		}
	}
	return false
}

func paletteRuneFromKey(key string) (rune, bool) {
	if key == "space" {
		return ' ', true
	}
	runes := []rune(key)
	if len(runes) == 1 {
		return runes[0], true
	}
	return 0, false
}

func renderVerticalDivider(height int) string {
	lines := make([]string, maxInt(1, height))
	for i := range lines {
		lines[i] = "│"
	}
	return strings.Join(lines, "\n")
}

func renderPaletteStat(styles paletteStyles, label, value string, width int, labelWidth int) string {
	parts := wrapText(value, maxInt(10, width-labelWidth-3))
	if len(parts) == 0 {
		parts = []string{"-"}
	}
	lines := []string{styles.statLabel.Width(labelWidth).Render(label+":") + " " + styles.statValue.Render(parts[0])}
	for _, part := range parts[1:] {
		lines = append(lines, strings.Repeat(" ", labelWidth+1)+styles.statValue.Render(part))
	}
	return strings.Join(lines, "\n")
}

func renderPaletteModeFooter(styles paletteStyles, width int, message string, showAltHints bool, normalCandidates [][][2]string, altCandidates [][][2]string) string {
	message = strings.TrimSpace(message)
	if message != "" {
		style := styles.footer
		lower := strings.ToLower(message)
		if strings.Contains(lower, "error") || strings.Contains(lower, "required") || strings.Contains(lower, "unknown") {
			style = styles.statusBad
		}
		return style.Width(width).Render(truncate(message, width))
	}
	renderSegments := func(pairs [][2]string) string {
		return renderShortcutPairs(func(v string) string { return styles.shortcutKey.Render(v) }, func(v string) string { return styles.shortcutText.Render(v) }, "   ", pairs)
	}
	candidates := normalCandidates
	if showAltHints {
		candidates = altCandidates
	}
	footer := pickRenderedShortcutFooter(width, renderSegments, candidates...)
	return lipgloss.NewStyle().Width(width).Render(footer)
}

func renderPaletteFooter(styles paletteStyles, width int, message string, showAltHints bool) string {
	return renderPaletteModeFooter(styles, width, message, showAltHints,
		[][][2]string{
			{{"J/K", "move"}, {"F", "search"}, {"Enter", "run"}, {"Esc", "close"}, {footerHintToggleKey, "more"}},
			{{"J/K", "move"}, {"Enter", "run"}, {"Esc", "close"}, {footerHintToggleKey, "more"}},
			{{"Enter", "run"}, {"Esc", "close"}, {footerHintToggleKey, "more"}},
		},
		[][][2]string{
			{{"Alt-U/E", "move"}, {"Alt-I", "run"}, {"Alt-A", "activity"}, {"Alt-P", "snippets"}, {"Alt-T", "todos"}, {"Alt-S", "close"}, {footerHintToggleKey, "hide"}},
			{{"Alt-A", "activity"}, {"Alt-T", "todos"}, {"Alt-S", "close"}, {footerHintToggleKey, "hide"}},
			{{"Alt-A", "activity"}, {"Alt-S", "close"}},
		},
	)
}

func paletteTmuxTodoPreviewItems(items []tmuxTodoItem) []paletteTodoPreviewItem {
	rows := make([]paletteTodoPreviewItem, 0, len(items))
	for _, item := range items {
		title := firstPaletteLine(item.Title)
		if title == "" || item.Done {
			continue
		}
		rows = append(rows, paletteTodoPreviewItem{Title: title, Done: item.Done})
	}
	return rows
}

func renderPaletteTodoPreviewSection(styles paletteStyles, section paletteTodoPreviewSection, width int, previewLimit int) []string {
	lines := []string{styles.statLabel.Render(section.Title)}
	if section.Lead != "" {
		lines = append(lines, renderPalettePreviewValue(styles, section.Lead, width, 2)...)
	}
	if len(section.Items) == 0 {
		if section.Lead == "" {
			lines = append(lines, styles.muted.Render("  "+section.Empty))
		}
		return lines
	}
	limit := clampInt(previewLimit, 1, len(section.Items))
	for _, item := range section.Items[:limit] {
		lines = append(lines, renderPaletteTodoPreviewItem(styles, item, width, 2)...)
	}
	hidden := len(section.Items) - limit
	if hidden > 0 {
		lines = append(lines, styles.muted.Render(fmt.Sprintf("  +%d more", hidden)))
	}
	return lines
}

func renderPaletteTodoPreviewItem(styles paletteStyles, item paletteTodoPreviewItem, width int, indent int) []string {
	title := strings.TrimSpace(item.Title)
	if title == "" {
		return nil
	}
	check := "○"
	checkStyle := styles.todoCheck
	textStyle := styles.panelText
	if item.Done {
		check = "●"
		checkStyle = styles.todoCheckDone
		textStyle = styles.panelTextDone
	}
	indentPrefix := strings.Repeat(" ", maxInt(0, indent))
	textPrefix := indentPrefix + check + " "
	available := maxInt(10, width-lipgloss.Width(textPrefix))
	parts := wrapText(title, available)
	if len(parts) == 0 {
		parts = []string{title}
	}
	lines := []string{indentPrefix + checkStyle.Render(check) + " " + textStyle.Render(truncate(parts[0], available))}
	continuationPrefix := strings.Repeat(" ", lipgloss.Width(textPrefix))
	for _, part := range parts[1:] {
		lines = append(lines, continuationPrefix+textStyle.Render(truncate(part, available)))
	}
	return lines
}

func renderPalettePreviewValue(styles paletteStyles, value string, width int, indent int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	prefix := strings.Repeat(" ", maxInt(0, indent))
	available := maxInt(10, width-len([]rune(prefix)))
	parts := wrapText(value, available)
	if len(parts) == 0 {
		parts = []string{value}
	}
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		lines = append(lines, prefix+styles.panelText.Render(truncate(part, available)))
	}
	return lines
}

func firstPaletteLine(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "\n")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

func paletteMessageForError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func clampInt(value, low, high int) int {
	if high < low {
		return low
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

package main

import (
	"flag"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type paletteMode int

const (
	paletteModeList paletteMode = iota
	paletteModePrompt
	paletteModeConfirmDestroy
	paletteModeSnippets
	paletteModeTodos
	paletteModeActivity
	paletteModeStatusRight
	paletteModeSettings
	paletteModeWindowTitle
	paletteModeWindowNav
	paletteModeGeneral
	paletteModeWindowResize
)

type paletteActionKind int

const (
	paletteActionOpenActivityMonitor paletteActionKind = iota
	paletteActionReloadTmuxConfig
	paletteActionOpenStatusRight
	paletteActionOpenSnippets
	paletteActionOpenTodos
	paletteActionOpenSettings
	paletteActionOpenWindowNav
)

type paletteAction struct {
	Section  string
	Title    string
	Subtitle string
	Keywords []string
	Kind     paletteActionKind
}

type paletteResultKind int

const (
	paletteResultClose paletteResultKind = iota
	paletteResultRunAction
	paletteResultOpenActivityMonitor
	paletteResultOpenSnippets
	paletteResultOpenTodos
)

type paletteResult struct {
	Kind   paletteResultKind
	Action paletteAction
	Input  string
	State  paletteUIState
}

type paletteUIState struct {
	Filter       []rune
	FilterCursor int
	SearchActive bool
	Selected     int
	ActionOffset int
	Mode         paletteMode
	ShowAltHints bool
	Message      string
}

// snippet types, loadSnippets, extractSnippetVars, renderSnippet and the
// {{var}} regex now live in snippet.go (the content-library data layer).

func pasteToTmuxPane(text string) error {
	return runTmux("send-keys", "-l", text)
}

func looksLikeTmuxFormatLiteral(value string) bool {
	value = strings.TrimSpace(value)
	return strings.Contains(value, "#{") && strings.Contains(value, "}")
}

func runPalette(args []string) error {
	return runBubbleTeaPalette(args)
}

func runWindowNavDirect(args []string) error {
	panel := newWindowNavPanelModel()
	panel.directMode = true
	// parse --window / --session-name / --path flags for current session context
	fs := flag.NewFlagSet("agent windows", flag.ContinueOnError)
	var windowID, sessionName, currentPath string
	var initW, initH int
	fs.StringVar(&windowID, "window", "", "current window id")
	fs.StringVar(&sessionName, "session-name", "", "current session name")
	fs.StringVar(&currentPath, "path", "", "current pane path")
	// The launching popup script (open_window_nav.sh) knows the popup dimensions
	// authoritatively; pass them so the Name column has the right width even when
	// the display-popup's WindowSizeMsg is late/zero.
	fs.IntVar(&initW, "width", 0, "initial popup width in columns")
	fs.IntVar(&initH, "height", 0, "initial popup height in rows")
	fs.SetOutput(nil)
	_ = fs.Parse(args)
	_ = windowID
	_ = sessionName
	_ = currentPath
	if initW > 0 {
		panel.width = initW
		panel.sizeLocked = true // popup is fixed-size; ignore misreported WindowSizeMsg
	}
	if initH > 0 {
		panel.height = initH
	}

	p := tea.NewProgram(panel, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

// runWindowTimerDirect runs the per-window timer panel as a standalone popup
// (prefix t). Window name is resolved here (not passed as an arg) to avoid
// run-shell word-splitting on names with spaces.
func runWindowTimerDirect(args []string) error {
	fs := flag.NewFlagSet("agent window-timer", flag.ContinueOnError)
	var windowID, sessionName, currentPath string
	fs.StringVar(&windowID, "window", "", "current window id")
	fs.StringVar(&sessionName, "session-name", "", "current session name")
	fs.StringVar(&currentPath, "path", "", "current pane path")
	fs.SetOutput(nil)
	_ = fs.Parse(args)
	_ = sessionName
	_ = currentPath

	windowName := ""
	if windowID != "" {
		if out, err := runTmuxOutput("display-message", "-p", "-t", windowID, "#{window_name}"); err == nil {
			windowName = stripStatusPrefix(strings.TrimSpace(out))
		}
	}

	panel := newWindowTimerPanel(windowID, windowName)
	panel.directMode = true
	p := tea.NewProgram(panel, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

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
)

type palettePromptField int

const (
	palettePromptFieldName palettePromptField = iota
	palettePromptFieldDevice
	palettePromptFieldWorktree
)

type palettePromptKind int

const (
	palettePromptStartAgent palettePromptKind = iota
)

type paletteActionKind int

const (
	paletteActionPromptStartAgent paletteActionKind = iota
	paletteActionOpenActivityMonitor
	paletteActionConfirmDestroy
	paletteActionReloadTmuxConfig
	paletteActionOpenStatusRight
	paletteActionOpenSnippets
	paletteActionOpenTodos
	paletteActionOpenDevices
	paletteActionOpenTracker
	paletteActionOpenSettings
	paletteActionOpenWindowNav
)

type paletteAction struct {
	Section  string
	Title    string
	Subtitle string
	Keywords []string
	Kind     paletteActionKind
	RepoRoot string
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
	Kind         paletteResultKind
	Action       paletteAction
	Input        string
	Device       string
	KeepWorktree bool
	State        paletteUIState
}

type paletteUIState struct {
	Filter              []rune
	FilterCursor        int
	SearchActive        bool
	Selected            int
	ActionOffset        int
	Mode                paletteMode
	PromptText          []rune
	PromptCursor        int
	PromptKind          palettePromptKind
	PromptField         palettePromptField
	PromptRepoRoot      string
	PromptDevices       []string
	PromptDeviceIndex   int
	PromptKeepWorktree  bool
	ShowAltHints        bool
	Message             string
	ConfirmRequiresText bool
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
	fs.StringVar(&windowID, "window", "", "current window id")
	fs.StringVar(&sessionName, "session-name", "", "current session name")
	fs.StringVar(&currentPath, "path", "", "current pane path")
	fs.SetOutput(nil)
	_ = fs.Parse(args)
	_ = windowID
	_ = sessionName
	_ = currentPath

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

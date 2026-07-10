package main

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── types ─────────────────────────────────────────────────────────────────────

type snippetPanelMode int

const (
	snippetPanelModeList snippetPanelMode = iota
	snippetPanelModeAdd
	snippetPanelModeEdit
	snippetPanelModeDeleteConfirm
	snippetPanelModeVarInput
)

const (
	snippetFavGroupKey  = "\x00favorites" // collapsed-map key for the virtual ★ group
	snippetUngroupedKey = "\x00ungrouped" // collapsed-map key for the root (ungrouped) group
)

const (
	snippetFormFieldName = iota
	snippetFormFieldGroup
	snippetFormFieldDesc
	snippetFormFieldContent
	snippetFormFieldCount
)

// snippetRow is one rendered line: a collapsible group header or a snippet entry.
type snippetRow struct {
	isHeader  bool
	groupKey  string // collapsed-map key (header rows)
	label     string // display label (header rows)
	count     int    // entry count (header rows)
	collapsed bool   // header rows
	fav       bool   // header: virtual favorites group
	snip      snippet
}

type snippetPanelModel struct {
	mode      snippetPanelMode
	collapsed map[string]bool
	selected  int

	searchActive bool
	searchQuery  []rune
	searchCursor int

	// add/edit form
	formFields   [snippetFormFieldCount][]rune
	formCursors  [snippetFormFieldCount]int
	formActive   int
	formEditPath string // empty = add, non-empty = editing this path

	// delete confirm
	deletePath string
	deleteName string

	// variable input (paste/return with {{vars}})
	varSnippet snippet
	varNames   []string
	varIdx     int
	varValues  map[string]string
	varInput   []rune
	varCursor  int

	// consumption mode
	returnMode    bool   // true = embedded picker: Enter returns content, no paste
	chosenContent string // set in returnMode when a snippet is chosen
	initialGroup  string // when non-empty, other groups start collapsed

	width  int
	height int

	requestBack  bool
	requestClose bool // paste completed in a palette host → close the whole palette

	statusMsg   string
	statusUntil time.Time

	// captured by renderList each frame; MouseMsg uses these to map Y → row.
	lastListStart      int
	lastListHeaderRows int
}

func newSnippetPanelModel(returnMode bool, initialGroup string) *snippetPanelModel {
	m := &snippetPanelModel{
		collapsed:    map[string]bool{},
		returnMode:   returnMode,
		initialGroup: initialGroup,
		varValues:    map[string]string{},
	}
	if initialGroup != "" {
		// collapse all groups except the initial one
		for _, g := range snippetGroups() {
			if g != initialGroup {
				m.collapsed[groupKeyFor(g)] = true
			}
		}
		m.collapsed[snippetFavGroupKey] = true
		m.collapsed[snippetUngroupedKey] = true
	}
	return m
}

func groupKeyFor(group string) string {
	if group == "" {
		return snippetUngroupedKey
	}
	return group
}

func (m *snippetPanelModel) setStatus(msg string) {
	m.statusMsg = msg
	m.statusUntil = time.Now().Add(3 * time.Second)
}

func (m *snippetPanelModel) currentStatus() string {
	if m.statusMsg == "" {
		return ""
	}
	if time.Now().After(m.statusUntil) {
		m.statusMsg = ""
		return ""
	}
	return m.statusMsg
}

// ── row model ───────────────────────────────────────────────────────────────

func (m *snippetPanelModel) matches(s snippet) bool {
	q := strings.TrimSpace(strings.ToLower(string(m.searchQuery)))
	if q == "" {
		return true
	}
	hay := strings.ToLower(s.Name + " " + s.Description + " " + s.Content + " " + s.Group)
	for _, term := range strings.Fields(q) {
		if !strings.Contains(hay, term) {
			return false
		}
	}
	return true
}

// visibleRows builds the flat row list (headers + entries) honoring collapse and search.
func (m *snippetPanelModel) visibleRows() []snippetRow {
	all := loadSnippets()
	var filtered []snippet
	for _, s := range all {
		if m.matches(s) {
			filtered = append(filtered, s)
		}
	}
	searching := strings.TrimSpace(string(m.searchQuery)) != ""

	var favs []snippet
	byGroup := map[string][]snippet{}
	for _, s := range filtered {
		if s.Favorite {
			favs = append(favs, s)
		}
		byGroup[s.Group] = append(byGroup[s.Group], s)
	}

	var rows []snippetRow
	collapsed := func(key string) bool {
		if searching {
			return false // search expands everything so matches are visible
		}
		return m.collapsed[key]
	}

	if len(favs) > 0 {
		c := collapsed(snippetFavGroupKey)
		rows = append(rows, snippetRow{isHeader: true, groupKey: snippetFavGroupKey, label: "★ favorites", count: len(favs), collapsed: c, fav: true})
		if !c {
			for _, s := range favs {
				rows = append(rows, snippetRow{snip: s})
			}
		}
	}

	named := make([]string, 0, len(byGroup))
	for g := range byGroup {
		if g != "" {
			named = append(named, g)
		}
	}
	sort.Strings(named)
	for _, g := range named {
		key := groupKeyFor(g)
		c := collapsed(key)
		rows = append(rows, snippetRow{isHeader: true, groupKey: key, label: g, count: len(byGroup[g]), collapsed: c})
		if !c {
			for _, s := range byGroup[g] {
				rows = append(rows, snippetRow{snip: s})
			}
		}
	}
	if len(byGroup[""]) > 0 {
		key := snippetUngroupedKey
		c := collapsed(key)
		rows = append(rows, snippetRow{isHeader: true, groupKey: key, label: "ungrouped", count: len(byGroup[""]), collapsed: c})
		if !c {
			for _, s := range byGroup[""] {
				rows = append(rows, snippetRow{snip: s})
			}
		}
	}
	return rows
}

func (m *snippetPanelModel) clampSelection(rows []snippetRow) {
	if len(rows) == 0 {
		m.selected = 0
		return
	}
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= len(rows) {
		m.selected = len(rows) - 1
	}
}

// reloadKeepingPath re-derives rows and restores selection onto the row with the
// given snippet path (so favorite/CRUD-induced row shifts don't misposition).
func (m *snippetPanelModel) reloadKeepingPath(path string) {
	rows := m.visibleRows()
	if path != "" {
		for i, r := range rows {
			if !r.isHeader && r.snip.Path == path {
				m.selected = i
				return
			}
		}
	}
	m.clampSelection(rows)
}

func (m *snippetPanelModel) selectedRow() (snippetRow, bool) {
	rows := m.visibleRows()
	if m.selected < 0 || m.selected >= len(rows) {
		return snippetRow{}, false
	}
	return rows[m.selected], true
}

// ── BubbleTea ─────────────────────────────────────────────────────────────────

func (m *snippetPanelModel) Init() tea.Cmd { return nil }

func (m *snippetPanelModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		return m.handleKey(paletteKeyString(msg))
	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

func (m *snippetPanelModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mode != snippetPanelModeList {
		return m, nil
	}
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.moveSelection(-1)
		return m, nil
	case tea.MouseButtonWheelDown:
		m.moveSelection(1)
		return m, nil
	case tea.MouseButtonLeft:
		if idx, ok := m.rowIndexAtY(msg.Y); ok {
			m.selected = idx
			return m.activateSelected()
		}
	}
	return m, nil
}

// rowIndexAtY maps a mouse Y coordinate to an index in visibleRows().
// Layout: [title · 1] [search? · 1] "" [body rows …] "" [footer · 1].
// headerLines already counts title + optional search + trailing blank.
func (m *snippetPanelModel) rowIndexAtY(y int) (int, bool) {
	rel := y - m.lastListHeaderRows
	if rel < 0 {
		return 0, false
	}
	rows := m.visibleRows()
	idx := m.lastListStart + rel
	if idx < 0 || idx >= len(rows) {
		return 0, false
	}
	if rows[idx].isHeader {
		return 0, false
	}
	return idx, true
}

func (m *snippetPanelModel) handleKey(key string) (tea.Model, tea.Cmd) {
	switch m.mode {
	case snippetPanelModeList:
		return m.handleListKey(key)
	case snippetPanelModeAdd, snippetPanelModeEdit:
		return m.handleFormKey(key)
	case snippetPanelModeDeleteConfirm:
		return m.handleDeleteKey(key)
	case snippetPanelModeVarInput:
		return m.handleVarKey(key)
	}
	return m, nil
}

func (m *snippetPanelModel) handleListKey(key string) (tea.Model, tea.Cmd) {
	if m.searchActive {
		switch key {
		case "enter":
			m.searchActive = false // keep query, exit search-typing
		case "esc":
			m.searchActive = false
			m.searchQuery = nil
			m.searchCursor = 0
		default:
			applyPaletteInputKey(key, &m.searchQuery, &m.searchCursor, false)
		}
		m.clampSelection(m.visibleRows())
		return m, nil
	}

	switch key {
	case "esc", "h", "H", "q":
		m.requestBack = true
	case "j", "J", "down":
		m.moveSelection(1)
	case "k", "K", "up":
		m.moveSelection(-1)
	case "l", "L", "enter", " ":
		return m.activateSelected()
	case "f", "F", "/":
		m.searchActive = true
	case "a", "A":
		m.openAddForm()
	case "e", "E":
		m.openEditForm()
	case "x", "X", "d", "D":
		m.openDeleteConfirm()
	case "s", "S":
		m.toggleFavoriteSelected()
	}
	return m, nil
}

func (m *snippetPanelModel) moveSelection(delta int) {
	rows := m.visibleRows()
	if len(rows) == 0 {
		return
	}
	m.selected = clampInt(m.selected+delta, 0, len(rows)-1)
}

func (m *snippetPanelModel) activateSelected() (tea.Model, tea.Cmd) {
	row, ok := m.selectedRow()
	if !ok {
		return m, nil
	}
	if row.isHeader {
		m.collapsed[row.groupKey] = !m.collapsed[row.groupKey]
		return m, nil
	}
	return m.consumeSnippet(row.snip)
}

// consumeSnippet either starts variable input, or finalizes (paste/return).
func (m *snippetPanelModel) consumeSnippet(s snippet) (tea.Model, tea.Cmd) {
	if len(s.Vars) > 0 {
		m.varSnippet = s
		m.varNames = s.Vars
		m.varIdx = 0
		m.varValues = map[string]string{}
		m.varInput = nil
		m.varCursor = 0
		m.mode = snippetPanelModeVarInput
		return m, nil
	}
	return m.finalize(s.Content)
}

// finalize delivers rendered content: return mode hands it back; otherwise paste.
func (m *snippetPanelModel) finalize(content string) (tea.Model, tea.Cmd) {
	if m.returnMode {
		m.chosenContent = content
		m.requestBack = true
		return m, nil
	}
	if err := pasteToTmuxPane(content); err != nil {
		m.setStatus("paste failed: " + err.Error())
		return m, nil
	}
	// paste succeeded → close the palette host (matches the pre-refactor behavior).
	m.requestClose = true
	m.requestBack = true
	return m, nil
}

func (m *snippetPanelModel) toggleFavoriteSelected() {
	row, ok := m.selectedRow()
	if !ok || row.isHeader {
		return
	}
	if _, err := toggleFavorite(row.snip.Path); err != nil {
		m.setStatus("favorite failed: " + err.Error())
		return
	}
	m.reloadKeepingPath(row.snip.Path)
}

func (m *snippetPanelModel) openAddForm() {
	m.mode = snippetPanelModeAdd
	m.formEditPath = ""
	m.formActive = 0
	for i := range m.formFields {
		m.formFields[i] = nil
		m.formCursors[i] = 0
	}
	if m.initialGroup != "" {
		group := []rune(m.initialGroup)
		m.formFields[snippetFormFieldGroup] = group
		m.formCursors[snippetFormFieldGroup] = len(group)
	}
}

func (m *snippetPanelModel) openEditForm() {
	row, ok := m.selectedRow()
	if !ok || row.isHeader {
		return
	}
	s := row.snip
	m.mode = snippetPanelModeEdit
	m.formEditPath = s.Path
	m.formActive = 0
	set := func(idx int, v string) {
		runes := []rune(v)
		m.formFields[idx] = runes
		m.formCursors[idx] = len(runes) // rune count — len(v) would be bytes and overshoot on CJK
	}
	set(snippetFormFieldName, s.Name)
	set(snippetFormFieldGroup, s.Group)
	set(snippetFormFieldDesc, s.Description)
	set(snippetFormFieldContent, s.Content)
}

func (m *snippetPanelModel) handleFormKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = snippetPanelModeList
		return m, nil
	case "ctrl+s":
		m.submitForm()
		return m, nil
	case "tab", "ctrl+j", "down":
		m.formActive = (m.formActive + 1) % snippetFormFieldCount
		return m, nil
	case "shift+tab", "ctrl+k", "up":
		m.formActive = (m.formActive - 1 + snippetFormFieldCount) % snippetFormFieldCount
		return m, nil
	case "enter":
		if m.formActive == snippetFormFieldContent {
			// content is multiline: Enter inserts a newline
			insertRunes([]rune{'\n'}, &m.formFields[m.formActive], &m.formCursors[m.formActive])
		} else if m.formActive < snippetFormFieldCount-1 {
			m.formActive++
		} else {
			m.submitForm()
		}
		return m, nil
	default:
		field := &m.formFields[m.formActive]
		cursor := &m.formCursors[m.formActive]
		applyPaletteInputKey(key, field, cursor, false)
	}
	return m, nil
}

func (m *snippetPanelModel) submitForm() {
	name := strings.TrimSpace(string(m.formFields[snippetFormFieldName]))
	group := strings.TrimSpace(string(m.formFields[snippetFormFieldGroup]))
	desc := strings.TrimSpace(string(m.formFields[snippetFormFieldDesc]))
	content := string(m.formFields[snippetFormFieldContent])
	if name == "" {
		m.setStatus("name cannot be empty")
		m.formActive = snippetFormFieldName
		return
	}
	isAdd := m.mode == snippetPanelModeAdd
	var err error
	if isAdd {
		err = addSnippet(group, name, desc, content)
	} else {
		err = updateSnippet(m.formEditPath, group, name, desc, content)
	}
	if err != nil {
		m.setStatus("error: " + err.Error())
		return
	}
	savedPath := filepath.Join(snippetsRootDir(), group, name)
	m.mode = snippetPanelModeList
	if isAdd {
		m.setStatus("snippet added")
	} else {
		m.setStatus("snippet saved")
	}
	m.reloadKeepingPath(savedPath)
}

func (m *snippetPanelModel) openDeleteConfirm() {
	row, ok := m.selectedRow()
	if !ok || row.isHeader {
		return
	}
	m.deletePath = row.snip.Path
	m.deleteName = row.snip.Name
	m.mode = snippetPanelModeDeleteConfirm
}

func (m *snippetPanelModel) handleDeleteKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y", "Y":
		if err := deleteSnippet(m.deletePath); err != nil {
			m.setStatus("delete failed: " + err.Error())
		} else {
			m.setStatus("snippet deleted")
		}
		m.mode = snippetPanelModeList
		m.reloadKeepingPath("")
	case "n", "N", "esc":
		m.mode = snippetPanelModeList
	}
	return m, nil
}

func (m *snippetPanelModel) handleVarKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = snippetPanelModeList
		return m, nil
	case "enter":
		m.varValues[m.varNames[m.varIdx]] = string(m.varInput)
		m.varIdx++
		m.varInput = nil
		m.varCursor = 0
		if m.varIdx >= len(m.varNames) {
			content := renderSnippet(m.varSnippet.Content, m.varValues)
			m.mode = snippetPanelModeList
			return m.finalize(content)
		}
		return m, nil
	default:
		applyPaletteInputKey(key, &m.varInput, &m.varCursor, false)
	}
	return m, nil
}

// ── view ──────────────────────────────────────────────────────────────────────

func (m *snippetPanelModel) View() string {
	return m.render(newPaletteStyles(), m.width, m.height)
}

func (m *snippetPanelModel) render(styles paletteStyles, width, height int) string {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 26
	}
	base := m.renderList(styles, width, height)
	switch m.mode {
	case snippetPanelModeAdd, snippetPanelModeEdit:
		return m.overlayForm(base, styles, width, height)
	case snippetPanelModeDeleteConfirm:
		return m.overlayDelete(styles, width, height)
	case snippetPanelModeVarInput:
		return m.overlayVarInput(styles, width, height)
	default:
		return base
	}
}

func (m *snippetPanelModel) renderList(styles paletteStyles, width, height int) string {
	title := "Snippets"
	if m.returnMode {
		title = "Pick snippet"
	}
	titleRow := styles.title.Render(title)
	if st := m.currentStatus(); st != "" {
		titleRow += "  " + styles.statusBad.Render(st)
	}

	var searchLine string
	if m.searchActive || strings.TrimSpace(string(m.searchQuery)) != "" {
		searchLine = styles.muted.Render("search: ") + renderInputValue(m.searchQuery, m.searchCursor, styles)
	}

	footerKeys := "j/k nav   l/Enter open   a add   e edit   x del   s ★   f search   h/Esc back"
	if m.returnMode {
		footerKeys = "j/k nav   l/Enter pick   a add   e edit   x del   s ★   f search   h/Esc back"
	}
	footer := styles.muted.Render(footerKeys)

	rows := m.visibleRows()
	m.clampSelection(rows)

	headerLines := 2
	if searchLine != "" {
		headerLines = 3
	}
	bodyHeight := maxInt(3, height-headerLines-2)

	// scroll window around selection
	start := 0
	if m.selected >= bodyHeight {
		start = m.selected - bodyHeight + 1
	}
	end := minInt(len(rows), start+bodyHeight)
	m.lastListStart = start
	m.lastListHeaderRows = headerLines

	var lines []string
	if len(rows) == 0 {
		lines = append(lines, styles.muted.Render("No snippets. Press 'a' to add one."))
	}
	for i := start; i < end; i++ {
		lines = append(lines, m.renderRow(styles, rows[i], i == m.selected, width-2))
	}
	body := strings.Join(lines, "\n")

	parts := []string{titleRow}
	if searchLine != "" {
		parts = append(parts, searchLine)
	}
	parts = append(parts, "", body, "", footer)
	view := lipgloss.JoinVertical(lipgloss.Left, parts...)
	return lipgloss.NewStyle().Width(width).Height(height).Padding(0, 1).Render(view)
}

func (m *snippetPanelModel) renderRow(styles paletteStyles, row snippetRow, selected bool, width int) string {
	var text string
	if row.isHeader {
		caret := "▼"
		if row.collapsed {
			caret = "▶"
		}
		text = caret + " " + row.label + "  (" + strconv.Itoa(row.count) + ")"
		styled := styles.sectionLabel.Render(text)
		if selected {
			return lipgloss.NewStyle().Width(width).Background(lipgloss.Color("238")).Render("› " + styled)
		}
		return lipgloss.NewStyle().Width(width).Render("  " + styled)
	}
	star := "  "
	if row.snip.Favorite {
		star = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("★ ")
	}
	name := row.snip.Name
	desc := row.snip.Description
	nameW := 22
	namePart := padRight(truncateWidth(name, nameW), nameW)
	descPart := truncateWidth(desc, maxInt(4, width-nameW-8))
	if selected {
		bg := lipgloss.NewStyle().Background(lipgloss.Color("238"))
		out := "  › " + star + bg.Foreground(lipgloss.Color("230")).Bold(true).Render(namePart) + bg.Foreground(lipgloss.Color("251")).Render(descPart)
		return lipgloss.NewStyle().Width(width).Background(lipgloss.Color("238")).Render(out)
	}
	out := "    " + star + styles.panelText.Render(namePart) + styles.muted.Render(descPart)
	return lipgloss.NewStyle().Width(width).Render(out)
}

func (m *snippetPanelModel) overlayForm(base string, styles paletteStyles, width, height int) string {
	title := "Add Snippet"
	if m.mode == snippetPanelModeEdit {
		title = "Edit Snippet"
	}
	labels := []string{"Name", "Group", "Description", "Content"}
	hints := []string{"file name (no / . _)", "subdir = group (empty = ungrouped)", "shown in list", "Enter = newline; {{var}} for variables"}

	modalWidth := minInt(72, width-4)
	innerWidth := modalWidth - 4

	var rows []string
	titleLine := styles.panelTitle.Render(title)
	if st := m.currentStatus(); st != "" {
		titleLine += "  " + styles.statusBad.Render(st)
	}
	rows = append(rows, titleLine, "")
	for i := 0; i < snippetFormFieldCount; i++ {
		active := m.formActive == i
		marker := "  "
		if active {
			marker = "› "
		}
		labelStyle := styles.muted
		if active {
			labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
		}
		valueStr := styles.input.Render(renderInputValue(m.formFields[i], m.formCursors[i], styles))
		row := marker + labelStyle.Render(padRight(labels[i]+":", 13)) + " " + valueStr
		if active && hints[i] != "" {
			row += "  " + styles.muted.Render("("+hints[i]+")")
		}
		rows = append(rows, row)
	}
	rows = append(rows, "", styles.muted.Render("Tab: next   Ctrl+S: save   Esc: cancel"))

	content := lipgloss.JoinVertical(lipgloss.Left, rows...)
	modal := lipgloss.NewStyle().Border(paletteModalBorder).BorderForeground(lipgloss.Color("63")).Width(innerWidth).Padding(0, 1).Render(content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

func (m *snippetPanelModel) overlayDelete(styles paletteStyles, width, height int) string {
	var rows []string
	rows = append(rows, styles.panelTitle.Render("Delete Snippet"), "",
		styles.panelText.Render("Name: "+truncateWidth(m.deleteName, 40)), "",
		styles.statusBad.Render("Delete this snippet?  y / n"))
	content := lipgloss.JoinVertical(lipgloss.Left, rows...)
	modal := lipgloss.NewStyle().Border(paletteModalBorder).BorderForeground(lipgloss.Color("196")).Width(50).Padding(0, 1).Render(content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

func (m *snippetPanelModel) overlayVarInput(styles paletteStyles, width, height int) string {
	name := m.varNames[m.varIdx]
	var rows []string
	rows = append(rows, styles.panelTitle.Render("Fill variable "+strconv.Itoa(m.varIdx+1)+"/"+strconv.Itoa(len(m.varNames))), "",
		styles.muted.Render("{{"+name+"}}"),
		styles.input.Render(renderInputValue(m.varInput, m.varCursor, styles)), "",
		styles.muted.Render("Enter: next   Esc: cancel"))
	content := lipgloss.JoinVertical(lipgloss.Left, rows...)
	modal := lipgloss.NewStyle().Border(paletteModalBorder).BorderForeground(lipgloss.Color("63")).Width(minInt(56, width-4)).Padding(0, 1).Render(content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

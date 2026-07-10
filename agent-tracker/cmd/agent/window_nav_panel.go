package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/david/agent-tracker/internal/ipc"
)

// ── types ────────────────────────────────────────────────────────────────────

type windowNavTickMsg struct{}

type windowNavRow struct {
	isHeader    bool
	sessionID   string
	sessionName string // header display text or session name for window rows
	windowCount int    // header only

	windowID      string
	windowName    string
	windowIndex   int
	activity      int64
	bell          bool // attention flag, shown in its own column (independent of status)
	path          string
	status        string // activity: "idle", "busy", "asking", "limited", ""
	agentDir      string // @agent_dir: abbreviated absolute path
	agentProvider string // @agent_provider: official / minimax / … (empty for codex)
	agentModel    string // @agent_model: sonnet / opus / …
	agentTitle    string // @agent_title: Claude session title
	agentClient   string // @agent_client: claude / codex
	isAgent       bool   // @agent_client is set
	lastBusyAt    int64  // @agent_last_busy_at: unix ts of last busy tick; 0 if never seen
}

type windowNavPanelModel struct {
	// allWindows holds windows in stable display order.
	// Sorted once on first load; subsequent refreshes update data in-place.
	// Only re-sorted when user explicitly changes sort settings (sortDirty=true).
	allWindows []windowNavRow
	windowByID map[string]windowNavRow // latest data keyed by windowID
	sortDirty  bool                    // user changed sort → re-sort on next refresh

	// display rows (built from allWindows + state)
	rows         []windowNavRow
	selected     int
	scrollOffset int

	// stripDatePrefix mirrors the strip_date_prefix setting: drop a leading
	// YYYY-MM-DD- from the Name column (matches the tmux tab / notify name).
	stripDatePrefix bool

	// grouping / ordering
	groupBy  string // "session", "none", "status", "path", "attention"
	orderBy  string // "activity", "index"
	orderDir string // "asc", "desc"

	// collapse state (session group)
	collapsed map[string]bool // sessionID → collapsed

	// search
	searchActive bool
	searchQuery  []rune
	searchCursor int

	// digit jump: buffer accumulates typed index digits; jumpSeq invalidates stale timers
	jumpBuffer string
	jumpSeq    int

	// context
	currentSessionID string
	currentWindowID  string

	// display
	width  int
	height int

	// panel signals
	requestBack  bool
	requestClose bool
	directMode   bool // true when run as standalone (agent windows), false when embedded in palette

	// inline status message
	statusMsg   string
	statusUntil time.Time

	// timer panel sub-mode
	timerPanelActive bool
	timerPanel       *windowTimerPanelModel

	// prompt detail sub-mode
	promptViewActive bool
	promptViewRow    windowNavRow
	promptText       string
	promptScroll     int

	// timer column data: windowID -> display string e.g. "[3]12:10"
	timerByWindow    map[string]string
	hasAnyTimers     bool // true when at least one window has timers (drives column visibility)
	allActivityToday bool // true when every agent window's lastBusyAt is today (narrows time column)

	// lastBusyAtByWindow tracks unix timestamp of last time each agent window was seen as "busy".
	// Updated every refresh when status=="busy"; frozen when idle/asking; never resets during panel lifetime.
	lastBusyAtByWindow map[string]int64

	// attentionSinceByWindow tracks when each window first entered the "needs
	// attention" state (🔔 / asking), used to order the pinned "需要处理" group
	// in the default session view (earliest first). Seeded from the tracker
	// cache timestamp when available, else from first-seen time; cleared when a
	// window stops needing attention.
	attentionSinceByWindow map[string]int64

	// initialCursorSet positions the cursor on the first actionable row (the
	// first 🔔 window) on the very first load only.
	initialCursorSet bool
}

// ── constructor ───────────────────────────────────────────────────────────────

func newWindowNavPanelModel() *windowNavPanelModel {
	cfg := loadAppConfig()
	groupBy, orderBy, orderDir := windowNavSettings(cfg)
	m := &windowNavPanelModel{
		stripDatePrefix:        stripDatePrefixSetting(cfg),
		groupBy:                groupBy,
		orderBy:                orderBy,
		orderDir:               orderDir,
		collapsed:              make(map[string]bool),
		windowByID:             make(map[string]windowNavRow),
		timerByWindow:          make(map[string]string),
		lastBusyAtByWindow:     make(map[string]int64),
		attentionSinceByWindow: make(map[string]int64),
	}
	m.refresh()
	return m
}

// persistSettings saves the current grouping/sorting choices so they survive reopen.
func (m *windowNavPanelModel) persistSettings() {
	saveWindowNavSettings(m.groupBy, m.orderBy, m.orderDir)
}

// ── data ─────────────────────────────────────────────────────────────────────

// parseWindowNavLine parses one `|`-delimited list-windows record into a
// windowNavRow. It computes everything derivable from the tmux fields alone
// (status prefix, native bell, agent metadata); the tracker-cache bell/asking
// overlay is applied separately by the caller. Returns ok=false for empty or
// malformed (fewer than 9 fields) lines.
func parseWindowNavLine(line string) (windowNavRow, bool) {
	if line == "" {
		return windowNavRow{}, false
	}
	parts := strings.SplitN(line, "|", 16)
	if len(parts) < 9 {
		return windowNavRow{}, false
	}
	field := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	activity, _ := strconv.ParseInt(parts[6], 10, 64)
	lastBusyAt, _ := strconv.ParseInt(field(14), 10, 64)
	// Native tmux bell, or a remote 🔔 the daemon mirrored onto this ssh window.
	nativeBell := parts[7] == "1" || field(15) == "1"
	raw := parts[4]
	// status: pure activity, derived from the window-name prefix.
	status := ""
	if strings.HasPrefix(raw, "[B] ") {
		status = "busy"
	} else if strings.HasPrefix(raw, "[?] ") {
		status = "asking"
	} else if strings.HasPrefix(raw, "[L] ") {
		status = "limited"
	} else if strings.HasPrefix(raw, "[I] ") {
		status = "idle"
	}
	idx, _ := strconv.Atoi(parts[2])
	return windowNavRow{
		sessionID:     parts[0],
		sessionName:   parts[1],
		windowIndex:   idx,
		windowID:      parts[3],
		windowName:    stripStatusPrefix(raw),
		activity:      activity,
		bell:          nativeBell,
		path:          parts[8],
		status:        status,
		agentDir:      field(9),
		agentProvider: field(10),
		agentModel:    field(11),
		agentTitle:    field(13),
		agentClient:   field(12),
		isAgent:       field(12) != "",
		lastBusyAt:    lastBusyAt,
	}, true
}

func (m *windowNavPanelModel) refresh() {
	if out, err := runTmuxOutput("display-message", "-p", "#{session_id} #{window_id}"); err == nil {
		fields := strings.Fields(out)
		if len(fields) >= 1 {
			m.currentSessionID = fields[0]
		}
		if len(fields) >= 2 {
			m.currentWindowID = fields[1]
		}
	}
	out, err := runTmuxOutput("list-windows", "-a", "-F",
		"#{session_id}|#{session_name}|#{window_index}|#{window_id}|#{window_name}|#{window_flags}|#{window_activity}|#{window_bell_flag}|#{pane_current_path}|#{@agent_dir}|#{@agent_provider}|#{@agent_model}|#{@agent_client}|#{@agent_title}|#{@agent_last_busy_at}|#{@agent_remote_bell}")
	if err != nil {
		return
	}

	// Load tracker cache to get bell (completed+unacknowledged) and asking state per window.
	// since carries the attention-appearance time hint (completed_at for a 🔔,
	// else started_at for asking) so the pinned "需要处理" group can order by it.
	type windowCacheState struct {
		bell, asking bool
		since        int64
	}
	cacheByWindow := map[string]windowCacheState{}
	if data, err2 := os.ReadFile(filepath.Join(homeDir(), ".hat-config", "state", "agent-tracker", "tmux-tracker-cache.json")); err2 == nil {
		var env ipc.Envelope
		if json.Unmarshal(data, &env) == nil {
			for _, t := range env.Tasks {
				c := cacheByWindow[t.WindowID]
				if t.Status == "completed" && !t.Acknowledged {
					c.bell = true
					if ts := parseTrackerTimeUnix(t.CompletedAt); ts > 0 {
						c.since = ts
					}
				}
				// Only an actively in-progress task drives the asking "?"; a stale
				// asking flag on a completed task (e.g. the previous agent exited at a
				// permission prompt) must not override the live window-name status.
				if t.Asking && t.Status == "in_progress" {
					c.asking = true
					if !t.Acknowledged {
						c.bell = true // asking also needs attention until visited
						if c.since == 0 {
							c.since = parseTrackerTimeUnix(t.StartedAt)
						}
					}
				}
				cacheByWindow[t.WindowID] = c
			}
		}
	}
	sinceHint := map[string]int64{}
	for wid, cs := range cacheByWindow {
		if cs.since > 0 {
			sinceHint[wid] = cs.since
		}
	}

	// Parse latest data
	freshByID := map[string]windowNavRow{}
	var freshOrder []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		w, ok := parseWindowNavLine(line)
		if !ok {
			continue
		}
		// bell/asking overlay from the tracker cache (not derivable from tmux fields):
		// asking is also reflected in status so the "?" persists after the bell is
		// acknowledged. limited (daemon-side also asking) keeps its more specific "L".
		cs := cacheByWindow[w.windowID]
		if cs.asking && w.status != "limited" {
			w.status = "asking"
		}
		w.bell = cs.bell || w.bell
		freshByID[w.windowID] = w
		freshOrder = append(freshOrder, w.windowID)
	}
	m.windowByID = freshByID

	// Build fresh slice in list-windows arrival order. Used both for the
	// first-load sort and as the input to updateLastBusyAt — we must
	// populate the sort key BEFORE sortWindows runs, otherwise the
	// activity sort falls back to "all 0 → preserve arrival order".
	fresh := make([]windowNavRow, 0, len(freshByID))
	for _, wid := range freshOrder {
		fresh = append(fresh, freshByID[wid])
	}
	m.updateLastBusyAt(fresh)
	m.updateAttentionSince(fresh, sinceHint)

	if len(m.allWindows) == 0 || m.sortDirty {
		// First load or explicit sort change: sort fresh data
		m.allWindows = m.sortWindows(fresh)
		m.sortDirty = false
	} else {
		// Subsequent refresh: update data in stable order, don't re-sort
		updated := make([]windowNavRow, 0, len(m.allWindows))
		for _, existing := range m.allWindows {
			if newData, ok := freshByID[existing.windowID]; ok {
				updated = append(updated, newData)
				delete(freshByID, existing.windowID)
			}
			// windows not in freshByID are gone (dropped)
		}
		// append new windows (appeared since last refresh) at end
		for _, wid := range freshOrder {
			if w, ok := freshByID[wid]; ok {
				updated = append(updated, w)
			}
		}
		m.allWindows = updated
	}

	m.rebuildRows()
	if !m.initialCursorSet {
		// Land on the first actionable row (the first 🔔 window) so Enter on
		// entry immediately handles the longest-waiting one.
		m.selected = m.firstSelectableRow()
		m.initialCursorSet = true
	} else if m.selected >= 0 && m.selected < len(m.rows) && m.rows[m.selected].isHeader {
		// Never leave the cursor parked on a group-name row after a refresh.
		m.selected = m.firstSelectableRow()
	}
	m.timerByWindow = timersByWindowMap()
	m.hasAnyTimers = len(m.timerByWindow) > 0
	m.allActivityToday = m.computeAllActivityToday()
}

// computeAllActivityToday returns true when every agent window that has been
// seen as busy at least once was last busy today (drives narrow time column).
func (m *windowNavPanelModel) computeAllActivityToday() bool {
	now := time.Now()
	hasAny := false
	for _, t := range m.lastBusyAtByWindow {
		if t == 0 {
			continue
		}
		hasAny = true
		ts := time.Unix(t, 0)
		if ts.Year() != now.Year() || ts.YearDay() != now.YearDay() {
			return false
		}
	}
	return hasAny
}

func (m *windowNavPanelModel) rebuildRows() {
	query := ""
	if len(m.searchQuery) > 0 {
		query = strings.ToLower(strings.TrimSpace(string(m.searchQuery)))
	}
	switch m.groupBy {
	case "session":
		m.buildSessionRows(query)
	default:
		m.buildFlatRows(query)
	}
	if len(m.rows) == 0 {
		m.selected = 0
	} else {
		m.selected = clampInt(m.selected, 0, len(m.rows)-1)
	}
}

func (m *windowNavPanelModel) buildSessionRows(query string) {
	// Windows needing attention (🔔 / asking) float into a single pinned group
	// at the very top, ordered by when their attention first appeared (earliest
	// first). They are pulled OUT of their session groups below to avoid
	// duplication, so the top list is a clean triage queue: pressing Enter on
	// entry handles the longest-waiting one.
	var attn []windowNavRow
	rest := make([]windowNavRow, 0, len(m.allWindows))
	for _, w := range m.allWindows {
		if needsAttention(w) {
			attn = append(attn, w)
		} else {
			rest = append(rest, w)
		}
	}
	sort.SliceStable(attn, func(i, j int) bool {
		return m.attentionSince(attn[i]) < m.attentionSince(attn[j])
	})

	var rows []windowNavRow

	// Pinned "需要处理" group (not collapsible; no sessionID so Enter is a no-op
	// on its header).
	var attnVisible []windowNavRow
	for _, w := range attn {
		if windowNavMatchesQuery(w, query) {
			attnVisible = append(attnVisible, w)
		}
	}
	if len(attnVisible) > 0 {
		rows = append(rows, windowNavRow{
			isHeader:    true,
			sessionName: fmt.Sprintf("需要处理 (%d)", len(attnVisible)),
		})
		rows = append(rows, attnVisible...)
	}

	// Remaining windows grouped by session: current session first, then by name.
	order := []string{}
	bySession := map[string][]windowNavRow{}
	nameOf := map[string]string{}
	for _, w := range rest {
		if _, ok := bySession[w.sessionID]; !ok {
			order = append(order, w.sessionID)
		}
		bySession[w.sessionID] = append(bySession[w.sessionID], w)
		nameOf[w.sessionID] = w.sessionName
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i] == m.currentSessionID {
			return true
		}
		if order[j] == m.currentSessionID {
			return false
		}
		return nameOf[order[i]] < nameOf[order[j]]
	})

	for _, sid := range order {
		// In-group order: idle agent > busy agent > non-agent. Stable within a
		// tier preserves the activity order set by sortWindows.
		wins := sortBySessionRank(bySession[sid])
		var visible []windowNavRow
		if query == "" {
			visible = wins
		} else {
			for _, w := range wins {
				if windowNavMatchesQuery(w, query) {
					visible = append(visible, w)
				}
			}
		}
		if query != "" && len(visible) == 0 {
			continue
		}
		// force expand when searching
		collapsed := m.collapsed[sid] && query == ""
		ind := "▾"
		if collapsed {
			ind = "▸"
		}
		cur := ""
		if sid == m.currentSessionID {
			cur = " ●"
		}
		hdrText := fmt.Sprintf("%s %s%s (%d)", ind, nameOf[sid], cur, len(wins))
		rows = append(rows, windowNavRow{
			isHeader:    true,
			sessionID:   sid,
			sessionName: hdrText,
			windowCount: len(wins),
		})
		if !collapsed {
			rows = append(rows, visible...)
		}
	}
	m.rows = rows
}

// windowSessionRank orders windows within a session group: idle agent (0) >
// busy agent (1) > everything else (2 — a non-agent window, or an agent window
// with no live session status, e.g. a lingering @agent_client tag after the
// agent exited). Attention windows are pulled to the top group before this runs,
// so asking agents don't appear here.
func windowSessionRank(w windowNavRow) int {
	switch {
	case w.isAgent && w.status == "idle":
		return 0
	case w.isAgent && w.status == "busy":
		return 1
	default:
		return 2
	}
}

func sortBySessionRank(wins []windowNavRow) []windowNavRow {
	out := make([]windowNavRow, len(wins))
	copy(out, wins)
	sort.SliceStable(out, func(i, j int) bool {
		return windowSessionRank(out[i]) < windowSessionRank(out[j])
	})
	return out
}

// attentionSince returns the tracked attention-appearance time for a window, or
// a far-future sentinel when unknown so it sorts after timestamped entries.
func (m *windowNavPanelModel) attentionSince(w windowNavRow) int64 {
	if t, ok := m.attentionSinceByWindow[w.windowID]; ok {
		return t
	}
	return int64(1) << 62
}

// firstSelectableRow returns the index of the first non-header row, or 0.
func (m *windowNavPanelModel) firstSelectableRow() int {
	for i, r := range m.rows {
		if !r.isHeader {
			return i
		}
	}
	return 0
}

func windowNavMatchesQuery(w windowNavRow, query string) bool {
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(w.windowName), query) ||
		strings.Contains(strings.ToLower(projectDisplayName(w.path)), query)
}

// needsAttention reports whether a window has unread attention. Asking/limited
// remain activity statuses after acknowledge, but no longer belong in the
// "需要处理" group once the user has visited the window.
func needsAttention(w windowNavRow) bool {
	return w.bell
}

func (m *windowNavPanelModel) buildFlatRows(query string) {
	if m.groupBy == "attention" {
		m.buildAttentionRows(query)
		return
	}
	prevGroup := ""
	var rows []windowNavRow
	for _, w := range m.allWindows {
		if !windowNavMatchesQuery(w, query) {
			continue
		}
		var groupKey string
		switch m.groupBy {
		case "status":
			groupKey = w.status
		case "path":
			groupKey = projectDisplayName(w.path)
		}
		if groupKey != "" && groupKey != prevGroup {
			rows = append(rows, windowNavRow{isHeader: true, sessionName: groupKey})
			prevGroup = groupKey
		}
		rows = append(rows, w)
	}
	m.rows = rows
}

// buildAttentionRows partitions windows into a "needs attention" group (🔔 /
// asking) and an "other" group. Two passes because the sorted order doesn't put
// attention windows adjacent, so the consecutive-key header logic can't apply.
func (m *windowNavPanelModel) buildAttentionRows(query string) {
	var attn, other []windowNavRow
	for _, w := range m.allWindows {
		if !windowNavMatchesQuery(w, query) {
			continue
		}
		if needsAttention(w) {
			attn = append(attn, w)
		} else {
			other = append(other, w)
		}
	}
	var rows []windowNavRow
	if len(attn) > 0 {
		rows = append(rows, windowNavRow{isHeader: true, sessionName: "需要处理"})
		rows = append(rows, attn...)
	}
	if len(other) > 0 {
		rows = append(rows, windowNavRow{isHeader: true, sessionName: "其它"})
		rows = append(rows, other...)
	}
	m.rows = rows
}

// lastBusyAt returns the panel's tracked last-busy unix timestamp for a row,
// or 0 when the row has never been seen as busy (rendered as "---" or blank).
// Non-agent windows always report 0 here.
func (m *windowNavPanelModel) lastBusyAt(row windowNavRow) int64 {
	if !row.isAgent {
		return 0
	}
	return m.lastBusyAtByWindow[row.windowID]
}

// updateLastBusyAt refreshes lastBusyAtByWindow from a fresh window set.
// Must run BEFORE sortWindows on first load / sortDirty, otherwise the
// activity sort key is empty and the list falls back to list-windows order.
// While a window is busy, the timestamp tracks the current second; on
// transition to idle/asking the last value is frozen (the !seen branch
// only seeds once, and the busy→non-busy branch leaves existing values
// intact).
func (m *windowNavPanelModel) updateLastBusyAt(wins []windowNavRow) {
	nowUnix := time.Now().Unix()
	for _, w := range wins {
		if !w.isAgent {
			continue
		}
		if w.status == "busy" {
			m.lastBusyAtByWindow[w.windowID] = nowUnix
		} else if _, seen := m.lastBusyAtByWindow[w.windowID]; !seen {
			// not yet tracked in this panel session → seed from tmux option
			if w.lastBusyAt > 0 {
				m.lastBusyAtByWindow[w.windowID] = w.lastBusyAt
			}
		}
	}
}

// parseTrackerTimeUnix parses an RFC3339 timestamp from the tracker cache
// (started_at / completed_at), returning unix seconds or 0 on empty/parse error.
func parseTrackerTimeUnix(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// updateAttentionSince records when each window first entered the needs-attention
// state and clears entries for windows that no longer need attention. sinceHint
// carries cache timestamps (completed_at / started_at) so the ordering stays
// meaningful and stable across panel reopens; windows without a hint are stamped
// with first-seen time.
func (m *windowNavPanelModel) updateAttentionSince(wins []windowNavRow, sinceHint map[string]int64) {
	now := time.Now().Unix()
	live := map[string]bool{}
	for _, w := range wins {
		if !needsAttention(w) {
			continue
		}
		live[w.windowID] = true
		if _, ok := m.attentionSinceByWindow[w.windowID]; ok {
			continue
		}
		since := sinceHint[w.windowID]
		if since == 0 {
			since = now
		}
		m.attentionSinceByWindow[w.windowID] = since
	}
	for wid := range m.attentionSinceByWindow {
		if !live[wid] {
			delete(m.attentionSinceByWindow, wid)
		}
	}
}

// windowLivenessTier returns 0 for a live agent window (has an idle/busy/asking
// session status) and 1 for everything else — a non-agent window or an agent
// window whose session has exited (status ""). Used as the primary sort key so
// dead/non-agent windows always sink below live agents.
func windowLivenessTier(w windowNavRow) int {
	if w.isAgent && (w.status == "idle" || w.status == "busy" || w.status == "asking" ||
		w.status == "limited") {
		return 0
	}
	return 1
}

func (m *windowNavPanelModel) sortWindows(wins []windowNavRow) []windowNavRow {
	out := make([]windowNavRow, len(wins))
	copy(out, wins)
	sort.SliceStable(out, func(i, j int) bool {
		// Primary tier (every order mode): live agents — those with a real
		// session status (idle/busy/asking) — sort above everything else, so a
		// window with no running agent (a non-agent window, or a lingering
		// @agent_client tag after the agent exited, status "") always sinks to
		// the bottom instead of leading the list.
		ti, tj := windowLivenessTier(out[i]), windowLivenessTier(out[j])
		if ti != tj {
			return ti < tj
		}
		if m.orderBy == "index" {
			a, b := int64(out[i].windowIndex), int64(out[j].windowIndex)
			if m.orderDir == "asc" {
				return a < b
			}
			return a > b
		}
		// Activity (Last) order: sort by lastBusyAt (same source as the "Last"
		// column in renderRow) so the list order matches what the user sees.
		// Rows with no last-busy timestamp (== 0, e.g. non-agent windows or
		// agents that have never been busy) always sort last, regardless of
		// asc/desc — they're an unordered tail, not a sortable bucket.
		ai, aj := m.lastBusyAt(out[i]), m.lastBusyAt(out[j])
		noTimeI, noTimeJ := ai == 0, aj == 0
		if noTimeI != noTimeJ {
			return noTimeJ // has-time comes first, no-time goes last
		}
		if m.orderDir == "asc" {
			return ai < aj
		}
		return ai > aj
	})
	return out
}

// ── BubbleTea ────────────────────────────────────────────────────────────────

type windowNavAnimTickMsg struct{}

// windowNavJumpMsg fires after the digit-buffer settle delay; seq guards against stale timers.
type windowNavJumpMsg struct{ seq int }

func windowNavTickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return windowNavTickMsg{} })
}

func windowNavAnimTickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return windowNavAnimTickMsg{} })
}

func (m *windowNavPanelModel) Init() tea.Cmd {
	return tea.Batch(windowNavTickCmd(), windowNavAnimTickCmd())
}

func (m *windowNavPanelModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case windowNavTickMsg:
		// save selected windowID so we can restore it after refresh
		selectedWID := ""
		if m.selected >= 0 && m.selected < len(m.rows) {
			if row := m.rows[m.selected]; !row.isHeader {
				selectedWID = row.windowID
			}
		}
		m.refresh()
		// restore selection by windowID
		if selectedWID != "" {
			for i, row := range m.rows {
				if row.windowID == selectedWID {
					m.selected = i
					break
				}
			}
		}
		m.selected = clampInt(m.selected, 0, maxInt(0, len(m.rows)-1))
		return m, windowNavTickCmd()
	case windowNavAnimTickMsg:
		return m, windowNavAnimTickCmd()
	case windowNavJumpMsg:
		if msg.seq != m.jumpSeq {
			return m, nil // stale timer; a newer digit or key superseded it
		}
		return m.resolveJump()
	case tea.KeyMsg:
		if m.promptViewActive {
			return m.handlePromptViewKey(msg.String())
		}
		if m.timerPanelActive && m.timerPanel != nil {
			if msg.String() == "esc" || msg.String() == "g" || msg.String() == "G" {
				m.timerPanelActive = false
				m.timerPanel = nil
				return m, nil
			}
			_, cmd := m.timerPanel.Update(msg)
			if m.timerPanel.requestBack {
				m.timerPanelActive = false
				m.timerPanel = nil
				return m, nil
			}
			return m, cmd
		}
		return m.handleKey(paletteKeyString(msg))
	case tea.MouseMsg:
		if m.promptViewActive || (m.timerPanelActive && m.timerPanel != nil) {
			return m, nil
		}
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.move(-1)
			return m, nil
		case tea.MouseButtonWheelDown:
			m.move(1)
			return m, nil
		case tea.MouseButtonLeft:
			if idx, ok := m.rowIndexAtY(msg.Y); ok {
				m.selected = idx
				return m.selectCurrent()
			}
		}
		return m, nil
	}
	return m, nil
}

// rowIndexAtY maps a mouse Y coordinate to an index in m.rows, returning
// ok=false for clicks outside the list body or on group-header rows.
// Layout (see render + renderList): [title? · 1 line] "" [col header · 1 line]
// body-rows … "" footer. In directMode the title row is absent.
func (m *windowNavPanelModel) rowIndexAtY(y int) (int, bool) {
	headerLines := 0
	if !m.directMode {
		headerLines = 1
	}
	firstRowY := headerLines + 2 // headerLines + blank + column header
	rel := y - firstRowY
	if rel < 0 {
		return 0, false
	}
	idx := m.scrollOffset + rel
	if idx < 0 || idx >= len(m.rows) {
		return 0, false
	}
	if m.rows[idx].isHeader {
		return 0, false
	}
	return idx, true
}

func (m *windowNavPanelModel) handleKey(key string) (tea.Model, tea.Cmd) {
	if m.searchActive {
		return m.handleSearchKey(key)
	}
	return m.handleNavKey(key)
}

func (m *windowNavPanelModel) handleNavKey(key string) (tea.Model, tea.Cmd) {
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		return m.handleJumpDigit(key)
	}
	if m.jumpBuffer != "" {
		// any non-digit key cancels a pending jump and voids its timer
		m.jumpBuffer = ""
		m.jumpSeq++
	}
	switch key {
	case "esc", "ctrl+c", "q":
		if m.directMode {
			return m, tea.Quit
		}
		m.requestBack = true
		return m, nil
	case "j", "J", "down", "ctrl+j":
		m.move(1)
	case "k", "K", "up", "ctrl+k":
		m.move(-1)
	case "g":
		switch m.groupBy {
		case "session":
			m.groupBy = "none"
		case "none":
			m.groupBy = "status"
		case "status":
			m.groupBy = "path"
		case "path":
			m.groupBy = "attention"
		default:
			m.groupBy = "session"
		}
		m.sortDirty = true
		m.refresh()
		m.persistSettings()
	case "o":
		if m.orderBy == "activity" {
			m.orderBy = "index"
		} else {
			m.orderBy = "activity"
		}
		m.sortDirty = true
		m.refresh()
		m.persistSettings()
	case "r":
		if m.orderDir == "asc" {
			m.orderDir = "desc"
		} else {
			m.orderDir = "asc"
		}
		m.sortDirty = true
		m.refresh()
		m.persistSettings()
	case "t", "T":
		return m.openTimerPanel()
	case "p", "P":
		return m.openPromptView()
	case "x", "X":
		return m.killSelected()
	case "f", "F":
		m.searchActive = true
		m.searchQuery = nil
		m.searchCursor = 0
	case "enter":
		return m.selectCurrent()
	}
	return m, nil
}

func (m *windowNavPanelModel) handleSearchKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.searchActive = false
		m.searchQuery = nil
		m.searchCursor = 0
		m.selected = 0
		m.scrollOffset = 0
		m.rebuildRows()
	case "enter":
		// keep query, exit search mode (list stays filtered)
		m.searchActive = false
	case "ctrl+c":
		if m.directMode {
			return m, tea.Quit
		}
		m.requestBack = true
	default:
		if applyPaletteInputKey(key, &m.searchQuery, &m.searchCursor, false) {
			m.selected = 0
			m.scrollOffset = 0
			m.rebuildRows()
		}
	}
	return m, nil
}

func (m *windowNavPanelModel) move(delta int) {
	n := len(m.rows)
	if n == 0 {
		return
	}
	// Step in `delta` direction, skipping header (group-name) rows so the cursor
	// only ever lands on selectable windows. Wrap around; give up after a full
	// loop when every row is a header.
	for i := 1; i <= n; i++ {
		next := ((m.selected+delta*i)%n + n) % n
		if !m.rows[next].isHeader {
			m.selected = next
			return
		}
	}
}

func (m *windowNavPanelModel) killSelected() (tea.Model, tea.Cmd) {
	if m.selected < 0 || m.selected >= len(m.rows) {
		return m, nil
	}
	row := m.rows[m.selected]
	if row.isHeader || row.windowID == "" {
		return m, nil
	}
	if err := runTmux("kill-window", "-t", row.windowID); err != nil {
		m.setStatus("kill failed: " + err.Error())
	} else {
		m.setStatus(fmt.Sprintf("killed %s", row.windowName))
	}
	m.refresh()
	return m, nil
}

func (m *windowNavPanelModel) selectCurrent() (tea.Model, tea.Cmd) {
	if m.selected < 0 || m.selected >= len(m.rows) {
		return m, nil
	}
	row := m.rows[m.selected]
	if row.isHeader {
		if m.groupBy == "session" && row.sessionID != "" {
			// toggle collapse
			m.collapsed[row.sessionID] = !m.collapsed[row.sessionID]
			m.rebuildRows()
		}
		return m, nil
	}
	if row.windowID == "" {
		return m, nil
	}
	return m.activateWindow(row)
}

// activateWindow switches to the window's session (if needed), selects it, and closes the panel.
func (m *windowNavPanelModel) activateWindow(row windowNavRow) (tea.Model, tea.Cmd) {
	// Cross-session: switch to the session first, then select the window.
	if row.sessionID != "" && row.sessionID != m.currentSessionID {
		_ = runTmux("switch-client", "-t", row.sessionID)
	}
	_ = runTmux("select-window", "-t", row.windowID)
	if m.directMode {
		return m, tea.Quit
	}
	m.requestClose = true
	return m, tea.Quit
}

// handleJumpDigit accumulates a typed window-index digit. It jumps immediately once the
// buffer can't be extended into a longer index; otherwise it waits briefly for more digits.
func (m *windowNavPanelModel) handleJumpDigit(d string) (tea.Model, tea.Cmd) {
	m.jumpBuffer += d
	hasLonger := false
	for i := range m.allWindows {
		idxStr := strconv.Itoa(m.allWindows[i].windowIndex)
		if len(idxStr) > len(m.jumpBuffer) && strings.HasPrefix(idxStr, m.jumpBuffer) {
			hasLonger = true
			break
		}
	}
	if !hasLonger {
		// unambiguous (or no match at all) → resolve now
		return m.resolveJump()
	}
	// ambiguous prefix (e.g. "1" with both #1 and #12 present): wait for the next digit
	m.jumpSeq++
	seq := m.jumpSeq
	m.setStatus("jump → #" + m.jumpBuffer)
	return m, tea.Tick(450*time.Millisecond, func(t time.Time) tea.Msg {
		return windowNavJumpMsg{seq: seq}
	})
}

// resolveJump consumes the digit buffer and jumps to the matching window index.
func (m *windowNavPanelModel) resolveJump() (tea.Model, tea.Cmd) {
	buf := m.jumpBuffer
	m.jumpBuffer = ""
	m.jumpSeq++ // void any pending timer
	if buf == "" {
		return m, nil
	}
	idx, err := strconv.Atoi(buf)
	if err != nil {
		return m, nil
	}
	return m.jumpToIndex(idx)
}

// jumpToIndex activates the window whose index matches, preferring the current session.
func (m *windowNavPanelModel) jumpToIndex(idx int) (tea.Model, tea.Cmd) {
	var match *windowNavRow
	for i := range m.allWindows {
		if m.allWindows[i].windowIndex != idx {
			continue
		}
		w := m.allWindows[i]
		if w.sessionID == m.currentSessionID {
			match = &w
			break
		}
		if match == nil {
			match = &w
		}
	}
	if match == nil {
		m.setStatus(fmt.Sprintf("no window #%d", idx))
		return m, nil
	}
	return m.activateWindow(*match)
}

func (m *windowNavPanelModel) openTimerPanel() (tea.Model, tea.Cmd) {
	if m.selected < 0 || m.selected >= len(m.rows) {
		return m, nil
	}
	row := m.rows[m.selected]
	if row.isHeader || row.windowID == "" {
		return m, nil
	}
	m.timerPanel = newWindowTimerPanel(row.windowID, row.windowName)
	m.timerPanel.width = m.width
	m.timerPanel.height = m.height
	m.timerPanelActive = true
	return m, nil
}

func (m *windowNavPanelModel) openPromptView() (tea.Model, tea.Cmd) {
	if m.selected < 0 || m.selected >= len(m.rows) {
		return m, nil
	}
	row := m.rows[m.selected]
	if row.isHeader || row.windowID == "" {
		return m, nil
	}
	prompt := promptForWindow(row.windowID)
	if strings.TrimSpace(prompt) == "" {
		m.setStatus("prompt not found")
		return m, nil
	}
	m.promptViewActive = true
	m.promptViewRow = row
	m.promptText = prompt
	m.promptScroll = 0
	return m, nil
}

func (m *windowNavPanelModel) handlePromptViewKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "h", "H", "q":
		m.promptViewActive = false
		m.promptText = ""
		m.promptScroll = 0
	case "j", "J", "down", "ctrl+j":
		m.promptScroll++
	case "k", "K", "up", "ctrl+k":
		if m.promptScroll > 0 {
			m.promptScroll--
		}
	case "c", "C":
		if err := writeClipboard(m.promptText); err != nil {
			m.setStatus("copy failed: " + err.Error())
		} else {
			m.setStatus("prompt copied")
		}
	}
	return m, nil
}

func promptForWindow(windowID string) string {
	ci := buildClaudeIndex()
	aiPane := agentAIPane(windowID, &ci)
	if aiPane == "" {
		return ""
	}
	if meta, _, ok := ci.sessionForPanePID(panePID(aiPane)); ok {
		return claudePromptFromSession(meta)
	}
	if meta, _, ok := codexThreadForPane(aiPane, &ci); ok {
		return codexPromptFromRollout(meta.RolloutPath)
	}
	return ""
}

func (m *windowNavPanelModel) setStatus(msg string) {
	m.statusMsg = msg
	m.statusUntil = time.Now().Add(20 * time.Second)
}

func (m *windowNavPanelModel) currentStatus() string {
	if m.statusMsg == "" {
		return ""
	}
	if !m.statusUntil.IsZero() && time.Now().After(m.statusUntil) {
		m.statusMsg = ""
		return ""
	}
	return m.statusMsg
}

// ── view ─────────────────────────────────────────────────────────────────────

func (m *windowNavPanelModel) View() string {
	if m.promptViewActive {
		return m.renderPromptView(newPaletteStyles(), m.width, m.height)
	}
	if m.timerPanelActive && m.timerPanel != nil {
		m.timerPanel.width = m.width
		m.timerPanel.height = m.height
		return m.timerPanel.View()
	}
	return m.render(newPaletteStyles(), m.width, m.height)
}

func (m *windowNavPanelModel) render(styles paletteStyles, width, height int) string {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 26
	}

	// header: title only (not in direct/standalone mode — popup title bar already shows it)
	var headerParts []string
	if !m.directMode {
		titleRow := styles.title.Render("Windows")
		if m.currentStatus() != "" {
			titleRow = titleRow + "  " + styles.statusBad.Render(m.currentStatus())
		}
		headerParts = append(headerParts, titleRow)
	}

	// footer: search box or shortcut hints (wraps to a 2nd line when too narrow)
	var footer string
	footerLines := 1
	renderSeg := func(pairs [][2]string) string {
		return renderShortcutPairs(
			func(v string) string { return styles.shortcutKey.Render(v) },
			func(v string) string { return styles.shortcutText.Render(v) },
			"   ", pairs,
		)
	}
	if m.searchActive {
		queryStr := renderInputValue(m.searchQuery, m.searchCursor, styles)
		footer = styles.searchBox.Width(width - 2).Render(
			styles.searchPrompt.Render("SEARCH>") + " " + styles.input.Render(queryStr),
		)
	} else if len(m.searchQuery) > 0 {
		footer = styles.searchBox.Width(width - 2).Render(
			styles.searchPrompt.Render(">") + " " + styles.meta.Render(string(m.searchQuery)) +
				styles.meta.Render("  [f: edit  esc: clear]"),
		)
	} else {
		footer, footerLines = renderWrappedShortcutFooter(width-2, renderSeg, 2,
			[][2]string{{"j/k", "nav"}, {"0-9", "jump"}, {"Enter", "sel"}, {"Esc", "back"}, {"f", "search"}, {"p", "prompt"}, {"t", "timer"}, {"g", m.groupBy}, {"o", "order"}, {"r", "flip"}, {"x", "del"}},
		)
		// When the hints wrap to two rows, insert a blank spacer line between them
		// so the two rows aren't cramped together (a wide popup keeps them on one).
		if footerLines == 2 {
			footer = strings.Replace(footer, "\n", "\n\n", 1)
			footerLines = 3
		}
	}

	headerLines := len(headerParts)
	bodyHeight := maxInt(4, height-headerLines-footerLines-2) // 2 blank separators
	body := m.renderList(styles, width-2, bodyHeight)

	viewParts := append(headerParts, "", body, "", footer)
	view := lipgloss.JoinVertical(lipgloss.Left, viewParts...)
	return lipgloss.NewStyle().Width(width).Height(height).Padding(0, 1).Render(view)
}

func (m *windowNavPanelModel) renderPromptView(styles paletteStyles, width, height int) string {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 26
	}
	title := "Prompt"
	if name := strings.TrimSpace(m.promptViewRow.windowName); name != "" {
		title += " · " + truncateWidth(name, maxInt(12, width-14))
	}
	header := styles.title.Render(title)
	if status := m.currentStatus(); status != "" {
		header += "  " + styles.meta.Render(status)
	}
	footer := styles.searchBox.Width(width - 2).Render(
		renderShortcutPairs(
			func(v string) string { return styles.shortcutKey.Render(v) },
			func(v string) string { return styles.shortcutText.Render(v) },
			"   ",
			[][2]string{{"j/k", "scroll"}, {"c", "copy"}, {"Esc", "back"}},
		),
	)
	bodyHeight := maxInt(4, height-4)
	bodyWidth := maxInt(20, width-2)
	lines := wrapText(m.promptText, bodyWidth)
	if m.promptScroll > maxInt(0, len(lines)-bodyHeight) {
		m.promptScroll = maxInt(0, len(lines)-bodyHeight)
	}
	start := clampInt(m.promptScroll, 0, maxInt(0, len(lines)-1))
	end := minInt(len(lines), start+bodyHeight)
	var rendered []string
	for _, line := range lines[start:end] {
		rendered = append(rendered, styles.panelText.Render(truncateWidth(line, bodyWidth)))
	}
	body := lipgloss.NewStyle().Width(bodyWidth).Height(bodyHeight).Render(strings.Join(rendered, "\n"))
	view := lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", footer)
	return lipgloss.NewStyle().Width(width).Height(height).Padding(0, 1).Render(view)
}

// truncateWidth shortens text to a target display width (CJK-aware: wide runes
// count as 2 cells), appending "…". Unlike truncate() which counts runes, this
// guarantees the result's display width is ≤ width, so columns don't overflow.
func truncateWidth(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	target := width - 1 // leave a cell for the ellipsis
	var b strings.Builder
	w := 0
	for _, r := range text {
		rw := lipgloss.Width(string(r))
		if w+rw > target {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}

// navCols decides which trailing columns fit at a given width, dropping the
// least essential first (model → provider → directory → activity) so the Name
// column always keeps a usable width on narrow / portrait popups.
type navCols struct {
	provider, model, dir, time, timer bool
	nameWidth                         int
	timeWidth                         int // content width of time column: 5 (today, "15:04") or 11 ("01/02 15:04")
}

func computeNavColumns(width int, hasTimers bool, allActivityToday bool) navCols {
	const (
		provSeg  = 11 // "  " + provider(9)
		modSeg   = 13 // " "  + model(12)
		dirSeg   = 19 // " "  + directory(18)
		timerSeg = 11 // " "  + timer(10) e.g. "[3]12:10"
		baseSeg  = 10 // marker(2)+bell(2)+St(3)+idx(2)+name lead(1)
		nameMin  = 10
	)
	timeWidth := 11 // "01/02 15:04"
	if allActivityToday {
		timeWidth = 5 // "15:04"
	}
	timeSeg := 1 + timeWidth // space separator + content
	c := navCols{provider: true, model: true, dir: true, time: true, timer: hasTimers, timeWidth: timeWidth}
	opt := func() int {
		s := 0
		if c.provider {
			s += provSeg
		}
		if c.model {
			s += modSeg
		}
		if c.dir {
			s += dirSeg
		}
		if c.time {
			s += timeSeg
		}
		if c.timer {
			s += timerSeg
		}
		return s
	}
	for width-baseSeg-opt() < nameMin {
		switch {
		case c.model:
			c.model = false
		case c.timer:
			c.timer = false
		case c.provider:
			c.provider = false
		case c.dir:
			c.dir = false
		case c.time:
			c.time = false
		default:
			c.nameWidth = maxInt(6, width-baseSeg) // nothing left to drop
			return c
		}
	}
	c.nameWidth = maxInt(8, width-baseSeg-opt())
	return c
}

func (m *windowNavPanelModel) renderColumnHeader(styles paletteStyles, width int) string {
	const (
		providerWidth = 9
		modelWidth    = 12
		dirWidth      = 18
	)
	c := computeNavColumns(width, m.hasAnyTimers, m.allActivityToday)
	pad := func(s string, w int) string {
		return s + strings.Repeat(" ", maxInt(0, w-lipgloss.Width(s)))
	}
	lbl := styles.muted
	// order indicator: an arrow on whichever column the list is sorted by.
	arrow := "↑"
	if m.orderDir == "desc" {
		arrow = "↓"
	}
	idxHdr := " #"        // kept at 2 display cells to preserve column alignment
	activityHdr := "Last" // last column → free to grow
	sortingByActivity := m.orderBy != "index"
	if m.orderBy == "index" {
		idxHdr = "#" + arrow
	} else {
		activityHdr = "Last " + arrow // e.g. "Last ↑" = 6 chars (fits timeWidth=11)
	}
	// marker(2) + bell(2) left blank; "St "(3) aligns with icon; " #"(2) aligns with idx; " "(1) is name leading space.
	line := "  " + "  " + lbl.Render("St ") + lbl.Render(idxHdr) + " " + lbl.Render(pad("Name", c.nameWidth))
	if c.provider {
		line += "  " + lbl.Render(pad("Provider", providerWidth))
	}
	if c.model {
		line += " " + lbl.Render(pad("Model", modelWidth))
	}
	if c.dir {
		line += " " + lbl.Render(pad("Directory", dirWidth))
	}
	if c.timer {
		line += " " + lbl.Render(pad("Timer", 10))
	}
	if c.time {
		hdr := activityHdr
		if lipgloss.Width(hdr) > c.timeWidth {
			// narrow column (timeWidth=5): "Last ↑" (6) exceeds budget → no space
			if sortingByActivity {
				hdr = "Last" + arrow // "Last↑" or "Last↓" = 5 chars, fits exactly
			} else {
				hdr = "Last"
			}
		}
		line += " " + lbl.Render(hdr)
	}
	return lipgloss.NewStyle().Width(width).Render(line)
}

func (m *windowNavPanelModel) renderList(styles paletteStyles, width, height int) string {
	colHeader := m.renderColumnHeader(styles, width)
	listHeight := maxInt(1, height-1) // reserve 1 line for column header

	if len(m.rows) == 0 {
		msg := "No windows"
		if len(m.searchQuery) > 0 {
			msg = fmt.Sprintf("No windows matching %q", string(m.searchQuery))
		}
		return lipgloss.JoinVertical(lipgloss.Left,
			colHeader,
			lipgloss.NewStyle().Width(width).Height(listHeight).Render(styles.muted.Render(msg)),
		)
	}

	sel := clampInt(m.selected, 0, len(m.rows)-1)

	// scroll: keep selected visible
	if sel < m.scrollOffset {
		m.scrollOffset = sel
	} else if sel >= m.scrollOffset+listHeight {
		m.scrollOffset = sel - listHeight + 1
	}
	m.scrollOffset = clampInt(m.scrollOffset, 0, maxInt(0, len(m.rows)-listHeight))

	var lines []string
	for i := m.scrollOffset; i < m.scrollOffset+listHeight && i < len(m.rows); i++ {
		lines = append(lines, m.renderRow(styles, m.rows[i], i == sel, width))
	}
	content := strings.Join(lines, "\n")
	return lipgloss.JoinVertical(lipgloss.Left,
		colHeader,
		lipgloss.NewStyle().Width(width).Height(listHeight).Render(content),
	)
}

func (m *windowNavPanelModel) renderRow(styles paletteStyles, row windowNavRow, selected bool, width int) string {
	if row.isHeader {
		text := truncateWidth(row.sessionName, maxInt(4, width-2))
		if selected {
			bg := lipgloss.Color("238")
			return lipgloss.NewStyle().Width(width).Background(bg).Foreground(lipgloss.Color("153")).Bold(true).Render("› " + text)
		}
		return styles.sectionLabel.Width(width).Render("  " + text)
	}

	// status icon (pure activity): spinner=busy, ?=asking, I=idle, space=plain.
	// All icons rendered as 3 display cells: char(1)+2spaces.
	now := time.Now()
	var statusFg lipgloss.Color
	var statusIcon string
	switch row.status {
	case "busy":
		frames := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
		statusIcon = string(frames[int(now.UnixNano()/int64(100*time.Millisecond))%len(frames)]) + "  "
		statusFg = lipgloss.Color("214") // orange
	case "asking":
		statusIcon = "?  "
		statusFg = lipgloss.Color("226") // yellow — waiting for input
	case "limited":
		statusIcon = "L  "
		statusFg = lipgloss.Color("203") // red — usage limit hit, blocked
	case "idle":
		statusIcon = "I  "
		statusFg = lipgloss.Color("244") // gray
	default:
		statusIcon = "   "
		statusFg = lipgloss.Color("244")
	}

	// bell column (2 cells): independent attention flag, shown left of the status icon.
	bellIcon := "  "
	if row.bell {
		bellIcon = "🔔"
	}

	// Activity: only meaningful for agent windows; tracks last-busy timestamp.
	timeStr := ""
	if row.isAgent {
		if lastBusy := m.lastBusyAtByWindow[row.windowID]; lastBusy > 0 {
			ts := time.Unix(lastBusy, 0)
			now := time.Now()
			if ts.Year() == now.Year() && ts.YearDay() == now.YearDay() {
				timeStr = ts.Format("15:04")
			} else {
				timeStr = ts.Format("01/02 15:04")
			}
		} else {
			timeStr = "---"
		}
	}

	// Build provider and model display strings.
	var providerStr, modelStr string
	if row.isAgent {
		switch {
		case row.agentClient == "codex":
			providerStr = "Codex"
		default:
			providerStr = row.agentProvider
		}
		modelStr = normalizeModelNameLong(row.agentModel)
	}

	// Build name: prefer agentTitle (no model suffix); fall back to window name with suffix stripped.
	displayName := row.windowName
	if row.isAgent && row.agentTitle != "" {
		displayName = row.agentTitle
	} else if row.isAgent && row.agentModel != "" {
		suffix := " (" + normalizeModelNameLong(row.agentModel) + ")"
		displayName = strings.TrimSuffix(row.windowName, suffix)
	}
	// Match the tmux tab: drop a leading YYYY-MM-DD- when the setting is on, and
	// strip control chars / '#' (agentTitle may be a raw user-typed title).
	displayName = maybeStripDatePrefix(sanitizeWindowMarker(displayName), m.stripDatePrefix)
	// Column widths are responsive: narrow popups drop model/provider/dir/time.
	const (
		providerWidth = 9
		modelWidth    = 12
		dirWidth      = 18
	)
	c := computeNavColumns(width, m.hasAnyTimers, m.allActivityToday)
	nameWidth := c.nameWidth

	idxStr := fmt.Sprintf("%2d", row.windowIndex)
	nameStr := truncateWidth(displayName, nameWidth)
	namePad := strings.Repeat(" ", maxInt(0, nameWidth-lipgloss.Width(nameStr)))
	provFmt := truncate(providerStr, providerWidth)
	provPad := strings.Repeat(" ", maxInt(0, providerWidth-lipgloss.Width(provFmt)))
	modFmt := truncate(modelStr, modelWidth)
	modPad := strings.Repeat(" ", maxInt(0, modelWidth-lipgloss.Width(modFmt)))
	dirStr := ""
	if row.isAgent {
		dirStr = truncateWidth(row.agentDir, dirWidth)
	}
	dirPad := strings.Repeat(" ", maxInt(0, dirWidth-lipgloss.Width(dirStr)))

	if selected {
		bg := lipgloss.Color("238")
		fg := lipgloss.Color("230")
		mutedFg := lipgloss.Color("244")
		provFg := lipgloss.Color("75") // blue for provider
		modFg := lipgloss.Color("110") // blue-gray for model
		dirFg := lipgloss.Color("150") // green-gray for dir
		base := lipgloss.NewStyle().Background(bg)
		out := base.Foreground(fg).Bold(true).Render("› ") +
			base.Render(bellIcon) +
			base.Foreground(statusFg).Bold(true).Render(statusIcon) +
			base.Foreground(mutedFg).Render(idxStr) +
			base.Foreground(fg).Render(" "+nameStr+namePad)
		if c.provider {
			out += base.Foreground(provFg).Render("  " + provFmt + provPad)
		}
		if c.model {
			out += base.Foreground(modFg).Render(" " + modFmt + modPad)
		}
		if c.dir {
			out += base.Foreground(dirFg).Render(" " + dirStr + dirPad)
		}
		if c.timer {
			timerStr := m.timerByWindow[row.windowID]
			if timerStr != "" {
				out += base.Foreground(lipgloss.Color("220")).Render(" " + padRight(timerStr, 10))
			} else {
				out += base.Foreground(mutedFg).Render(" " + padRight("N/A", 10))
			}
		}
		if c.time {
			out += base.Foreground(mutedFg).Render(" " + timeStr)
		}
		return lipgloss.NewStyle().Width(width).Background(bg).Render(out)
	}

	// Current window's name mirrors status bar's selected window
	// (window-status-current-format: #[fg=colour51,bold]). The cursor-selected
	// row keeps its bg highlight; this cyan+bold overlay distinguishes the
	// active tmux window from peers when the cursor is elsewhere.
	nameStyle := styles.panelText
	if row.windowID != "" && row.windowID == m.currentWindowID {
		nameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("51")).Bold(true)
	}
	out := "  " + bellIcon +
		lipgloss.NewStyle().Foreground(statusFg).Bold(true).Render(statusIcon) +
		styles.muted.Render(idxStr) +
		nameStyle.Render(" "+nameStr+namePad)
	if c.provider {
		provStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
		if !row.isAgent {
			provStyle = styles.muted
		}
		out += provStyle.Render("  " + provFmt + provPad)
	}
	if c.model {
		out += lipgloss.NewStyle().Foreground(lipgloss.Color("110")).Render(" " + modFmt + modPad)
	}
	if c.dir {
		out += lipgloss.NewStyle().Foreground(lipgloss.Color("150")).Render(" " + dirStr + dirPad)
	}
	if c.timer {
		timerStr := m.timerByWindow[row.windowID]
		if timerStr != "" {
			out += lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Render(" " + padRight(timerStr, 10))
		} else {
			out += styles.muted.Render(" " + padRight("N/A", 10))
		}
	}
	if c.time {
		out += styles.muted.Render(" " + timeStr)
	}
	return lipgloss.NewStyle().Width(width).Render(out)
}

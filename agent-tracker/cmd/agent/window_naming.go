package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/david/agent-tracker/internal/agentclient"
	"github.com/david/agent-tracker/internal/statustag"
)

// 窗口自动命名：agentWindowName 拼名、状态前缀、provider/model 探测、
// 标题截断与净化。从 claude_session.go 拆出。

// statusTag maps a Claude session status to the window-name prefix.
// statusTag renders a live status as its window-name prefix. The vocabulary
// (and the shell→busy / waiting→asking aliasing) lives in internal/statustag,
// shared with tracker-server's remote-prefix parsing.
func statusTag(status string) string {
	return statustag.ForStatus(status)
}

// providersRelDir is the provider .env directory relative to home
// (HAT-574: 迁入 ~/.hat-env/providers 单一真身).
const providersRelDir = ".hat-env/providers"

// loadProviderMap reads ~/.hat-env/providers/*.env into a map from the provider's
// ANTHROPIC_BASE_URL to its name (file basename). official unsets the URL and so
// is absent (treated as the empty-URL default).
func loadProviderMap() map[string]string {
	m := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(homeDir(), providersRelDir))
	if err != nil {
		return m
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".env") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(homeDir(), providersRelDir, e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "unset") || !strings.Contains(line, "ANTHROPIC_BASE_URL=") {
				continue
			}
			v := line[strings.Index(line, "ANTHROPIC_BASE_URL=")+len("ANTHROPIC_BASE_URL="):]
			if v = strings.Trim(strings.TrimSpace(v), `"'`); v != "" {
				m[v] = strings.TrimSuffix(e.Name(), ".env")
			}
			break
		}
	}
	return m
}

// providerForPID reads the Claude process env's ANTHROPIC_BASE_URL and maps it to
// a provider name. Empty/unset → "anthropic" (the official login provider, which unsets the URL).
func providerForPID(pid int, providers map[string]string) string {
	if pid <= 0 {
		return ""
	}
	out, err := runCommandCombinedOutput(3*time.Second, "ps", "eww", "-p", strconv.Itoa(pid))
	if err != nil {
		return ""
	}
	for _, tok := range strings.Fields(string(out)) {
		if strings.HasPrefix(tok, "ANTHROPIC_BASE_URL=") {
			url := strings.TrimPrefix(tok, "ANTHROPIC_BASE_URL=")
			if name, ok := providers[url]; ok {
				return name
			}
			return ""
		}
	}
	return "anthropic"
}

// agentAIPane returns the window's primary AI pane (@agent_pane_role=ai),
// falling back to its active pane. Empty when the window has no panes.
func agentAIPane(windowID string, ci *claudeIndex) string {
	if strings.TrimSpace(windowID) == "" {
		return ""
	}
	out, err := runTmuxOutput("list-panes", "-t", windowID, "-F", "#{pane_id}::#{@agent_pane_role}::#{pane_active}")
	if err != nil {
		return ""
	}
	active := ""
	var paneIDs []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "::", 3)
		if len(parts) != 3 || parts[0] == "" {
			continue
		}
		if parts[1] == paneRoleAI {
			return parts[0]
		}
		paneIDs = append(paneIDs, parts[0])
		if parts[2] == "1" && active == "" {
			active = parts[0]
		}
	}
	// No pane tagged role=ai (e.g. a window rebuilt by workspace-restore that
	// lost its @agent_pane_role). Prefer the pane whose process tree actually
	// hosts a Claude session over the active pane, which may be the git/run/zsh
	// pane when the user has focus there.
	if ci != nil {
		for _, p := range paneIDs {
			if _, _, ok := ci.sessionForPanePID(panePID(p)); ok {
				return p
			}
		}
	}
	return active
}

func panePID(paneID string) int {
	if strings.TrimSpace(paneID) == "" {
		return 0
	}
	out, err := runTmuxOutput("display-message", "-p", "-t", paneID, "#{pane_pid}")
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0
	}
	return pid
}

// agentWindowName builds the agent window name in the format:
//
//	[B] project/name [model]
//
// tmux status bar already prepends the window index, so we don't add a session
// index prefix here (that would produce double numbers like "1:1:[I] name").
// Status [B]/[I] comes from the live Claude session.
// project/name respects the show_path config toggle.
// [model] is appended when show_model is on and a model is detected.
// Empty for non-agent windows. ci is reused across windows; pass nil for a one-shot.
// agentWindowName builds the tab name. acIdx is a shared process Index for this
// sync pass (nil → BuildIndex once for one-shot callers).
func agentWindowName(windowID, sessionID, aiPane string, ci *claudeIndex, acIdx *agentclient.Index) string {
	client := tmuxWindowOption(windowID, "@agent_client")
	model := tmuxWindowOption(windowID, "@agent_model")

	idx := ci
	if idx == nil {
		built := buildClaudeIndex()
		idx = &built
	}
	pane := panePID(aiPane)
	meta, claudePID, hasClaude := idx.sessionForPanePID(pane)
	codexMeta, _, hasCodex := codexThreadForPane(aiPane, idx)
	// Registry path (Grok + future). Claude/Codex keep legacy enrichment for
	// provider/limited/error parity until full cutover.
	if acIdx == nil {
		acIdx = agentclient.BuildIndex()
	}
	regLive, hasReg := agentclient.DefaultRegistry().DetectForPane(acIdx, pane, client)
	liveTitle := ""
	liveStatus := ""

	// Prefer registry for non-claude/codex clients (currently Grok). Avoid
	// client=="grok" string in assemble path — branch on "not covered by legacy".
	if hasReg && !hasClaude && !hasCodex {
		client = regLive.Client
		liveTitle = agentTitleForWindow(regLive.Title)
		liveStatus = regLive.Status
		if regLive.Status == agentclient.StatusUnknown {
			liveStatus = "" // do not show false [I]; skip finish path via empty/non-idle
		}
		if regLive.Model != "" {
			model = sanitizeWindowMarker(regLive.Model)
		}
		// Always refresh structural client from live Detect so a stale launcher
		// tag (e.g. codex) cannot outlive a Grok process and mislabel Window Nav.
		if client != "" && tmuxWindowOption(windowID, "@agent_client") != client {
			setWindowOption(windowID, "@agent_client", client)
		}
		if model != "" && tmuxWindowOption(windowID, "@agent_model") != model {
			setWindowOption(windowID, "@agent_model", model)
		}
	} else if hasClaude {
		client = "claude"
		// Live model from the JSONL tail (latest assistant turn) is authoritative:
		// it tracks in-session /model switches and provider switches that the
		// launch-time process args miss. Fall back to the process --model arg only
		// when no assistant message has been written yet.
		if m := liveModelFromSession(meta); m != "" {
			model = m
		} else if m := modelForPID(claudePID); m != "" {
			model = m
		}
		// Read full JSONL only for the title fallback (latest AI-generated title).
		var aiTitle string
		if meta.Name == "" {
			_, aiTitle = readSessionJSONL(meta)
		}
		// Detect the live provider from the Claude process env (ANTHROPIC_BASE_URL
		// → providers/*.env). This is the single authoritative source; the
		// @agent_provider option is only a cache written once at window creation
		// and goes stale (or is missing) when the provider is switched or the
		// window is rebuilt by workspace-restore. Persist it so Window Nav and
		// the status bar read the correct value.
		provider := providerForPID(claudePID, idx.providers)
		if provider != "" && tmuxWindowOption(windowID, "@agent_provider") != provider {
			setWindowOption(windowID, "@agent_provider", provider)
		}
		// Fallback: read ANTHROPIC_MODEL from provider env file (e.g. minimax).
		if model == "" {
			model = modelFromProviderEnv(provider)
		}
		// Persist raw model name so Window Nav and other consumers can read it.
		if model != "" && tmuxWindowOption(windowID, "@agent_model") != model {
			setWindowOption(windowID, "@agent_model", model)
		}
		if tmuxWindowOption(windowID, "@agent_client") == "" {
			setWindowOption(windowID, "@agent_client", "claude")
		}
		// Use AI-generated title as default name when user hasn't set one.
		if meta.Name == "" && aiTitle != "" {
			meta.Name = aiTitle
		}
		liveTitle = agentTitleForWindow(meta.Name)
		liveStatus = meta.Status
		// A session whose latest turn died on a usage-limit 429 is its own
		// "limited" status ([L]): the dialog blocks input and no timer/idle
		// semantics apply. The reset instant is stamped on the window so the
		// same sync pass (task reconcile) and other consumers reuse the probe.
		if !strings.EqualFold(liveStatus, "busy") {
			if resetAt, ok := claudeLimitResetFromSession(meta, time.Now()); ok {
				liveStatus = "limited"
				stamp := strconv.FormatInt(resetAt.Unix(), 10)
				if tmuxWindowOption(windowID, "@agent_limit_reset_at") != stamp {
					setWindowOption(windowID, "@agent_limit_reset_at", stamp)
				}
			} else if tmuxWindowOption(windowID, "@agent_limit_reset_at") != "" {
				_ = runTmux("set", "-wu", "-t", windowID, "@agent_limit_reset_at")
			}
		}
		// A turn that stopped on an API error (5xx/529 overloaded, auth, etc.) is
		// the "error" status ([E]) — mirroring Codex. Only checked when the session
		// isn't busy and didn't hit the 429 limit (which is its own [L]). We stamp
		// @agent_error_at/type for recoverable (5xx) errors so the same sync pass's
		// reconcileClaudeErrorRetry can drive a bounded auto-retry; non-recoverable
		// errors show [E] but carry no retry stamp.
		if !strings.EqualFold(liveStatus, "busy") && !strings.EqualFold(liveStatus, "limited") {
			if terr, ok := claudeErrorFromSession(meta); ok {
				liveStatus = "error"
				if terr.Retryable() {
					setWindowTimeOption(windowID, optErrorAt, terr.At)
					if terr.Type != "" && tmuxWindowOption(windowID, optErrorType) != terr.Type {
						setWindowOption(windowID, optErrorType, terr.Type)
					}
				} else {
					unsetWindowOption(windowID, optErrorAt)
					unsetWindowOption(windowID, optErrorType)
				}
			} else {
				unsetWindowOption(windowID, optErrorAt)
				unsetWindowOption(windowID, optErrorType)
			}
		} else {
			unsetWindowOption(windowID, optErrorAt)
			unsetWindowOption(windowID, optErrorType)
		}
	} else if hasCodex {
		client = "codex"
		if codexMeta.Model != "" {
			model = codexMeta.Model
		}
		liveTitle = codexMeta.Title
		liveStatus = codexMeta.Status
		if tmuxWindowOption(windowID, "@agent_client") == "" {
			setWindowOption(windowID, "@agent_client", "codex")
		}
		if model != "" && tmuxWindowOption(windowID, "@agent_model") != model {
			setWindowOption(windowID, "@agent_model", model)
		}
		// A codex thread whose latest rate_limits snapshot shows an exhausted
		// window is its own "limited" status ([L]), mirroring the Claude 429
		// probe above: the reset instant is stamped on the window so the same
		// sync pass (task reconcile) and other consumers reuse it.
		if !strings.EqualFold(liveStatus, "busy") {
			if resetAt, ok := codexLimitResetFromMeta(codexMeta, time.Now()); ok {
				liveStatus = "limited"
				stamp := strconv.FormatInt(resetAt.Unix(), 10)
				if tmuxWindowOption(windowID, "@agent_limit_reset_at") != stamp {
					setWindowOption(windowID, "@agent_limit_reset_at", stamp)
				}
			} else if tmuxWindowOption(windowID, "@agent_limit_reset_at") != "" {
				_ = runTmux("set", "-wu", "-t", windowID, "@agent_limit_reset_at")
			}
		}
	} else {
		// No live claude/codex/grok in this window.
		if client != "" {
			// Stale agent tags: either the agent exited or the launcher tagged the
			// window before its process came up. Drop the live-detected
			// provider/model so Window Nav shows no phantom provider (e.g. a
			// lingering "anthropic") for a window with no running agent. Keep
			// @agent_client as the window's structural identity; it is refilled
			// when a claude/codex/grok process appears.
			if tmuxWindowOption(windowID, "@agent_provider") != "" {
				_ = runTmux("set", "-wu", "-t", windowID, "@agent_provider")
			}
			if tmuxWindowOption(windowID, "@agent_model") != "" {
				_ = runTmux("set", "-wu", "-t", windowID, "@agent_model")
			}
		}
		// If it's running ssh, mark it "🌐 host", prefixed with the remote's
		// aggregate live status ([B]/[?]/[E]/…) when the daemon's remote-bell poller
		// has mirrored it into @agent_remote_status (any remote window busy → [B]).
		// autoRenameWindow keeps manual renames.
		if marker := sshWindowMarker(windowID); marker != "" {
			if rs := strings.TrimSpace(tmuxWindowOption(windowID, "@agent_remote_status")); rs != "" {
				return statusTag(rs) + marker
			}
			return marker
		}
		// A pending agent window — its 3-pane layout was built via `prefix ]` with a
		// typed title (persisted to @agent_title) but the agent process hasn't come
		// up yet, so @agent_client is still empty. Name it from @agent_title so the
		// typed title shows immediately instead of the bare shell name ("zsh"); once
		// claude/codex launches, @agent_client is set and the live title takes over.
		if client == "" && sanitizeWindowMarker(tmuxWindowOption(windowID, "@agent_title")) == "" {
			return ""
		}
	}

	cfg := loadAppConfig()

	// Use @agent_dir if available (set at window creation); fall back to live pane path.
	agentDir := tmuxWindowOption(windowID, "@agent_dir")
	var project string
	if agentDir != "" {
		project = filepath.Base(agentDir)
	} else {
		project = tmuxProjectName(aiPane)
	}
	sessionName := tmuxSessionName(sessionID)
	_, sessionLabel := splitSessionLabel(sessionName)

	// Name part priority: live session title > persisted @agent_title > session
	// label > project dir.
	sessionTitle := liveTitle
	if sessionTitle == "" {
		// @agent_title may be a user-typed title (prefix ] prompt). Strip C0/C1
		// control chars and '#' so a pasted title can't corrupt the status line —
		// same hygiene as the ssh host marker.
		sessionTitle = sanitizeWindowMarker(tmuxWindowOption(windowID, "@agent_title"))
	}

	// When enabled (default), strip a leading YYYY-MM-DD- from the title/label
	// segment so task dirs like "2026-07-09-open-source-refactor" render as
	// "open-source-refactor" in the tab, @agent_notify_name, and Window Nav Name.
	// (The project/dir segment already strips it via abbrevProject.)
	stripDate := stripDatePrefixSetting(cfg)
	titleSeg := truncateWindowTitle(maybeStripDatePrefix(sessionTitle, stripDate), windowTitleMaxRunes)
	labelSeg := truncateWindowTitle(maybeStripDatePrefix(sessionLabel, stripDate), windowTitleMaxRunes)

	// assemble builds "[status]project/name (model)", each part gated by a flag.
	// tmux already shows the window index before the name, so no idx prefix here.
	assemble := func(showStatus, showPath, showModel bool) string {
		var namePart string
		switch {
		case titleSeg != "":
			if showPath && project != "" {
				namePart = abbrevProject(project) + "/" + titleSeg
			} else {
				namePart = titleSeg
			}
		case labelSeg != "":
			if showPath && project != "" {
				namePart = abbrevProject(project) + "/" + labelSeg
			} else {
				namePart = labelSeg
			}
		default:
			namePart = abbrevProject(project)
		}
		if namePart == "" {
			return ""
		}
		name := namePart
		if showStatus {
			name = statusTag(liveStatus) + namePart
		}
		if showModel && strings.TrimSpace(model) != "" {
			name += " (" + normalizeModelNameLong(model) + ")"
		}
		return name
	}

	// Persist the notification title: always the full project/name (model) form
	// with no status prefix, independent of the window-tab display toggles, so
	// notifications stay self-descriptive even when the tab name is compact. The
	// daemon reads @agent_notify_name when building notification titles.
	if notify := assemble(false, true, true); notify != "" &&
		tmuxWindowOption(windowID, "@agent_notify_name") != notify {
		setWindowOption(windowID, "@agent_notify_name", notify)
	}

	// Stamp last-busy timestamp every tick the agent is actively working so
	// window nav can display "idle since" even after the panel is reopened.
	if s := strings.ToLower(strings.TrimSpace(liveStatus)); s == "busy" {
		setWindowOption(windowID, "@agent_last_busy_at",
			strconv.FormatInt(time.Now().Unix(), 10))
	}

	return assemble(windowNameShowStatus(cfg), windowNameShowPath(cfg), windowNameShowModel(cfg))
}

// autoRenameWindow applies an auto-computed name to windowID while respecting
// manual renames. We track the last auto-set name in @agent_window_name_auto:
//   - First call (option unset): always renames and records the name.
//   - Subsequent calls where current name == @agent_window_name_auto: renames on change.
//
// extractStatusPrefix returns the leading agent status prefix from name.
func extractStatusPrefix(name string) string {
	return statustag.Prefix(name)
}

// stripStatusPrefix removes a leading agent status prefix if present.
func stripStatusPrefix(name string) string {
	return statustag.Strip(name)
}

//   - Current name is empty/blank: user cleared it — resume auto-naming.
//   - Current name differs from @agent_window_name_auto (non-empty): user renamed it
//     manually — still update [B]/[I] status prefix, but keep user's base name.
//
// placeholderWindowName is the literal name new_agent_window.sh gives a
// freshly created window (tmux new-window -n "agent") before an agent
// process has started. A window still showing exactly this name was never
// actually renamed by anyone; treating it as a manual rename would freeze
// auto-naming on it forever the moment @agent_window_name_auto happens to
// hold a stale value (e.g. after workspace restore recreates the window, or
// a transient poll failure) — see the manual-override guard below.
const placeholderWindowName = "agent"

func autoRenameWindow(windowID, name string) {
	cur, _ := runTmuxOutput("display-message", "-p", "-t", windowID, "#{window_name}")
	cur = strings.TrimSpace(cur)
	lastAuto := strings.TrimSpace(tmuxWindowOption(windowID, "@agent_window_name_auto"))

	// Manual-override: user renamed the window — keep their base name but still
	// update the [B]/[I] status prefix so busy/idle is always current. The
	// placeholder exception keeps a window stuck on "agent" from being
	// mistaken for a deliberate rename (see placeholderWindowName above).
	if lastAuto != "" && cur != "" && cur != lastAuto && stripStatusPrefix(cur) != placeholderWindowName {
		// Clear call (name==""): the source that earned the auto-name is gone
		// (e.g. ssh exited) but the user has their own name. Keep their name, but
		// drop the tracking option so the poll stops re-entering this path.
		if name == "" {
			_ = runTmux("set-option", "-w", "-u", "-t", windowID, "@agent_window_name_auto")
			return
		}
		newStatus := extractStatusPrefix(name)
		userBase := stripStatusPrefix(cur)
		newName := newStatus + userBase
		if newName != cur {
			_ = runTmux("rename-window", "-t", windowID, newName)
		}
		return
	}

	// Clearing our auto-name (e.g. an ssh window after the session exited): tmux
	// leaves automatic-rename off once a window has been renamed, so we re-enable it
	// to hand the window back to tmux (which relabels from the pane command on the
	// next tick). We deliberately do NOT rename-window "" here — an explicit rename
	// turns automatic-rename back off. Drop the tracking option so we don't re-enter.
	if name == "" {
		_ = runTmux("set-option", "-w", "-t", windowID, "automatic-rename", "on")
		if lastAuto != "" {
			_ = runTmux("set-option", "-w", "-u", "-t", windowID, "@agent_window_name_auto")
		}
		return
	}

	if cur != name {
		_ = runTmux("rename-window", "-t", windowID, name)
	}
	if lastAuto != name {
		_ = runTmux("set-option", "-w", "-t", windowID, "@agent_window_name_auto", name)
	}
}

// sanitizeWindowMarker strips C0/C1 control characters (incl. DEL) plus the tmux
// format character '#' from s, so a malformed alias/hostname can't inject markup
// into the status line. Multi-byte printable runes (e.g. the 🌐 emoji) pass through.
// windowTitleMaxRunes bounds the title segment of an auto-computed window name.
// tmux expands the window name on every status-line redraw and every sync-names
// tick; an unbounded name makes each expansion allocate proportionally and tmux
// 3.6b does not free those, so a 6KB name grew the server ~6MB/min. Codex uses
// the entire prompt as its session title, so this is the normal case, not an edge.
const windowTitleMaxRunes = 100

// truncateWindowTitle caps s at max runes, marking a cut with an ellipsis that
// counts toward the cap. A non-positive max disables truncation.
func truncateWindowTitle(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func sanitizeWindowMarker(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == '#' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// agentTitleForWindow normalizes a raw session title (collapse whitespace) but
// does NOT truncate: the window name is the FULL title at the data layer, so
// both #{window_name} consumers (the tmux tab #W and the Window Nav Name column)
// see the complete name. Truncation is a display concern applied only at the
// status-bar format (window-status-format's width-limited #W).
func agentTitleForWindow(title string) string {
	// Collapse whitespace then strip control / '#' so model-generated titles
	// cannot corrupt tmux status formats (same hygiene as ssh markers).
	return sanitizeWindowMarker(strings.Join(strings.Fields(strings.TrimSpace(title)), " "))
}

// modelForPID reads the raw model name from the Claude process args (--model <value>).
func modelForPID(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := runCommandOutput(3*time.Second, "ps", "-p", strconv.Itoa(pid), "-o", "args=")
	if err != nil {
		return ""
	}
	args := strings.Fields(string(out))
	for i, arg := range args {
		if arg == "--model" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, "--model=") {
			return strings.TrimPrefix(arg, "--model=")
		}
	}
	return ""
}

// modelFromSessionJSONL is a convenience wrapper used for model-only lookups.
func modelFromSessionJSONL(pid int) string {
	sessionFile := filepath.Join(claudeSessionsDir(), fmt.Sprintf("%d.json", pid))
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return ""
	}
	var meta claudeSessionMeta
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	model, _ := readSessionJSONL(meta)
	return model
}

// modelFromProviderEnv reads ANTHROPIC_MODEL from a provider .env file.
// Used for providers (e.g. minimax) that set the model via env var, not --model flag.
func modelFromProviderEnv(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(homeDir(), providersRelDir, provider+".env"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"export ANTHROPIC_MODEL=", "ANTHROPIC_MODEL="} {
			if after, ok := strings.CutPrefix(line, prefix); ok {
				return strings.Trim(strings.TrimSpace(after), "\"'")
			}
		}
	}
	return ""
}

// normalizeModelName maps raw model IDs to short family names for the status bar.
// e.g. "claude-sonnet-4-6" → "sonnet", "MiniMax-M3" → "MiniMax-M3"
func normalizeModelName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	lower := strings.ToLower(model)
	for _, family := range []string{"opus", "sonnet", "haiku", "fable"} {
		if strings.Contains(lower, family) {
			return family
		}
	}
	r := []rune(model)
	if len(r) > 12 {
		return string(r[:12]) + "…"
	}
	return model
}

// normalizeModelNameLong maps raw model IDs to a longer form for Window Nav.
// e.g. "claude-sonnet-4-6" → "sonnet4.6", "sonnet" → "sonnet4.6", "MiniMax-M3" → "MiniMax-M3"
func normalizeModelNameLong(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	// Short-name aliases: map to current versioned equivalents.
	shortNames := map[string]string{
		"opus":   "opus4.8",
		"sonnet": "sonnet4.6",
		"haiku":  "haiku4.5",
		"fable":  "fable5",
	}
	if v, ok := shortNames[strings.ToLower(model)]; ok {
		return v
	}
	s := model
	if lower := strings.ToLower(model); strings.HasPrefix(lower, "claude-") {
		s = model[7:]
	}
	parts := strings.Split(s, "-")
	for i, p := range parts {
		lp := strings.ToLower(p)
		for _, f := range []string{"opus", "sonnet", "haiku", "fable"} {
			if lp == f {
				var ver []string
				for _, vp := range parts[i+1:] {
					if _, err := strconv.Atoi(vp); err != nil {
						break
					}
					ver = append(ver, vp)
				}
				if len(ver) > 0 {
					return lp + strings.Join(ver, ".")
				}
				return lp
			}
		}
	}
	// Non-Claude model (minimax, etc.): return as-is, capped at 16 chars.
	r := []rune(model)
	if len(r) > 16 {
		return string(r[:16]) + "…"
	}
	return model
}

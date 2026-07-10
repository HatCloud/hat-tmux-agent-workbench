# Hat Config — tmux + AI agent terminal workbench

**English** · [简体中文](./README.zh-CN.md)

> **Note:** This is my personal configuration repo, so it will likely get
> frequent, personally-driven updates. I may not be able to respond to issues
> promptly — or at all. The recommended way to use it is to fork it and make your
> own customizations; the project is MIT-licensed, so you're free to modify it
> however you like.

A personal tmux workflow for driving AI coding agents (Claude Code, Codex, and
custom providers) from the terminal. It gives each agent a three-pane window
(agent / git / run), auto-names and status-tracks those windows from a small Go
daemon, surfaces ⏳/🔔 task state and desktop notifications in the tmux status
bar, and can snapshot/restore whole workspaces after a crash.

Everything is deployed into a live tmux environment through `scripts/deploy.sh`
(or the higher-level `scripts/setup` wizard), and can be cleanly uninstalled.

![Hat Config workbench — agent command palette, git pane, and tmux status bar](./assets/screenshot.png)

## Requirements

macOS only. The daemon uses launchd, Carbon (input-source switching), and
`lsappinfo`, so Linux/Windows are out of scope.

The install location is hardcoded to `~/.hat-config` (see
`agent-tracker/internal/paths/paths.go`, which resolves the binary, socket, and
runtime state under that directory). Clone the repository there:

```bash
git clone <repo-url> ~/.hat-config
```

Dependencies (detected by `scripts/setup`):

| Dependency | Kind | Min version | Used for |
|---|---|---|---|
| `tmux` | required | 3.3 | status bar + agent window layout |
| `go` | required | — | building the agent-tracker daemon |
| `fzf` | required | — | fuzzy pickers (`prefix [`, `tmux-resume`) |
| `jq` | required | — | JSON config merge + `--json` output |
| `z` | optional | — | directory-history jump (`prefix [`) |
| `lazygit` | optional | — | git TUI pane |
| `terminal-notifier` | optional | — | desktop notifications (bell-only if absent) |
| `gh` | optional | — | GitHub CLI helpers |

Missing optional dependencies only degrade the corresponding feature; missing
required ones abort the install.

## Installation footprint

Deploying touches six places outside the repo. Each is an independent, opt-out
step (see the `--skip-*` flags below); uninstall reverses every one of them.

1. **Managed tmux block** — a `# >>> hat-config managed tmux … <<<` block
   appended to `~/.tmux.conf` that `source-file`s this repo's `tmux/tmux.conf`.
2. **launchd daemon** — `app.hat-tmux-workbench.agent-tracker` LaunchAgent that
   runs the tracker daemon (window naming, task state, notifications).
3. **launchd workspace timer** — `app.hat-tmux-workbench.workspace-save`
   LaunchAgent that auto-snapshots workspaces every 180s.
4. **Claude Stop hook** — a `Stop` hook merged into `~/.claude/settings.json`
   (captures the Claude session id for workspace restore).
5. **Claude statusLine** — a `statusLine` registration in
   `~/.claude/settings.json` pointing at this repo's `claude_statusline.sh`.
6. **Shell alias** — `agent` / `tmux-resume` aliases added to
   `~/.hat-env/shared/alias-common` (or, absent that, a managed block in
   `~/.zshrc`).

## Quick start

Run the setup wizard — it checks dependencies, picks an icon set and keymap
preset, discloses the six intrusion points, and hands off to `deploy.sh`:

```bash
~/.hat-config/scripts/setup
```

It works non-interactively too (CI-safe; every intrusive step defaults to skip
unless you opt in). See the full flag list with:

```bash
~/.hat-config/scripts/setup --help
```

## Manual install

`deploy.sh` is usable directly, without the wizard:

```bash
~/.hat-config/scripts/deploy.sh install --yes   # install or update (same path)
~/.hat-config/scripts/deploy.sh status          # report deployment state
```

Each of the six footprint steps can be skipped independently. The flags apply
symmetrically to install and uninstall:

```bash
--skip-tmux        # managed tmux block
--skip-daemon      # agent-tracker launchd daemon
--skip-ws-timer    # workspace auto-save launchd timer
--skip-stop-hook   # Claude Stop hook
--skip-statusline  # Claude statusLine registration
--skip-alias       # agent / tmux-resume shell aliases
```

### AI deployment

To have an agent deploy this for you, point it at the machine-readable contract
first. One-line instruction template:

> Run `~/.hat-config/scripts/setup agent-guide` to read the deployment contract
> (flags, decision points, JSONL output schema), then run
> `~/.hat-config/scripts/setup --non-interactive --json` with explicit
> `--<step>=install` flags for the intrusion points you want, and report the
> resulting `{"result": …}` line.

`agent-guide` emits a static JSON contract and performs no intrusive action.

## Private overlay

Machine-local and personal files live in a gitignored overlay so the public repo
stays clean. This includes `private/` (e.g. `private/keymap.conf` written by the
setup keymap module, plus personal docs), the repo-root `CLAUDE.md`, `.tasks/`,
`snippets/private/` with `snippets/.favorites`, and
`agent-tracker/agent-config.json`. These are never committed; the setup wizard
and daemon read them in place.

> **WARNING:** `git clean -fdx` deletes gitignored files — it will wipe your
> entire private overlay (keymap, personal snippets, CLAUDE.md, tasks). Back the
> overlay up (e.g. Syncthing versioning or a personal backup) before running any
> destructive clean, and prefer `git clean -fd` (without `-x`) if you only mean
> to drop untracked *tracked-candidate* files.

## Uninstall

```bash
~/.hat-config/scripts/deploy.sh uninstall --yes --keep-state
```

This reverses all six footprint steps (removes the managed tmux block, boots out
both launchd jobs, strips the Claude Stop hook and statusLine, and removes the
shell aliases). `--keep-state` preserves runtime state under
`~/.hat-config/state/`; use `--remove-state` to delete it.

## Documentation

- [docs/GUIDE.md](./docs/GUIDE.md) — full workflow guide (incl. nested SSH tmux).
- [docs/GUIDE_TMUX.md](./docs/GUIDE_TMUX.md) — tmux basics, keymap, and the
  keymap.conf customization presets.
- [docs/GUIDE_TIMER_SNIPPET.md](./docs/GUIDE_TIMER_SNIPPET.md) — timer panel +
  snippet library.
- [docs/GUIDE_WORKSPACE.md](./docs/GUIDE_WORKSPACE.md) — workspace snapshot /
  restore.
- [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) — daemon + tmux architecture
  (maintainers).

Runtime state is written to `~/.hat-config/state/` and is not committed to git.

# Hat Config — tmux + AI agent terminal workbench

A personal tmux workflow for driving AI coding agents (Claude Code, Codex, and
custom providers) from the terminal. It gives each agent a three-pane window
(agent / git / run), auto-names and status-tracks those windows from a small Go
daemon, surfaces ⏳/🔔 task state and desktop notifications in the tmux status
bar, and can snapshot/restore whole workspaces after a crash.

Everything is deployed into a live tmux environment through `scripts/deploy.sh`
(or the higher-level `scripts/setup` wizard), and can be cleanly uninstalled.

### 中文对照

面向终端的 tmux + AI agent 工作流：为每个 agent（Claude Code / Codex / 自定义
provider）开三 pane 窗口（agent / git / run），由一个小型 Go daemon 自动命名和
状态追踪，在 tmux 状态栏显示 ⏳/🔔 任务状态并发系统通知，还能在崩溃后快照 / 恢复
整个 workspace。全部经 `scripts/deploy.sh`（或上层 `scripts/setup` 向导）接入真实
tmux 环境，也可干净卸载。

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

### 中文对照

仅支持 macOS（daemon 依赖 launchd、Carbon 输入源切换、`lsappinfo`）。安装路径硬编码
为 `~/.hat-config`（见 `agent-tracker/internal/paths/paths.go`——二进制、socket、运行时
状态都落在该目录下），请把仓库 clone 到那里。必需依赖 `tmux`(≥3.3)/`go`/`fzf`/`jq`，
可选依赖 `z`/`lazygit`/`terminal-notifier`/`gh`；可选依赖缺失只降级对应能力，必需依赖
缺失则中止安装。

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

### 中文对照

部署会改动仓库之外的六处，每处都是独立、可跳过的步骤（对应下方 `--skip-*` 开关），
卸载会逐一还原：① `~/.tmux.conf` 的 managed block；② launchd daemon
`app.hat-tmux-workbench.agent-tracker`；③ launchd workspace 自动存档定时器
`app.hat-tmux-workbench.workspace-save`（每 180s）；④ `~/.claude/settings.json` 的
Claude Stop hook；⑤ `~/.claude/settings.json` 的 Claude statusLine 注册；⑥ shell alias
`agent` / `tmux-resume`。

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

### 中文对照

运行 `scripts/setup` 向导：检查依赖、选图标集与键位预设、披露六处侵入点，再交给
`deploy.sh` 执行。也支持非交互模式（CI 安全，非交互下侵入步骤默认全跳过、需显式
opt-in）；`scripts/setup --help` 查看完整 flag。

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

### 中文对照

`deploy.sh` 可脱离向导直接用：`install --yes`（安装/更新同一路径）、`status`（查看
部署状态）。六个步骤各自可用 `--skip-*` 独立跳过，install/uninstall 对称。**AI 部署**：
先让 agent 跑 `scripts/setup agent-guide` 读机读契约（flags / 决策点 / JSONL schema），
再以显式 `--<step>=install` 跑 `scripts/setup --non-interactive --json` 并回报
`{"result": …}`；`agent-guide` 只输出静态 JSON、不执行任何侵入动作。

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

### 中文对照

机器本地与个人文件放在 gitignore 的 overlay 里，保持公开仓干净：`private/`（含 setup
键位模块产出的 `private/keymap.conf` 与个人文档）、仓根 `CLAUDE.md`、`.tasks/`、
`snippets/private/` 与 `snippets/.favorites`、`agent-tracker/agent-config.json`。这些
永不提交，由向导和 daemon 就地读取。**警告**：`git clean -fdx` 会删除 gitignore 文件，
即会抹掉整个私有 overlay；执行任何破坏性 clean 前先备份（Syncthing versioning 或个人
备份），只想清理未跟踪文件时优先用不带 `-x` 的 `git clean -fd`。

## Uninstall

```bash
~/.hat-config/scripts/deploy.sh uninstall --yes --keep-state
```

This reverses all six footprint steps (removes the managed tmux block, boots out
both launchd jobs, strips the Claude Stop hook and statusLine, and removes the
shell aliases). `--keep-state` preserves runtime state under
`~/.hat-config/state/`; use `--remove-state` to delete it.

### 中文对照

`deploy.sh uninstall` 反向还原全部六处（移除 managed block、bootout 两个 launchd job、
剥离 Claude Stop hook 与 statusLine、删除 shell alias）。`--keep-state` 保留
`~/.hat-config/state/` 运行时状态，`--remove-state` 则删除。

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

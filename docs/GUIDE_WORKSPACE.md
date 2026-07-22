# Workspace 存档 / 恢复使用指南

tmux server 偶尔会整体崩溃（终端出现 `[server exited]`，再 `tmux a` 得到 `no sessions`），所有 session/window 一次性消失。这套机制用来在崩溃后快速把工作区结构重建回来。

tmux 基础与全部快捷键速查见 [GUIDE_TMUX.md](./GUIDE_TMUX.md)；底层实现/数据流见 [ARCHITECTURE.md](./ARCHITECTURE.md)。

## 1. 它能做什么、不能做什么

这是「骨架快照」方案，刻意只记录结构、不记录运行内容，换取简单可靠：

**会保存**：
- 哪些 session、各自有哪些 window、window 的编号顺序与名字
- 每个 window 对应的 git repo 根目录
- 主布局朝向（landscape / portrait）
- 若该窗口跑的是 **Claude Code**，还会存它的 session id（用于恢复时一键续接）

**不会保存**：
- pane 里运行的进程（包括 Claude agent 本身）
- 滚动历史、未保存的编辑、shell 状态

**过滤规则**：只存「多 pane 且首个 pane 在 git 仓库内」的 window。单 pane 窗口、非 git 目录的窗口会被跳过（它们多是临时窗口）。

**恢复时**：按快照重开 session/window、摆正编号，并按当前 Window & Resize 设置重建 ai/git[/run]、在 git pane 起 lazygit。**不会自动启动 agent**。但如果该窗口存到了 Claude Code 的 session id，恢复时会在 ai pane **预填**好命令：

```
claude --resume <session-id>
```

**只预填、不回车**——你回到窗口确认无误后自己按 Enter，就能续上崩溃前的那段对话。没存到 session id 的窗口，ai 格是空 shell（手动 `agent -r` 或 `claude` 即可）。

> session id 怎么来的：Claude Code 每次 Stop 会经 hook（`claude_report.sh`）把当前 session id 按所在 pane 落盘到 `state/workspaces/claude-sessions/`，存档时按 ai pane 读出。所以**该窗口至少触发过一次 Claude 的停止**才会有 id；全新刚开、一次都没停过的会话可能还没记录到。

## 2. 快捷键与命令

| 操作 | 触发 | 场景 |
|---|---|---|
| 存档当前工作区 | `prefix s` | 随手存一份 |
| 恢复最近一次快照 | `prefix r` | tmux 还活着、且是干净的 1 窗口环境 |
| 选历史快照恢复 | `prefix g` | tmux 内，fzf 弹窗挑一个旧快照 |
| 终端原生恢复 | `tmux-resume` | **崩溃后裸终端**的主入口 |
| 强制重建恢复 | `tmux-resume -f` | 先 kill-server 再按快照干净重建（kill 前比对差异并询问） |

> 快照菜单（`prefix g` 与 `tmux-resume` 共用）已禁用文字搜索、改用 **j/k 上下导航**（回车恢复 / Esc 取消），并带预览：选中即看该快照有哪些窗口、对应 repo 与朝向，`↻` 标记的窗口存有 Claude session、恢复时会预填 `--resume`。

> `prefix` 默认是 `Ctrl-s` 或 `Ctrl-b`。所以"存档"是顺手的 `Ctrl-s` 然后 `s`。

后台还有一个 launchd 定时器，每 180 秒自动存一次（内容无变化则跳过，最多保留最新 3 个快照），所以即使你忘了手动存，崩溃后通常也有一份近期快照可用。

> 注意「内容无变化则跳过」：如果工作区结构（session/window/repo/朝向）一段时间没动，最新快照的时间戳会停留在上次结构变化时——这是预期行为，不是定时器没跑。结构没变时旧快照本就等价于当前状态。

## 3. 存档

随时按 `prefix s`，底部状态栏报告结果：

```
Saved 4 workspace windows to 20260619-184532.tsv (1 single-pane, 2 non-git skipped)
```

快照是纯文本 TSV，人可读、可手改：

```bash
cat "$(cat ~/.hat-config/state/workspaces/last)"
# 1-CONFIG⇥1⇥Adjustment⇥~/.hat-config⇥landscape
# 每行 5 列：session ⇥ 窗口号 ⇥ 名字 ⇥ repo路径 ⇥ 朝向
```

- 快照目录：`~/.hat-config/state/workspaces/snapshots/<时间戳>.tsv`
- `~/.hat-config/state/workspaces/last`：指向最近一次快照的指针
- 整个 `state/` 已 gitignore，不入库

## 4. 恢复

### 4.1 崩溃后（裸终端）— 用 `tmux-resume`

server 崩了你回到普通 shell，这条是主路径：

```bash
tmux-resume
```

fzf 在终端里列出历史快照（文件名 + 窗口数）→ 选一个 → 自动建一个干净 session → 重建所有窗口 → attach 进去。

> 如果提示 `command not found`：alias 由 deploy 写进 `~/.hat-env/shared/alias-common`，当前 shell 还没加载。`source ~/.hat-env/shared/alias-common` 或新开一个终端标签即可。

### 4.2 tmux 内 — `prefix r` / `prefix g`

- `prefix r`：直接恢复 `last` 指向的最近快照。
- `prefix g`：fzf 弹窗挑一个历史快照恢复。

### 4.3 恢复的前置守卫（重要）

恢复**只在"恰好 1 session / 1 window / 1 pane"的干净环境执行**。这是有意设计：避免把快照内容灌进你正在用的、已经有一堆窗口的会话里造成污染。

所以：
- 在多窗口的现有会话里按 `prefix r`，会看到 `Restore requires exactly 1 session, 1 window, and 1 pane` 然后拒绝执行——这是正常的。
- 正确用法是崩溃后的全新 tmux，或新开一个干净 server。`tmux-resume` 会自己建干净 session，所以它不受这个限制困扰。

**强制重建** `tmux-resume -f`：想把整个 tmux 推倒按快照重来时用。它先 `kill-server` 再回到干净路径重建，所以：

- **必须在 tmux 外的终端运行**（或先 `prefix d` 脱离）——kill-server 会连脚本所在 pane 一起杀掉。在 tmux 内带 `-f` 会被直接拦下并提示。
- kill 前若已有 server，会比对**当前 live 结构**与**所选快照**（按 session+窗口号+名字+repo，忽略状态前缀/朝向/session id），不一致时列出「仅在当前（kill 后会丢失）」和「仅在快照（将重建）」两组差异，再询问 `[y/N]`；输入 `y` 才 kill-server 并重建。
- 一致时不打扰，直接重建。

## 5. 如何测试

按风险从低到高，建议依次做前三个，愿意承受一次断连时再做第 5 个。

**① 手动存档（零风险）**
```
prefix s              # 看状态栏报告
```
```bash
cat "$(cat ~/.hat-config/state/workspaces/last)"   # 核对 5 列内容
```

**② 自动存档定时器（零风险）**
```bash
~/.hat-config/scripts/deploy.sh status | grep Timer          # 应 loaded
launchctl kickstart -k gui/$(id -u)/app.hat-tmux-workbench.workspace-save
sleep 1; cat ~/.hat-config/state/workspaces/workspace-save.err.log   # 应为空
```

**③ 选历史快照（零风险）**
```
prefix g              # fzf 列出快照；多窗口环境下按 Esc 退出别真恢复
```

**④ 隔离 server 恢复（不影响当前 tmux）**

用独立 socket 起一个干净 server 跑恢复，全程不碰你现有会话：
```bash
d=$(mktemp -d /tmp/hat-restore.XXXXXX)
env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$d" tmux new-session -d -s clean -x 200 -y 50
env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$d" ~/.hat-config/tmux/scripts/restore_workspace.sh "$(cat ~/.hat-config/state/workspaces/last)"
socket="$d/tmux-$(id -u)/default"
tmux -S "$socket" list-windows -a -F '#{window_index} #{window_name} panes=#{window_panes}'
tmux -S "$socket" kill-server
rm -rf "$d"
```
应看到窗口按快照重建、每个 2 格。

这里必须同时清掉继承的 `TMUX`/`TMUX_PANE`，并在清理时显式指定临时 socket；只设置 `TMUX_TMPDIR` 仍可能连接到当前真实 server。

**⑤ 真实崩溃演练（会断开当前 tmux）**

⚠️ 先确认每个 agent 窗口对话都能 `--resume` 再做。
```
prefix s              # 先存一份
```
```bash
tmux kill-server      # 模拟崩溃，回到裸终端
tmux-resume           # fzf 选最近快照 → 重建 → attach
```
验收点：窗口数量/顺序/名字与崩溃前一致；repo 窗口按当前 2/3-pane 默认重建且 lazygit 起来；ai pane 空 shell，`agent -r` 续对话。

## 6. 故障排查

**`prefix s` 没反应 / 没生成快照**
- 当前没有"多 pane 且在 git repo"的窗口时，会显示 `No qualifying windows to save` 并不写文件——这是正常的，单 pane / 非 git 窗口本就不存。
- 确认部署生效：`~/.hat-config/scripts/deploy.sh status`。

**自动存档的快照总是 0 窗口 / err 日志有内容**
- 检查 `~/.hat-config/state/workspaces/workspace-save.err.log`。
- launchd 环境与交互 shell 不同（PATH、tmux 输出处理都有坑），相关机制与规避见 [ARCHITECTURE.md](./ARCHITECTURE.md) 的「launchd 下运行 tmux 的两个坑」。重新 `deploy.sh install` 会用当前环境重新生成定时器配置。

**`prefix r` 总是拒绝**
- 见 4.3：恢复要求干净 tmux。多窗口会话里它本就该拒绝；用 `tmux-resume` 或干净 server。

**`tmux-resume: command not found`**
- `source ~/.hat-env/shared/alias-common` 或新开终端标签。

**恢复后窗口名字带了旧的 `[B]`/`[I]` 前缀**
- 不会——存档时已剥掉瞬时状态前缀，存的是干净基础名。若仍看到，多半是该窗口被手动命名过。

## 7. 涉及的文件

| 文件 | 职责 |
|---|---|
| `tmux/scripts/save_workspace.sh` | 存档（`--auto` 供定时器用：静默 + 去重 + 保留最新 3） |
| `tmux/scripts/restore_workspace.sh` | 恢复（干净环境守卫 + 重建 + 杀 scratch） |
| `tmux/scripts/build_agent_layout.sh` | 可配置 ai/git[/run] 布局重建（新建与恢复共用） |
| `tmux/scripts/choose_workspace.sh` | tmux 内 fzf 选历史快照（`prefix g`） |
| `tmux/scripts/tmux_resume.sh` | 终端原生恢复入口（`tmux-resume`） |
| `tmux/scripts/workspace_snapshot_menu.sh` | 快照选择菜单（fzf + manifest 预览，上两者共用） |
| `agent-tracker/app.hat-tmux-workbench.workspace-save.plist.tmpl` | launchd 定时器模板（每 180s 自动存档） |
| `tmux/scripts/claude_report.sh` | Claude Stop hook：finish_task 上报 + 顺带把 session id 按 pane 落盘，供存档/恢复 `--resume` 预填 |

# Hat Config 工作流使用指南

这份文档说明当前 `~/.hat-config` 里的 tmux + AI session 工作流如何使用。

tmux 基础概念、session/window/pane 管理和快捷键速查见 [GUIDE_TMUX.md](./GUIDE_TMUX.md)。

当前这套工作流的目标不是一次性替代所有终端习惯，而是先提供几个稳定能力：

- 在 tmux 中按项目组织 AI session。
- 快速为一个项目创建三 pane 的 agent 工作窗口，朝向（横/竖）按窗口尺寸自动决定并随 resize 自动切换。
- 手动标记 AI session 的运行状态、完成状态和已确认状态。
- 从 tmux 当前项目快速打开 VS Code。
- 通过部署脚本把本仓库配置接入真实 tmux 环境，并支持卸载。

## 1. 安装、更新和卸载

推荐入口是 `scripts/setup` 向导：它检查依赖、选图标集与键位预设、逐项披露六处侵入点
（managed tmux block / daemon / workspace timer / Claude Stop hook / statusLine / shell alias），
再把决策交给底层 `deploy.sh` 执行。

```bash
~/.hat-config/scripts/setup            # 交互向导（首选）
~/.hat-config/scripts/setup --help     # 全部 flag
~/.hat-config/scripts/setup agent-guide  # agent 用的机读部署契约
```

`scripts/setup` 也支持非交互 / CI（`--non-interactive --json`，侵入步骤默认全跳过、需
显式 `--<项>=install` opt-in）。若不需要向导的决策收集，也可直接用底层部署脚本：

```bash
~/.hat-config/scripts/deploy.sh
```

直接执行时会进入交互模式：

```text
1) Install / update
2) Uninstall
3) Status
4) Quit
```

也可以用参数直接执行，方便被其他脚本、alias 或 agent 调用：

```bash
~/.hat-config/scripts/deploy.sh install --yes
~/.hat-config/scripts/deploy.sh update --yes
~/.hat-config/scripts/deploy.sh uninstall --yes
~/.hat-config/scripts/deploy.sh status
```

`install` 和 `update` 是同一套逻辑：每次执行都会重新部署并覆盖之前由本项目管理的 tmux 设置。

部署脚本会在 `~/.tmux.conf` 中写入一个 managed block：

```tmux
# >>> hat-config managed tmux
source-file ~/.hat-config/tmux/tmux.conf
# <<< hat-config managed tmux
```

卸载时会删除这段 managed block，并尝试从当前运行中的 tmux server 里移除本项目注册的快捷键。

### 常用部署命令

查看是否已经部署：

```bash
~/.hat-config/scripts/deploy.sh status
```

安装或更新：

```bash
~/.hat-config/scripts/deploy.sh install --yes
```

卸载，但保留 tracker 状态：

```bash
~/.hat-config/scripts/deploy.sh uninstall --yes --keep-state
```

卸载，并删除本地 tracker 状态：

```bash
~/.hat-config/scripts/deploy.sh uninstall --yes --remove-state
```

测试部署到临时 tmux 配置文件：

```bash
tmp_conf="$(mktemp)"
~/.hat-config/scripts/deploy.sh install --yes --no-reload --tmux-conf "$tmp_conf"
cat "$tmp_conf"
rm -f "$tmp_conf"
```

## 1.5 agent 启动器与自动状态提醒

部署后新增两项核心能力，替代过去「手敲 tmux + 手输窗口名 + 手动 `agent_tracker.py`」的流程。

### agent 启动器

任意目录直接运行 `agent`：

```bash
agent
```

- 弹出 fzf 选客户端：`Claude Code · 默认` / 各 provider（minimax/kimi/deepseek/openrouter/temp/official）/ `Codex` / `Grok`（PATH 上有 `grok` 时）。**直接回车 = 默认 Claude**；Esc 取消。
- **恢复会话**：`agent -r`（或 `--resume`）进入客户端的交互恢复菜单；`agent -r <name|id>` 直接恢复指定会话——参数透传给选中客户端（claude/grok `--resume`、codex `resume`）。
- 不在 tmux 内 → 新建 session 并 attach；已在 tmux 内 → 当前 session 开新 window。
- 自动搭三 pane（ai/git/run）布局，在 ai pane 起选定客户端（如 `claude-minimax`），并把所选 client/provider 写入 window 变量 `@agent_client`/`@agent_provider`。
- 不再需要进窗口时输名——窗口名由 agent-tracker 在聚焦时自动命名。

### 自动命名模板

窗口名自动按模板 `rename-window`：

| 情况 | 窗口名 | 例 |
| --- | --- | --- |
| 无会话标题，仅编号 | `项目缩写·客户端#编号` | `hat-config·CL#1` |
| 带 provider | `项目缩写·客户端-P#编号` | `hat-config·CL-M#1` |
| **AI 会话有标题** | `项目缩写·客户端-P·会话标题` | `hat-config·CL-M·重构登录` |

缩写规则：客户端 `claude→CL`/`codex→CO`；provider 取首字母大写（`minimax→M`）；项目名去掉开头 `YYYY-MM-DD-` 日期前缀、超过 15 字符截断加 `…`（`2026-06-18-plugin-popup-reminders → plugin-popup-re…`）。状态栏 tab 前缀 `#I:` 是 tmux 窗口编号，可据此跳转。

命名锚点是**当前窗口主 AI pane 那个 agent 会话的标题**。Claude 读 `~/.claude/sessions/<pid>.json` 的 `name`，Codex 读 `~/.codex/state_5.sqlite` 中最新 CLI thread 的 `title`，Grok 读 `~/.grok` 的 `summary.json`（经 `internal/agentclient`）。自动获取到的标题会按显示宽度截断到约 10 个汉字 / 20 个英文字母，避免 tab 和 Window Nav 过长；在 Claude 里给会话改名后，底部窗口名**自动同步**：
- **切窗 / 切 session / 聚焦**时即时重算（`after-select-window`/`client-session-changed`/`pane-focus-in` hook 触发全量 `sync-names`，含 client/provider 变化）。
- **停留不动时**状态栏每秒发轻量触发，完整 `sync-names` 内部限流为最多每 5 秒一次；切窗/聚焦仍即时触发。重叠触发会自动合并，始终只运行一个 worker。

**不经 `agent` 启动器**进入的窗口也能命名：只要主 pane 跑着 Claude/Codex 会话，client 自动推断。没有 AI 会话的普通 shell 窗口不会被改名。未命名会话回退到 `项目名·客户端#编号`。

项目名取 git 仓库根目录名（无 git 则取 pane 当前路径名）；客户端/provider 来自启动器写的 `@agent_client`/`@agent_provider`。

### 自动状态提醒（取代手动 tracker）

agent-tracker 作为 launchd 常驻 daemon（`scripts/deploy.sh install` 自动装），**状态由最多每 5 秒一次的周期轮询（切窗/聚焦时即时触发）读取 `~/.claude/sessions/<pid>.json` 的 `status` 驱动**，无需 hook、无需手动调 `agent_tracker.py`：

- AI 在干活（busy）→ 窗口名加 **`[B]`** 实时前缀。
- AI 空闲（idle）→ 窗口名加 **`[I]`** 前缀；若是「干完一轮」（busy→idle）→ 窗口名后缀 **🔔**（完成待查看）。
- Codex 最终执行错误（capacity、529、请求/网络错误等）→ 窗口名加醒目的 **`[E]`** 前缀，Window Nav 显示红色 `E`，无 busy 动画并抬起 🔔；同 turn 自动恢复或用户重试产生新模型活动后自动回到 `[B]`。
- 切回/聚焦该 window → 自动标记已读，🔔 消失。

`[B]`/`[I]`/`[?]`/`[L]`/`[E]` 是实时状态；🔔 是完成或 attention 的未读提醒。聚焦只清 🔔，不会清仍未恢复的 `[E]`。

> 早期版本曾设计经 MCP + Stop/Notification hook 上报，现已改为 sessions-json 轮询（更可靠、不依赖 `$TMUX_PANE` 与 AI 自觉）。Notification hook 已移除；MCP 与 Stop hook 仍冗余保留（Stop 兼抓 session id）。

## 2. 推荐的 tmux 使用模型

当前推荐模型：

- 一个项目对应一个 tmux session。
- 一个 AI session 对应一个 tmux window。
- 每个 AI window 里使用三个 pane：
- `agent`：主 AI agent 终端。
- `git`：git/status/scratch 或提交相关终端，当前会优先打开 `lazygit`。
- `run`：测试、dev server、watch 命令终端。

这样组织的好处是：

- 每个项目的上下文固定在一个 tmux session 中。
- 每个 agent 的工作环境固定在一个 window 中。
- git 状态、运行命令和 AI 对话不会互相覆盖。
- 后续 tracker 可以用 `session_id`、`window_id`、`pane_id` 稳定定位不同 AI session。

## 3. tmux 快捷键

以下快捷键来自本项目的 `tmux/tmux.conf`。

### tmux prefix: `Ctrl-s` 或 `Ctrl-b`

本项目额外启用了 `Ctrl-s` 作为第二个 tmux prefix，同时保留 tmux 默认的 `Ctrl-b`。

也就是说，下面两种操作等价：

```text
Ctrl-s
Shift-a
```

```text
Ctrl-b
Shift-a
```

如果 `Ctrl-b` 对手的位置太远，优先使用 `Ctrl-s`。

注意：`Ctrl-s` 在一些终端里是 XOFF 暂停输出键。如果按下后终端像卡住了一样，先按 `Ctrl-q` 恢复。若 `Ctrl-s` 没有进入 tmux，需要在 shell 中关闭 flow control：

```bash
stty -ixon
```

启用它作为 tmux prefix 后，在 tmux 内直接按 `Ctrl-s` 会先被 tmux 捕获。如果你需要把真实的 prefix 发送给 shell，可以连续按两次：

```text
Ctrl-s
Ctrl-s
```

按下 prefix 后，tmux 状态栏左侧会显示高亮的 `PREFIX`，这可以用来确认 prefix 是否已经被 tmux 接收。active pane 顶部标题也会同步显示 `[PREFIX]`。本机的 Hammerspoon 配置还会在屏幕水平居中、距离顶部约 1/3 的位置弹出一个短暂的 `PREFIX` 提示，竖屏时不用把视线移到左下角。

中文输入法注意事项：本机通过 Hammerspoon 做了一层输入法保护。当前台 App 是 iTerm2、Ghostty 或 Terminal 时，按下 `Ctrl-s` 或 `Ctrl-b` 会先切到 ABC 输入源，再把 prefix 发送给 tmux。

本机还通过 Hammerspoon 绑定了全局快捷键：

```text
Ctrl-;
```

它会在 VS Code 和终端之间快速切换。当前在终端时，Hammerspoon 会直接聚焦已有 VS Code，不会打开或切换当前 tmux pane 对应的项目；当前在 VS Code 或其他 App 时切回终端。终端优先级是：

1. iTerm2
2. Ghostty
3. Terminal

如果这些终端都没有运行，会尝试启动 iTerm2。

### `Alt-Shift-C` / `prefix Enter`: 打开当前项目的 VS Code

tmux 配置：

```tmux
bind -n M-C run-shell -b "~/.hat-config/tmux/scripts/open_vscode_project.sh '#{pane_current_path}'"
bind Enter run-shell -b "~/.hat-config/tmux/scripts/open_vscode_project.sh '#{pane_current_path}'"
bind C-m run-shell -b "~/.hat-config/tmux/scripts/open_vscode_project.sh '#{pane_current_path}'"
```

使用方式：

1. 在任意 tmux pane 中进入项目目录。
2. 按 `Alt-Shift-C`，或按 `prefix Enter`。
3. 脚本会从当前 pane 的路径向上寻找 git root。
4. 找到 git root 后，用 VS Code 打开该项目。
5. 如果当前目录不在 git repo 中，就打开当前 pane 所在目录。

实现策略：

- 优先使用 `code "$project_root"`。
- 然后执行 `open -a "Visual Studio Code"`，让 macOS 聚焦 VS Code。
- 当前不依赖 `yabai`。

注意：

- 如果 VS Code 已经打开同一个 folder，通常会复用或聚焦已有窗口。
- 如果 VS Code 自身策略导致重复窗口，目前不会强行用窗口管理器处理。
- 只有当 VS Code 聚焦/复用无法满足需求时，才重新考虑 `yabai`。
- 如果终端把 `Alt-Shift-C` 输入成 `ç` 或 `Ç`，本项目也会把它当作打开 VS Code 处理。
- 如果 `Alt-Shift-C` 难按，优先使用 `prefix Enter`。

### `prefix ]` / `prefix [`: 创建三 pane AI 工作窗口

tmux 配置：

```tmux
bind ']' run-shell -b "~/.hat-config/tmux/scripts/new_agent_window_prompt.sh here '#{pane_current_path}'"
bind '[' display-popup -E -w 80 -h 16 -d "#{pane_current_path}" "~/.hat-config/tmux/scripts/new_agent_window_prompt.sh ask '#{pane_current_path}'"
```

两个键共用 `new_agent_window_prompt.sh`，区别只在目录从哪来：

- **`prefix ]`（当前目录）**：在当前 pane 所在目录直接建窗，不输入任何东西，名字交给 agent-tracker 自动命名（`项目/标题`）。等价于「快速建一个跟当前项目同目录的 agent 窗口」。
- **`prefix [`（指定目录）**：弹出 popup 用 fzf 从 **z 的目录历史**（`~/.z`，按 frecency 排序、常用目录在前，已删除目录自动过滤）里选——输入关键字即过滤（如输 `hat` 就列出所有含 hat 的历史目录），**Enter 或 Tab 选中高亮项**；候选首行是当前目录，直接回车即用当前目录；要去一个 z 没记录过的新路径，输完整路径（无匹配项）回车即可。确定后在该目录建窗，名字同样自动。

> 候选顺序用 z.sh 同款 frecency 公式从 `~/.z` 现算（`--tiebreak=index` 保序），不依赖 z 的 shell 函数；fzf 不可用时回退为 `read -e` 手输（Tab 文件名补全）。

新窗口按 **Settings → General → Default layout**（默认 auto）生成三 pane 布局；auto 时按当前窗口尺寸自动选横/竖朝向。

两种朝向：

```text
纵向 (portrait)                       横向 (landscape)
+-----------------------------+      +-------------------+-----------+
| agent                       |      |                   | git       |
|                             |      | agent             +-----------+
+--------------+--------------+      |                   | run       |
| git          | run          |      |                   |           |
+--------------+--------------+      +-------------------+-----------+
```

每个 pane 的用途：

- `agent`：运行 Codex、Claude Code 或其他 AI agent。
- `git`：查看 `git status`、diff、log，或做提交相关操作（默认起 `lazygit --screen-mode half`，无 lazygit 时回退 `git status --short --branch`）。
- `run`：跑测试、dev server、watch 命令、脚本等。

新窗口默认是 **auto 模式**：朝向跟随窗口尺寸，后续 resize 自动 reflow（见下）。焦点最后回到 `agent` pane。

### 布局朝向与自动 reflow

新窗口默认 **auto 模式**：朝向由 agent-tracker 的最多 5 秒周期轮询维护（切窗/resize hook 仍即时）——按窗口宽高比（终端 cell 高约宽 2 倍，故宽 ≥ 高×2 判为横向，并留滞回带防止临界抖动）决定该横还是该竖，与当前实际朝向不符就无损重排：用 `break-pane -d` 把 git/run 摘成游离窗口（保留进程）再 `join-pane` 按新朝向接回，不杀 lazygit / run pane 里的进程，并恢复原 active pane。所以把终端在横竖屏之间挪动、拖动 pane 改变窗口比例时，布局会自动跟着切。焦点最后回到 `agent` pane。

**固定朝向**：没有 per-window 手动切朝向的快捷键。要钉死横向/纵向，改 **Settings → General → Default layout** 为 landscape / portrait（对新建窗口全局生效）。

### `prefix w`: window 列表导航

弹出全 session window 列表（Go TUI，替代旧 fzf 版本）。j/k 导航，Enter 跳转；直接按 `0`–`9` 输入 window index 一键跳转；`f` 搜索；`p` 查看当前 AI window 的原始 prompt，详情页按 `c` 复制；`g/o/r` 切换分组/排序；支持 session 折叠展开。详见 `docs/GUIDE_TMUX.md` 中的快捷键表。

**鼠标**：所有弹窗子面板（`prefix w`/`t`/`T`/`alt-s` 主 palette 及 Snippets、Activity Monitor 等）都支持**单击列表行 = Enter**、滚轮翻页；Settings / General / Status Bar / Window Title 等表单页仍用键盘操作。

### `prefix t`: Todo 面板

在 agent palette 中直接打开 Todo 子面板，管理 window 级和全局待办事项。详见 `docs/GUIDE_TODO.md`。

### pane 移动、删除和滚动

当前 active pane 会有更明显的视觉提示：

- 边框颜色更亮。
- pane 顶部显示 `ACTIVE`。
- 按下 prefix 后，状态栏左侧和 active pane 顶部标题都会显示高亮 `PREFIX`；Hammerspoon 还会在屏幕水平居中、距离顶部约 1/3 的位置弹出短暂提示。

pane 之间移动：

```text
prefix h  左
prefix j  下
prefix k  上
prefix l  右
```

也可以不用 prefix：

```text
Alt-h  左
Alt-j  下
Alt-k  上
Alt-l  右
```

在 macOS 上，如果终端把 Option 键输出成特殊字符，本项目也兼容了常见字符：

```text
˙ = 左
∆ = 下
˚ = 上
¬ = 右
```

鼠标操作：

- 点击 pane 可以切换焦点。
- 拖动 pane 边界可以调整大小。

删除当前 window：

```text
prefix x
```

删除当前 pane：

```text
prefix X
```

tmux 会要求确认，按 `y` 才会删除。

分屏：

```text
prefix |    左右分（竖线切一刀）
prefix \    左右分（竖线切一刀，和 | 等价，看顺手）
prefix -    上下分（横线切一刀）
```

这两个是项目自定义，比默认的 `prefix %` / `prefix "` 顺手——字符本身就像线，方向一目了然。新 pane 默认留在当前目录，不需要再 `cd` 一次。

键盘滚动当前 pane：

```text
prefix v
```

进入 copy-mode 后：

```text
j        向下 1 行
k        向上 1 行
Ctrl-d   向下半页
Ctrl-u   向上半页
PageDown 向下翻页
PageUp   向上翻页
g        到顶部
G        到底部
q        退出 copy-mode
Esc      退出 copy-mode
```

复制文字：

```text
Space    开始选择
h/j/k/l  扩展选择范围
y        复制并退出 copy-mode
```

也可以在 copy-mode 里用鼠标拖选，松开鼠标后会复制。当前配置会把复制内容通过 `pbcopy` 写入 macOS 系统剪贴板，所以复制后可以在其他 App 里直接 `Cmd-v`。

快速滚动，不用手动进入 copy-mode：

```text
Alt-u  向上半页
Alt-d  向下半页
```

`Alt-u/d` 也有绑定，但 macOS 上 `Option-u` 可能是 dead key，`Option-d` 可能输入 `∂`。如果 Alt/Option 版本不稳定，进入 copy-mode 后用 `u` / `d` 滚半页，或用鼠标滚动。

## 4. AI session 状态通知

当前 tracker 的状态文件在：

```text
~/.hat-config/state/tracker.json
```

这个目录已被 `.gitignore` 忽略，不会提交到仓库。

### 状态模型

每个 AI session 由 tmux 坐标定位：

- `session_id`
- `window_id`
- `pane_id`

状态字段包括：

- `in_progress`：正在处理。
- `completed`：任务完成，但可能还没被你查看。
- `acknowledged`：你已经确认过这个完成状态。

这套模型对应的使用习惯是：

1. AI session 开始处理一个任务时，标记为 `in_progress`。
2. AI session 完成时，标记为 `completed`。
3. 你查看结果后，标记为 `acknowledged`。

### 如何让状态通知生效

**注意：状态上报已由 agent-tracker daemon 自动化**（见 1.5 节，daemon 经 sessions-json 轮询感知状态、聚焦时自动 ack；不再靠 Claude hook 上报）。以下 `agent_tracker.py` 手动命令不再是日常必需，保留供脚本化、调试或 daemon 未运行时参考。

开始任务时：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py start "实现 tmux agent layout"
```

完成任务时：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py finish "已实现并通过冒烟测试"
```

查看所有状态：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py list
```

确认当前 pane 的完成状态：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py ack
```

删除当前 pane 的 tracker 记录：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py delete
```

输出原始 JSON：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py json
```

跳转到最高优先级的 tracked pane：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py focus
```

### 推荐操作流程

一个手动管理的 AI session 可以这样使用：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py start "重构配置部署脚本"
codex
```

Codex 完成后，在同一个 pane 中执行：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py finish "部署脚本已实现 install/update/uninstall"
```

你回到这个 pane 看完结果后：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py ack
```

也可以在任意 tmux window 中按 `alt-s` → Tracker 查看当前有哪些 session 还在运行或已完成但未确认。

### 当前通知能力的边界

已经具备：

- 手动记录任务开始。
- 手动记录任务完成。
- 手动确认完成状态。
- 用 tmux popup 查看状态列表。
- 用 pane id 记录对应 tmux pane。

现已具备（agent-tracker daemon 自动化，见 1.5 节与 `docs/ARCHITECTURE.md`）：

- 自动检测 Claude Code 开始/完成（sessions-json 轮询，见 1.5 节）。
- 自动在 pane focus 时 ack（聚焦 hook 调 `acknowledge`）。
- 状态变化时系统通知：完成弹「✅ 任务已完成」，转入待回答弹「❓ 有问题需要你回答」，Codex error 弹「⚠️ Codex 执行出错，请查看窗口」。**只在你没看着该窗口时才弹**（和状态栏 🔔 同一逻辑：正看着就不打扰）。点击通知激活终端（Ghostty）并跳到对应 pane；标题不带状态前缀。
- 通知开关：`alt-s` → Settings → **General** → Notifications（ON/OFF）。关掉后仍保留状态栏 ⏳/🔔 图标，只是不发系统通知。
- 通知分组：同页 **Notification grouping**——`single`（默认，新通知替换旧、只留一条）/ `per_window`（每窗口一条独立通知、并发共存；聚焦某窗口时会像灭 🔔 一样自动消掉它的通知）。
- 第三方 `agent-tracker` 的完整 Go daemon（`tracker-server`）+ `tracker-mcp`（MCP）+ palette/TUI + Claude Code hooks 整套已移植。

### General 设置补充项（`alt-s` → Settings → General）

除上面的 Notifications / Notification grouping / Default layout / Status bar position / Icon set / Timer timezone 外，另有 4 项：

- **Poll interval**（默认 `3s`）：window 自动命名 + 状态/🔔 刷新的主轮询节奏。状态栏每秒触发一次，但只按此间隔真正跑一遍完整 sync（导航 hook 仍即时）。`Enter` 在 `1s`/`3s`/`10s` 间循环，`Space` 打开自由输入（支持 `1s`/`10s` 这类 Go duration、裸数字秒如 `5`，非法值回退 3s，钳制到 500ms–60s）。改后无需重启 daemon。
- **New agent prompt**（默认 ON）：ON 时 `prefix ]` 先用 tmux 原生底部 `command-prompt`（`New agent title:`）让你输窗口标题（空 = 自动命名）再建窗；OFF 时直接建窗（旧行为）。`prefix [`（目录选择）不受影响。标题会写进 `@agent_title`、走 date-strip + 自动命名拼成 `[status] 项目/标题 (model)`。含空格的标题整体保留（单引号单参数传给 `new_agent_window.sh`）。
- **Strip date prefix**（默认 ON）：从窗口名/标题段剥掉前导 `YYYY-MM-DD-`，例如 `2026-07-09-open-source-refactor` 在 tmux 状态栏、通知标题、Window Nav 的 Name 列都显示为 `open-source-refactor`。OFF 时保留日期。
- **Window nav size**（默认 `wide`）：`prefix w` 窗口导航弹窗宽度档位——`standard`（紧凑，~140 cap）/ `wide`（默认，~180 cap，够宽让底部提示行单行显示、Name 列留足宽度）/ `full`（~96% client 宽）。
- **URL picker folders**（默认 OFF）：`prefix u` 抓屏结果里是否包含文件夹条目。默认关——裸词目录误报多（prompt/命令行里的 `tmux`、`scripts` 这类 token 都是真实存在的目录），且按「最近在前」排序时它们常占据列表头部；只在确实需要用 VS Code/Finder 打开目录时再开。文件/URL 条目不受影响。

> **未来可配置（backlog，暂未实现，仅记录）**：completion grace window（busy→idle 完成去抖，现固定 2s）、remote-bell interval（现固定 3s）、workspace-save interval、window-title 最大长度、auto-rename 总开关、通知声音。

仍部分依赖手动：summary 文本由上报方传入或缺省（未做 AI 输出自动提取）。

## 5. AI session 具备哪些能力

这里说的“能力”指当前这套工作流赋予 AI session 的操作环境能力，而不是某个模型自身的能力。

### 项目级上下文隔离

通过”一个项目一个 tmux session”，AI session 会更稳定地留在对应项目目录下。`prefix ]`（当前目录）/`prefix [`（指定目录）创建的新 window 会从目标路径解析 git root，并把三个 pane 都放在项目根目录。

这意味着 AI session 通常可以直接：

- 读取项目文件。
- 修改项目文件。
- 运行项目命令。
- 调用 git 查看 diff/status。
- 使用右侧 pane 运行测试或 dev server。

### 多 agent 并行

通过“一个 agent 一个 tmux window”，你可以在同一个项目里同时开多个 AI session。

例子：

- window `codex-impl`：负责实现功能。
- window `codex-review`：负责 review diff。
- window `claude-notes`：负责整理设计文档。

tracker 会按 tmux pane 记录它们的状态，因此可以同时追踪多个 session。

### Git 与运行环境分离

三 pane 布局避免了所有命令挤在一个终端里：

- AI 输出留在 `agent` pane。
- git 状态和提交操作留在 `git` pane，默认使用 `lazygit --screen-mode half`。
- 测试、server、watch 命令留在 `run` pane。

这可以减少误操作，也方便你快速判断 AI 改动是否还需要验证。

### 可恢复的本地工作流

配置保存在 `~/.hat-config`，通过 `scripts/deploy.sh` 接入真实环境。

这意味着：

- 配置可版本化。
- 可以卸载。
- 可以在修改后重新部署。
- 可以把运行时状态和仓库内容分开。

## 6. VS Code 操作

当前 VS Code 入口是 `Alt-Shift-C` 或 `prefix Enter`。

### 从 tmux 打开当前项目

在任何 pane 中按：

```text
Alt-Shift-C
```

如果 `Alt-Shift-C` 被终端输入成符号，也可以用：

```text
prefix Enter
```

脚本执行逻辑：

1. 读取 tmux 当前 pane 路径。
2. 如果这个路径在 git repo 中，解析到 git root。
3. 如果不在 git repo 中，使用当前目录。
4. 调用 `code "$project_root"`。
5. 调用 `open -a "Visual Studio Code"` 聚焦 VS Code。

### 如果 VS Code 没有打开

会打开 VS Code 并加载当前项目目录。

### 如果 VS Code 已经打开

VS Code 通常会复用同一个 folder window 或聚焦已有窗口。具体行为取决于 VS Code 自身对 folder 的处理。

### 如果打开失败

可能原因：

- `code` 命令没有安装到 PATH。
- VS Code app 不在 `/Applications/Visual Studio Code.app`。
- 当前 pane 路径不存在。

检查 `code`：

```bash
command -v code
code --version
```

在 VS Code 中安装 `code` 命令的常见方式：

1. 打开 VS Code。
2. 打开 Command Palette。
3. 运行 `Shell Command: Install 'code' command in PATH`。

## 7. 推荐日常流程

### 第一次部署

```bash
cd ~/.hat-config
~/.hat-config/scripts/deploy.sh install --yes
```

如果已经有 tmux server，部署脚本会尝试 reload。

### 为项目创建 tmux session

可以手动创建：

```bash
cd /path/to/project
tmux new-session -s project-name
```

也可以 attach 到已有 session：

```bash
tmux attach -t project-name
```

### 创建 AI 工作窗口

在项目 session 中：

```text
prefix ]
```

在当前目录直接新建 auto 模式窗口，名字自动；想换目录则改按 `prefix [`，在 popup 里用关键字从 z 历史选目录（无匹配时可直接输完整路径）。

### 开始 AI 任务

在 `agent` pane：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py start "实现 README 和 GUIDE"
codex
```

或者运行其他 agent：

```bash
claude
```

当前 tracker 不关心你运行的是 Codex、Claude Code 还是其他命令。它只记录当前 tmux pane 的状态。

### 查看状态

在任意 tmux pane：

```text
alt-s → Tracker
```

或直接运行：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py list
```

### 完成后标记

在对应 AI pane：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py finish "完成 GUIDE 初稿"
```

看完结果后：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py ack
```

### 打开 VS Code

在任意项目 pane：

```text
Alt-Shift-C
```

备用入口：

```text
prefix Enter
```

在 VS Code 和终端之间切换：

```text
Ctrl-;
```

从终端触发时，它会直接聚焦已有 VS Code；从 VS Code 触发时，它会切回终端。这个快捷键只做 App 切换，不负责打开当前项目。打开当前项目仍然使用 `prefix Enter` 或 `Alt-Shift-C`。

## 8. 故障排查

### 快捷键没有生效

先检查是否部署：

```bash
~/.hat-config/scripts/deploy.sh status
```

再手动 reload：

```bash
tmux source-file ~/.tmux.conf
```

检查绑定是否存在：

```bash
tmux list-keys -T root M-C
tmux list-keys A
tmux list-keys T
```

### `prefix ]` / `prefix [` 没有创建 layout

检查脚本权限：

```bash
ls -l ~/.hat-config/tmux/scripts/new_agent_window*.sh
```

重新部署会自动修复权限：

```bash
~/.hat-config/scripts/deploy.sh update --yes
```

### Tracker 显示没有记录

这通常表示还没有手动标记任何任务。

在某个 AI pane 中执行：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py start "测试 tracker"
```

再按 `alt-s` → Tracker 查看。

### tracker 状态看起来不对

查看原始 JSON：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py json
```

删除当前 pane 的记录：

```bash
python3 ~/.hat-config/scripts/agent_tracker.py delete
```

完全清空 tracker 状态：

```bash
rm -rf ~/.hat-config/state
```

### 卸载后快捷键仍然存在

如果 tmux server 已经运行，先执行：

```bash
~/.hat-config/scripts/deploy.sh uninstall --yes --keep-state
```

然后 reload：

```bash
tmux source-file ~/.tmux.conf
```

如果仍然存在，说明快捷键可能来自其他 tmux 配置。用下面命令定位：

```bash
tmux list-keys -T root M-C
tmux list-keys A
tmux list-keys T
```

## 9. 当前设计原则

这套配置目前遵循几个原则：

- 先小范围可用，再逐步自动化。
- 不直接修改第三方配置仓库。
- 不默认依赖 `yabai`。
- 运行时状态不进 git。
- 部署必须可重复执行，也必须能卸载。
- tracker 目标是完整还原参考仓库的 daemon + MCP + palette/TUI + Claude hooks 自动上报整套能力。

## 10. SSH 远程嵌套 tmux + agent-tracker（远程开发）

让这套 tmux + agent-tracker 工作流在「本地 tmux 外层嵌套远程 tmux 内层」的 SSH 场景下可用：`ssh mini` + `tmux attach` 后，远程机也跑完整 agent-tracker 栈（自动命名 / busy-idle / 🔔），同时解决嵌套带来的键传递、双重 status、ssh 窗辨识三个冲突。

### 10.1 一次性准备：Syncthing 共享 + 远程部署

1. **Syncthing 共享 `~/.hat-config`**：两端各放一个 `.stignore`，内容只需一行 `#include .stignore-shared`；共享规则集中在随同步分发的 `.stignore-shared`（已排除 `state/`、`agent-tracker/bin`、worktree 等机器独立/构建产物）。`.git` 参与同步，故两端 git HEAD 一致——deploy 前用 `ssh mini 'cd ~/.hat-config && git rev-parse HEAD'` 比对，确认本机改动已同步到位。
2. **远程机部署**：`ssh mini`，`cd ~/.hat-config && scripts/deploy.sh install`。
   - **tmux 版本须 ≥ 3.3**：本配置用到 `allow-passthrough`、`window-resized` hook、`pane-border-indicators` 等 tmux 3.3+ 选项。旧版（如 3.2a）`source` 时会报形如 `invalid option: allow-passthrough` 的非致命告警、并使 resize 相关 hook（status 位置 / 布局 reflow 随尺寸更新）失效。`tmux -V` 确认；homebrew 机器 `brew upgrade tmux` 升级。本任务的 F12 passthrough / ssh 检测本身兼容 3.2a，但整套配置以现代 tmux 为前提。
   - `deploy.sh install` 自带 `go build`（`agent-tracker/bin` 不跨机同步，每台机器自建二进制）+ 安装 launchd daemon + 写 `~/.tmux.conf` 的 managed block。
   - **go 须在 PATH**：非交互 ssh 默认 PATH 不含 homebrew（`/opt/homebrew/bin`），`go build` 会失败。经 ssh 直接跑时先 `export PATH=/opt/homebrew/bin:$PATH`，或在交互登录的远程 tmux 里跑（登录 shell PATH 已含 go）。
   - 远程当前若无运行的 tmux server，install 末尾会有形如 `warning: failed to reload /Users/<you>/.tmux.conf` 的告警——无害（无 server 可 reload），下次起 tmux 时生效。
3. **校验**：`scripts/deploy.sh status` 报 `Deployment: installed` + daemon `state = running`；`grep -c hat-config ~/.tmux.conf` > 0。

### 10.2 日常使用

本地 tmux 内开一个窗，`ssh mini`，再 `tmux attach`（或 `tmux new`）进远程 tmux。此后内外两层都 source 同一份 `tmux.conf`，靠运行时检测区分角色：

- **F12 passthrough（键传递）**：嵌套时外层 root 表会抢掉 `C-b`/`C-s`（prefix 入口）与 `M-*` 等键。在外层按 **F12** → 外层 root 表暂停、状态栏变橙底 `⌨ PASSTHROUGH`，此后所有键直达内层远程 tmux 直接操作；再按 **F12** 切回外层、指示还原。
  - **二次逃逸**：部分终端/OS 会吞 F12——此时在 passthrough 状态下按 **`C-q`** 同样切回外层（防被锁死）。
- **`C-q` 内层 prefix 单键（轻量替代 F12）**：不想进全透传、只想偶尔给内层发个 prefix 时，普通态下按 **`C-q`** 即向当前 pane 发送内层 tmux 的 prefix（`send-keys C-b`），随后按命令键就操作内层远程 tmux。和 F12 互补——F12 是「所有键持续透传」，`C-q` 是「一次性发一个内层 prefix」。注意：① 用 C-b 而非 C-s（C-s 是 XOFF 会被 flow control 吞）；② 若 `C-q` 按下无反应，是终端把它当 XON 吃了，先 `stty -ixon`；③ 命令键里若用到外层占用的 `M-*`/`F*` 仍会被外层抢，那种连续操作还是用 F12。passthrough 态下的 `C-q` 仍是二次逃逸，互不冲突。
- **🌐 ssh 窗标记**：任一 pane 跑 ssh 的窗，窗名自动显示 `🌐 host`（host 从 ssh 命令行解析，如 `🌐 mini`）；ssh 退出后标记自动清除、窗名交还 tmux 自动命名。手动 `prefix ,` 改名后不被覆盖。
- **远程 🔔 透传**：当你 ssh 到的那台机器（如 mini）上有任意 window 出现 🔔（任务完成待查看 / asking 待回答），本地这个 `🌐 host` 窗的 tab 也会亮 🔔、并在 Window Nav 里进「需要处理」组，同时弹一条本地系统通知（点击跳到该 ssh 窗）。远端 🔔 消失后本地同步熄灭、通知撤销。要求：远端也部署同一套 hat-config（tracker 在跑）、且本机能免密 ssh 到它（用 `~/.ssh/config` 里的 host 别名）。本地 daemon 每 3s 经复用的 ssh 连接读远端 tracker 状态实现，对远端零改动。正在看着该 ssh 窗时不重复弹通知。
- **外层 pane border 让位**：含 ssh pane 的窗，外层 tmux 的 pane border 标题（`[ACTIVE] cmd — dir`）会自动隐藏，避免压在内层远程 tmux 的 status line 上；ssh 退出后恢复。仍可手动 `setw pane-border-status top` 覆盖。
- **status 错位（per-machine 固定）**：内层（远程机）status 固定**上方**、外层（本地机）固定**下方**，两层错开不叠。这靠**每台机器各自的固定设置**实现（不随尺寸/朝向自动变换）：
  - 本地（外层）机：General → Status bar position 设 `bottom`（或 `agent-config.json` 的 `status_position: "bottom"`）——含 ssh 窗、含竖版一律底部。
  - 远程（内层）机：同设为 `top`。
  - `agent-config.json` 是 per-machine、不跨机同步，故两机可各设不同固定值。仍用 `auto` 的机器：ssh 窗会自动 top 作兜底（旧行为保留）。
- 内层远程也能 `prefix ]` 开 agent 窗、跑 Claude/Codex，状态/🔔 由**远程** daemon 驱动、原样显示在内层 status。

### 10.3 注意点与回滚

- **`agent-config.json` 是 per-machine**：window-title 开关 / layout_default / status_position 存在 `~/.config/agent-tracker/agent-config.json`，**不在 `~/.hat-config` 路径下、不被 Syncthing 同步**——有意为之，每台机独立设置。跨机想一致需手动对齐。
- **`alias-common` 跨机幂等**：被 Syncthing 同步、两机都 deploy，`register_alias` 仅在缺失时追加，不冲突。
- **daemon / state 机器独立**：daemon 由各机 launchd 自管；`state/` 不跨机同步（`.stignore-shared` 已排除），两端各跑各的、互不打架。
- **回滚**：`deploy.sh install` 基本幂等（managed block 标记替换、launchd 可重入），中途失败重跑即可；彻底回退 = 删 `~/.tmux.conf` 的 managed block + `launchctl bootout gui/$UID/app.hat-tmux-workbench.agent-tracker`（或 `scripts/deploy.sh uninstall`）。
- **deploy 失败先查同步**：远程报缺文件 / 源码过期，多半是 Syncthing 还没同步完——先比对两端 git HEAD 再 deploy。
- ssh 接入靠手动（无快捷键 / palette 入口）；桌面通知桥接、3+ 层深嵌套优化不在当前范围。

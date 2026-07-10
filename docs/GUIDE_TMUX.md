# tmux 基础与快捷键指南

这份文档只讲 tmux 的基础概念、常用命令，以及本项目当前配置的快捷键。

## 1. tmux 的三层结构

tmux 里最重要的是三层：

```text
session = 一个长期存在的工作空间，推荐对应一个项目
window  = session 里的一个标签页，推荐对应一个 agent 或一个工作主题
pane    = window 里的一个分屏区域，推荐对应 agent / git / run
```

本项目推荐的使用模型：

```text
一个项目 = 一个 tmux session
一个 AI 任务或 agent = 一个 tmux window
一个 agent window = 三个 pane
```

三 pane 的职责：

- `agent`: 运行 Codex、Claude Code 或其他 AI agent。
- `git`: 运行 `lazygit`，查看 diff、stage、commit。
- `run`: 跑测试、dev server、watch 命令或临时 shell 命令。

## 2. prefix 是什么

tmux 的大部分快捷键都不是直接按一个键，而是先按 prefix，再按命令键。

本项目当前支持两个 prefix：

```text
Ctrl-s
Ctrl-b
```

推荐优先用 `Ctrl-s`，因为比默认的 `Ctrl-b` 更近。

例如按下 prefix 后再按某个键触发操作：

```text
Ctrl-s
Shift-a
```

这等价于：

```text
Ctrl-b
Shift-a
```

注意：`Ctrl-s` 在一些终端里是 XOFF 暂停输出键。如果按下后终端像卡住了一样，先按 `Ctrl-q` 恢复。若 `Ctrl-s` 没有进入 tmux，需要在 shell 中关闭 flow control：

```bash
stty -ixon
```

因为它现在也是 tmux prefix，所以如果你想把真正的 prefix 发给 shell，可以连续按两次：

```text
Ctrl-s
Ctrl-s
```

按下 prefix 后，tmux 状态栏左侧会显示高亮的 `PREFIX`，这可以用来确认 prefix 是否已经被 tmux 接收。active pane 顶部标题也会同步显示 `[PREFIX]`。

### 中文输入法下的 prefix 透传

中文输入法（macOS 自带拼音/双拼）会把 `Ctrl-b` 当成 Emacs 风格「光标后退」吃掉，导致 prefix 进不去 tmux。为此 prefix 不再走 tmux 内建 `prefix` 选项，而是用 root-table 绑定拦截：按下 `Ctrl-b` / `Ctrl-s` 时先调 `agent ime switch` 把输入源切到 ABC，再 `switch-client -T prefix` 进入 prefix 键表。切换走 Carbon `TISSelectInputSource`（`agent-tracker/cmd/agent/ime_darwin.go`），由 agent 二进制一次性执行，不依赖任何常驻进程。

> 历史方案曾用 Hammerspoon `CGEventTap` 拦截 keyDown，但该 tap 长稳运行会被 macOS 系统层静默 mute（用一阵子后失效、需 reload），已弃用。

如果切换没生效，先确认：

- `~/.hat-config/agent-tracker/bin/agent` 存在且可执行（改 Go 代码后需重新 `go build -o bin/agent ./cmd/agent`）。
- 系统设置 ▸ 键盘 ▸ 输入法里启用了 `ABC`（`agent ime switch` 找不到会以非 0 退出，但 prefix 仍会进入——绑定带 `|| true` 兜底）。

只切到 ABC，不自动切回中文（切回 CJKV 是 macOS 上不可靠的方向）；需要打中文时手动切回。iTerm2 Trigger 不适合解决这个问题，因为 Trigger 匹配的是终端接收到的输出文本，不是键盘输入事件。

> 注意：若你的中文输入法把 `Ctrl-b` 在到达 tmux **之前**就完全吃掉（按下毫无反应、连 `PREFIX` 都不出现），说明拦截发生在 IME 上游，tmux 层无能为力，需要改用 Karabiner 的 `select_input_source` 规则在 HID 层先切 ABC 再透传按键。

tmux 原生只有一条 status line，不能同时在顶部和底部各放一条完整 status。status line 位置由 **Settings → General → Status bar position** 控制，三种模式（默认 auto）：
- `auto`：**跟随当前 active window 的布局朝向**——横向布局 → 底部，纵向布局 → 顶部。布局朝向变化（自动 reflow 或改 Default layout）就等于切换 status 上下位置。
- `top` / `bottom`：固定到该位置，不随布局变。

实现：`tmux/scripts/update_status_position.sh` 读 General 设置（`agent tmux status-position`）；auto 时读当前 window 的 `@agent_orientation`，幂等 `set -g status-position`；在布局变换（`reflow_agent_layout.sh`/`build_agent_layout.sh`）、切窗/尺寸变化（`after-select-window`/`window-resized` hook）、`client-resized`/`client-attached` 时调用。非 agent 窗口回退按 client 视觉宽高比（`client_width < client_height*2`）判定。prefix 状态另外叠加到 active pane 标题里，无论 status 在顶还是底都能就近看到。

Hammerspoon 还提供全局快捷键：

```text
Ctrl-;
```

用于在 VS Code 和终端之间快速切换。当前在终端时，Hammerspoon 会直接聚焦已有 VS Code，不会打开或切换当前 tmux pane 对应的项目；当前在 VS Code 或其他 App 时切回终端。终端优先级是 iTerm2、Ghostty、Terminal。如果都没有运行，会尝试启动 iTerm2。

## 3. 本项目新增的快捷键

这些来自 `~/.hat-config/tmux/tmux.conf`。

| 快捷键 | 作用 |
| --- | --- |
| `Alt-Shift-C` | 打开当前 pane 所在项目的 VS Code |
| `prefix Enter` | 打开当前 pane 所在项目的 VS Code |
| `prefix ]` | 在当前目录直接新建三 pane agent window（无输入，名字自动） |
| `prefix [` | 弹窗从 z 目录历史选目录（关键字过滤，可输新路径）后新建 agent window |
| `prefix w` | 打开 window 列表导航（全 session，Go TUI，支持折叠/搜索） |
| `prefix W` | tmux 内置 choose-tree（备用） |
| `prefix t` | 打开本 window 定时任务面板（独立弹窗：同屏三栏 This Window/Other Windows/History，`Tab` 切焦点 / `Ctrl+P` 从 snippet 取） |
| `prefix T` | 打开 agent palette 并进入 Todo 视图（窗口/全局 todos） |
| `prefix s` | 存档当前 workspace 骨架快照 |
| `prefix r` | 恢复最近一次 workspace 快照（仅限干净 tmux） |
| `prefix g` | fzf 弹窗选历史快照恢复 |
| `prefix x` | 删除当前 window（带确认） |
| `prefix X` | 删除当前 pane |
| `prefix v` | 进入 copy-mode，用键盘滚动当前 pane |
| `prefix \|` / `prefix \` / `prefix -` | 左右 / 左右 / 上下分屏（`|` 和 `\` 都是 `split-window -h`；`-` 是 `split-window -v`） |
| `prefix h/j/k/l` | 向左/下/上/右移动 pane |
| `prefix Tab` | 跳到上一个 active window（last-window，LRU 栈），与 `prefix n`/`prefix p`（顺序切窗）互补 |
| `Alt-u/d` | 当前 pane 向上/下滚半页 |
| `Alt-h/j/k/l` | 无需 prefix，向左/下/上/右移动 pane |
| `˙/∆/˚/¬` | macOS Option 键输出这些字符时，向左/下/上/右移动 pane |
| 鼠标点击 pane | 切换 pane 焦点 |
| `F1`~`F9`（或 `Ctrl-1`~`Ctrl-9`） | 按编号切换 session（工作空间），无需 prefix。`Ctrl-数字` 多数终端不发送，`F1`~`F9` 为稳定兜底 |
| `prefix S`（Shift+s） | 新建 session：底部弹输入框填 label（最终名为 `<编号>-<label>`），直接回车走自动编号（`<编号>-session`）。与 `prefix s`（save workspace）配对 |
| `Alt-s` / `prefix o` | 打开 agent palette（工作空间总览/切换）；`Alt-s` 无需 prefix，`prefix o` 是 Option 不发 Meta 时的兜底 |
| `prefix .` | 重命名当前 session |
| `prefix <` / `prefix >` | 在 session 列表中左右移动当前 session |

### 终端内点击链接 / 文件路径

tmux.conf 已启用终端能力（`terminal-features` 含 `hyperlinks`/`osc7`、`allow-passthrough on`），让 tmux 不再剥离 OSC 8 超链接、并把 pane 的 cwd 透传给终端 App（Ghostty / VSCode 集成终端 / iTerm2 通用）：

- **URL 链接**：程序用 OSC 8 输出的超链接可直接点击跳浏览器。
- **文件路径开 vscode**：路径/链接的识别本身是**终端 App 自己的能力**（各自正则匹配屏幕文本），tmux 只负责不破坏（不剥离 OSC8、经 osc7 透传 cwd 让相对路径可解析）。
- **鼠标被 tmux 抢**：开了 `mouse on` 后点击会先被 tmux 接管，需按终端的 bypass 修饰键（如 Ghostty 默认按住 ⌥/Shift）把点击交给终端 App。
- osc7 还依赖 shell 真正 emit cwd 转义（zsh 的 `precmd`/`chpwd`）；若相对路径点击仍不灵，检查 `.zshrc` 是否配了 OSC7 上报。

### session 作为工作空间切换

本项目把 tmux **session 当作工作空间**：每个 session 一个项目/任务，命名为 `<编号>-<标签>`（如 `1-refactor`）。

- `F1`~`F9`（或 `Ctrl-1`~`Ctrl-9`）直接按编号跳到对应 session——像浏览器标签一样切换工作空间，状态栏左侧的 session tabs 显示全部工作空间及其 ⏳/🔔 状态。多数终端不发送 `Ctrl-数字`，优先用 `F1`~`F9`。
- **鼠标点击 session tab 也能切换**：直接点状态栏左侧某个工作空间标签即跳到该 session（与点 window tab 切窗并存）。
- session tab 的 ⏳/🔔 是该 session 下**当前窗口**的聚合：🔔 = 有窗口未读（任务完成待查看 / asking 待回答），⏳ = 有窗口正忙（`[B]`）。全部窗口 idle 时两者都不显示。
- `prefix S`（Shift+s）新建 session：底部弹输入框填 label（最终名 `<编号>-<label>`），直接回车走自动编号（`<编号>-session`）。与 `prefix s` save workspace 配对。`prefix .` 给当前 session 起名（窗口名随之按命名模板同步）。
- `Alt-s` 打开 agent palette，可视化浏览/跳转所有工作空间。
- 用 `agent` 启动器（见 `docs/GUIDE.md`）会自动建好带三 pane 布局、自动命名的工作窗口，通常不必手动建。

### 复制文字

tmux 里不能直接按普通终端的方式稳定复制历史输出，推荐进入 copy-mode：

```text
Ctrl-s
v
```

进入后使用 vi 风格移动：

```text
h/j/k/l     左/下/上/右
gg          到顶部
G           到底部
Ctrl-u      上半页
Ctrl-d      下半页
/           向下搜索
?           向上搜索
q           退出
```

复制一段文字：

```text
Space       开始选择
h/j/k/l     移动光标扩展选择
y           复制并退出 copy-mode
```

也可以在 copy-mode 里用鼠标拖选，松开鼠标后会复制。当前配置会把 tmux copy-mode 的复制内容通过 `pbcopy` 写入 macOS 系统剪贴板，所以复制后可以在其他 App 里直接 `Cmd-v`。

### `Alt-Shift-C` / `prefix Enter`: 打开 VS Code

在任意 tmux pane 中按：

```text
Alt-Shift-C
```

如果终端把 `Alt-Shift-C` 输入成符号，可以用备用入口：

```text
Ctrl-s
Enter
```

行为：

- 如果当前目录在 git repo 中，打开 git root。
- 如果不在 git repo 中，打开当前目录。
- 优先用 `code "$project_root"`。
- 然后用 `open -a "Visual Studio Code"` 聚焦 VS Code。
- macOS Option 输出 `ç` 或 `Ç` 时，也会被当作打开 VS Code。

### `prefix ]` / `prefix [`: 新建 agent window

- **`prefix ]`**：在**当前 pane 目录**直接创建三 pane（agent/git/run）布局，不弹任何输入，名字走默认（由 agent-tracker 自动命名为 `项目/标题`）。
- **`prefix [`**：弹出 popup 用 fzf 从 **z 的目录历史**里选目标目录——输入关键字即过滤（frecency 排序，常用目录在前），**Enter/Tab 选中高亮项**；首行是当前目录，直接回车即用它；去 z 没记录的新路径就输完整路径（无匹配）回车。确定后在该目录新建窗口，名字同样自动。

两键共用 `tmux/scripts/new_agent_window_prompt.sh`（`here` / `ask` 两个模式），底层都走 `new_agent_window.sh`。

> 候选从 `~/.z` 现算（z.sh 同款 frecency 公式，过滤已删除目录），不依赖 z 的 shell 函数；fzf 不可用时回退 `read -e` 手输（Tab 文件名补全）。

**默认布局模式**来自 **Settings → General → Default layout**（默认 auto）：auto 按窗口宽高比选横向或纵向（终端 cell 高约宽 2 倍，宽 ≥ 高×2 视为横向），也可设成固定 landscape / portrait。

两种朝向布局：

```text
纵向 (portrait)                       横向 (landscape)
+-----------------------------+      +-------------------+-----------+
| agent                       |      |                   | git       |
|                             |      | agent             +-----------+
+--------------+--------------+      |                   | run       |
| git          | run          |      |                   |           |
+--------------+--------------+      +-------------------+-----------+
```

auto 模式窗口：朝向跟随窗口尺寸，后续 resize（拖动 pane、改终端窗口、横竖屏切换）自动 reflow——无损重排，不杀 lazygit / run pane 里的进程，并保留当前 active pane。**切到某个 window 时也会即时按当前屏幕修正其布局**（`after-select-window`/`window-resized` hook → `agent tmux reflow-focus`），所以从竖屏切到横屏后，逐个切过去的窗口都会自动适配新朝向，不会停留在旧布局。

### 固定布局朝向

不再有 per-window 手动切朝向的快捷键（原 `prefix [` 循环朝向已移除）。要钉死横向/纵向，改 **Settings → General → Default layout** 为 landscape / portrait（全局对新建窗口生效）；auto 则交回自动按尺寸 reflow。朝向存 window 选项 `@agent_orientation_mode`（auto/landscape/portrait）/ `@agent_orientation`。

### Git pane 里的 lazygit

三 pane 工作流会在 `git` pane 中启动：

```bash
lazygit --screen-mode half
```

`--screen-mode` 是 lazygit 自带的初始视图模式，可选值是 `normal`、`half`、`full`。竖屏布局默认用 `half`，让当前 focus panel 更大一些；如果仍然觉得太挤，可以把脚本里的参数改成 `full`，或者后续为这套工作流单独加一份 lazygit config。

### `prefix w`: window 列表导航

按 `prefix w` 打开 Go TUI popup，列出**所有 session** 的 window，默认按 session 分组、按最近活动排序（进入后排序稳定，不随 tick 刷新变动）。

**默认 session 视图的待处理浮顶**：默认视图下，需要处理的 window（带 🔔：任务完成待查看、或 asking/limited 未读）会从各自 session 组**移出**、聚到列表最顶部的「需要处理」组（组名不加 🔔 前缀，每行自带 🔔 列），按**铃铛出现时间升序**排（最早等待的在最上）。打开 popup 时光标自动落在第一个待处理 window，直接 `Enter` 即可处理等待最久的那个。进入该 window 后 acknowledge 会清掉 🔔，即使 activity 仍显示 `?` 或 `L`，它也会离开「需要处理」并回到普通 session 分组。session 组内其余 window 按**有 agent 闲置 > 有 agent 忙碌 > 无 agent** 排序。

**快捷键（popup 内）：**

| 键 | 作用 |
| --- | --- |
| `j` / `k` | 上下移动光标（**自动跳过分组标题行**，光标只落在可选 window 上） |
| `0`–`9` | 直接输入 window index 跳转到对应 window（同名优先当前 session）；多位 index 连续输入，约 0.45s 内无法再延长即跳转 |
| `Enter` | 跳转到选中的 window，关闭 popup（组名行不可选中，故无 Enter 折叠） |
| `f` | 激活搜索（字符实时过滤）；搜索中 `ESC` 清空并退出，`Enter` 保留 query 退出 |
| `p` | 查看当前 AI window 的原始 user prompt；详情页 `c` 复制，`Esc` 返回 |
| `g` | 切换分组方式：session → none → status → path → attention（当前值显示在 footer 的 `g:` 提示上） |
| `o` | 切换排序方式：activity ↔ index（下次 `g/o/r` 时生效） |
| `r` | 翻转排序方向 |
| `x` | 删除当前选中的 window |
| `Esc` / `q` | 关闭 popup |

Session 分组时，`●` 标记当前所在的 session。组名（分组标题）行不可被光标选中，`j/k` 会自动跳过。搜索时自动展开所有 session 显示完整结果。

`attention` 分组把**需要处理**的 window（带未读 🔔：任务完成待查看、或 asking/limited 尚未访问）聚到顶部「需要处理」组，其余归「其它」组——一眼锁定该回应的窗口。进入看过后 🔔 清除，`?`/`L` 可继续作为 activity 状态显示，但不会单独把窗口留在「需要处理」。

当前分组/排序状态有可视提示：footer 的 `g:` 直接显示当前分组方式（`session`/`none`/`status`/`path`/`attention`）；列头会在被排序的列（`#` 或 `Activity`）上加方向箭头 `↑`/`↓`，表示当前排序字段与升降序。

`g`/`o`/`r` 的选择会持久化到 `~/.config/agent-tracker/agent-config.json` 的 `window_nav` 字段（`group_by`/`order_by`/`order_dir`），下次打开面板自动恢复；全为默认值（session / activity / desc）时不写入该字段。

**Prompt 详情**：`p` 只对能定位到 Claude/Codex 会话记录的 AI window 生效。Claude 读取 `~/.claude/projects/.../<sessionId>.jsonl` 的第一条 user message；Codex 读取 `~/.codex/state_5.sqlite` 指向的 rollout JSONL 的第一条 `user_message`。详情页不会把完整 prompt 写入 tmux window option；按 `c` 时才复制到系统剪贴板。

**窄屏 / 竖屏自适应**：popup 宽高都有上限（宽 cap 130、高 cap 45 行），不会在竖屏铺满整屏。列布局按宽度自适应，空间不足时按 Model → Provider → Directory → Activity 的顺序依次隐藏，Name 列始终保留可用宽度（中文名按显示宽度截断，不会折行）。底部快捷键提示一行放不下时自动换两行。

### `prefix t`: 本 window 定时任务面板

独立弹窗（`agent window-timer`），仿 todo 面板**同屏三栏**：左列 **This Window**（上）+ **Other Windows**（下），右列 **History**。**`Tab` 切换焦点栏**（高亮当前栏），`Esc`/`g` 退出弹窗。键作用于当前焦点栏。

- **This Window**：列出**当前 window** 的 timer，`j/k` 导航、`Enter`/`Space` 启停、`a` 新建、`e` 编辑、`x` 删除。Add 表单字段 Content/Trigger（`5m`/`1h30m`/`3h20m`/`13:10`）/Loop（`none`/`5m`/`daily`）/Max/Send-Enter；`HH:MM`、`daily` 和所有 Next 时间按 **Timer timezone** 解释与显示（默认东八区 UTC+8；`Alt-s → Settings → General` 可选 system `auto` 或输入 IANA/UTC offset）；表单内 **`Ctrl+P` 打开 snippet 选择器**（定位 `timer` 分组）选一条内容回贴 Content。
- **Other Windows**：聚合**其它 live window** 的 timer，每行带该 window 的**状态图标**（●busy/○idle/?asking，源自窗口名 `[B]`/`[I]`/`[?]` 前缀）、**名字**、**目录**（`@agent_dir`）；`Enter` 启停 / `e` 编辑 / `x` 删除作用于所选 timer 的所属 window（跨窗管理）。**已结束（tmux 中已不存在）的 window 的 timer 不显示**。每条仍 fire 到各自 window。
- **History**：列出本 window 用过的 timer 内容（per-window 持久化，timer 删了仍在；按 content+trigger 全组合去重）。`Enter` 用该历史预填新建、`s` ★把它存入 snippet 库（默认 `timer` 分组）、`x` 删历史项。
- timer 触发时由 daemon 每秒轮询，向该 window 的 ai pane `send-keys` 内容（可选回车）；trigger 支持复合时长 `3h20m`。

### `prefix T`: Todo 视图

在 agent palette 中直接打开 Todo 子面板（`--open=todos`），管理窗口级 / 全局 todos。与 `Alt-s` 打开的 palette 相同界面，只是跳过了列表直接进入 Todo。任务进行中/完成状态由 daemon 经状态栏 ⏳/🔔 与系统通知反映（聚焦窗口自动 ack），不再有独立的 palette Tracker 面板。

### Snippet 内容库（`Alt-s` → Paste snippet）

可复用文本片段库，存 `~/.hat-config/snippets/`（**子目录 = 分组**，根目录文件归 `ungrouped`）。每文件一条：首行 `# 描述`、其余正文、`{{var}}` 变量占位。

- 打开：`Alt-s` palette → 选「Paste snippet」。面板按分组可折叠显示，`★ favorites` 组置顶。
- 键位（hjkl）：`j/k` 导航、`l/Enter` 折叠组 / 粘贴选中、`a` 新建、`e` 编辑、`x` 删除、`s` 收藏、`f` 搜索、`h/Esc` 返回。
- **新建不再需要手建文件**：面板内 `a` 填 Name/Group/Description/Content 即可（Content 内 Enter = 换行）。含 `{{var}}` 的片段粘贴前会逐个提示填值。
- **手动创建（带占位）**：也可直接建文件——文件路径 `~/.hat-config/snippets/<分组>/<名字>`，首行 `# 描述`，正文用 `{{名字}}` 占位。例：

  ```bash
  mkdir -p ~/.hat-config/snippets/git
  printf '# 切分支并拉取\ngit checkout {{branch}} && git pull\n' > ~/.hat-config/snippets/git/checkout-pull
  ```

  之后 `Alt-s` → Paste snippet 会在 `git` 分组下看到它，粘贴时提示填 `branch`。名字不能以 `.`/`_` 开头或含 `/`。
- 收藏记录在 `~/.hat-config/snippets/.favorites`（git/Syncthing 同步友好）。timer 面板与此库共用一套内容（见上方 `prefix t` 的 `Ctrl+P` / 历史 `s`）。

### Workspace 存档 / 恢复（崩溃保命）

tmux server 偶尔会整体崩溃（`[server exited]` / `tmux a: no sessions`），所有 session/window 一次性丢失。这套机制**只记录骨架快照**（哪些 session、第几个 window、叫什么名字、对应哪个 git repo、三格朝向），崩溃后按快照重建结构——不保存运行进程，agent 需手动 `agent -r` 续。

- `prefix s`：立即存档当前所有 workspace（跳过单 pane window 和非 git 目录），写入 `state/workspaces/snapshots/<时间戳>.tsv` 并更新 `last` 指针。
- 后台每 180 秒由 launchd 定时器自动存一次（内容无变化则跳过，最多保留最新 3 个）。
- `prefix r`：在**干净 tmux**（恰好 1 session/1 window/1 pane）上恢复 `last` 快照；非干净环境会拒绝以免污染。
- `prefix g`：fzf 弹窗列出历史快照，选一个恢复。
- **`tmux-resume`（终端命令）**：server 崩溃后人在裸终端、进不了 tmux 时的主入口。直接在终端 fzf 选快照 → 自动建干净 session → 重建 → attach。

恢复时每个 repo window 会重建 ai/git/run 三格并在 git 格起 lazygit；ai 格留空，由你 `agent -r` 续上崩溃前的对话。

完整说明（含测试步骤、故障排查）见 [GUIDE_WORKSPACE.md](./GUIDE_WORKSPACE.md)。

## 4. 创建和进入 session

创建一个新 session：

```bash
tmux new -s session-name
```

推荐用项目名作为 session 名，例如：

```bash
cd ~/.hat-config
tmux new -s hat-config
```

查看所有 session：

```bash
tmux ls
```

进入已有 session：

```bash
tmux attach -t session-name
```

如果已经在 tmux 里，切换到另一个 session：

```bash
tmux switch-client -t session-name
```

## 5. 离开和恢复 tmux

离开当前 tmux session，但不关闭它：

```text
Ctrl-s
d
```

这叫 detach。detach 后，session 仍然在后台运行。

稍后恢复：

```bash
tmux attach -t session-name
```

## 6. 管理 window

window 类似标签页。

查看所有 window 并选择：

```text
Ctrl-s
w
```

切到下一个 window：

```text
Ctrl-s
n
```

切到上一个 window：

```text
Ctrl-s
p
```

重命名当前 window：

```text
Ctrl-s
,
```

删除当前 window：

```text
Ctrl-s
x
```

tmux 会要求确认，按 `y` 删除。

tmux 默认的 `prefix &` 也仍然可用，但本项目推荐用更顺手的 `prefix x`（带 y/n 确认）。

直接用命令删除某个 window：

```bash
tmux kill-window -t session-name:window-index
```

例如：

```bash
tmux kill-window -t hat-config:2
```

## 7. 管理 pane

pane 是一个 window 里的分屏区域。

进入 copy-mode，使用键盘滚动当前 pane：

```text
Ctrl-s
v
```

进入后可用：

```text
j / k      下/上移动一行
Ctrl-d     向下半页
Ctrl-u     向上半页
PageDown   向下翻页
PageUp     向上翻页
g          到顶部
G          到底部
q          退出 copy-mode
Esc        退出 copy-mode
```

不用手动进入 copy-mode 的快速滚动，推荐用：

```text
Alt-u      向上半页
Alt-d      向下半页
```

在 macOS 上，`Option-u` 可能是 dead key，`Option-d` 可能输入 `∂`。如果 Alt/Option 版本不稳定，先 `prefix v` 进 copy-mode，再用 `Ctrl-u`/`Ctrl-d` 翻半页。（注意：`prefix d` 是 detach-client、`prefix u` 未绑定，不要用来滚动。）

在 pane 之间移动：

```text
Ctrl-s
h / j / k / l
```

方向对应：

```text
h = 左
j = 下
k = 上
l = 右
```

也可以不用 prefix，直接按：

```text
Alt-h
Alt-j
Alt-k
Alt-l
```

如果你的终端没有把 Option 当作 Meta，按 `Alt-j` 时可能会输入 `∆`。本项目也绑定了 macOS 常见 Option 字符：

```text
˙ = 左
∆ = 下
˚ = 上
¬ = 右
```

更彻底的做法是在终端软件里把 Option/Alt 配置为 Meta 或 Esc+。如果你之后换终端或配置 profile，可以优先用这种方式。

也可以用鼠标直接点击 pane 切换焦点。tmux mouse mode 已开启，在 iTerm2 中还可以拖动 pane 边界调整大小。

删除当前 pane：

```text
Ctrl-s
Shift-x
```

`prefix X`（kill-pane）不带确认，直接删除当前 pane。

临时放大当前 pane：

```text
Ctrl-s
z
```

再按一次 `Ctrl-s z` 恢复。

显示 pane 编号：

```text
Ctrl-s
q
```

显示编号后，可以按对应数字跳转 pane。

## 8. 删除 session

删除某个 session：

```bash
tmux kill-session -t session-name
```

例如：

```bash
tmux kill-session -t hat-config-vertical
```

删除当前 session：

```text
Ctrl-s
:
```

然后输入：

```tmux
kill-session
```

删除所有 tmux session 和 server：

```bash
tmux kill-server
```

注意：`tmux kill-server` 会关掉所有 tmux session，慎用。

## 9. 清理测试窗口和 session

如果你只是想删除刚刚用 `prefix [` 或 `prefix ]` 创建的测试 window：

```text
Ctrl-s
w
```

选择要删除的 window，进入后：

```text
Ctrl-s
&
y
```

如果你想删除整个测试 session，先查看：

```bash
tmux ls
```

然后删除：

```bash
tmux kill-session -t session-name
```

## 10. 常用组合流程

### 创建一个项目工作区

```bash
cd /path/to/project
tmux new -s project-name
```

### 创建 AI 工作窗口（auto 朝向）

```text
Ctrl-s
]
```

直接在当前目录建窗，名字自动。要建在别的目录就改按 `prefix [`，在弹出的 popup 里用关键字从 z 历史选目录（回车用当前目录，无匹配时直接输完整路径）。

朝向按窗口尺寸自动定，并随 resize 自动切换。需要固定横/竖改 **Settings → General → Default layout**。

### 打开 VS Code

```text
Alt-Shift-C
```

备用入口：

```text
Ctrl-s
Enter
```

### 查看 tracker

```text
Ctrl-s
Shift-t
```

### 离开 tmux，稍后回来

离开：

```text
Ctrl-s
d
```

回来：

```bash
tmux attach -t project-name
```

## 11. 键位定制（keymap.conf）

`scripts/setup` 的键位模块可以按你的终端 / 插件冲突情况调整核心键位，产物是一个
gitignore 的 override 文件 `private/keymap.conf`。`tmux/tmux.conf` 末尾有一行固定挂载：

```tmux
# private overlay keymap（不存在时静默跳过）
source -q ~/.hat-config/private/keymap.conf
```

因为用的是 `source -q`，override 文件不存在时静默跳过，用默认键位。

### 三个预设

向导第一层让你选 preset：

- **default**：不改动任何键位，**不生成 override**（这是保守默认）。
- **compat**：避让常见插件冲突——把 `F12`（passthrough）挪到 `F10`、`M-s`（palette）挪到 `M-a`。
- **minimal**：只保留 `M-s` palette 入口，其余核心键（新建窗口 / timer / window nav /
  workspace / pane 移动 / session 切换等）全部解绑。

选 compat/minimal 后向导还会扫描你现有的 `~/.tmux.conf` 找冲突键、允许逐条改键。

### 键位语法白名单（注入防护）

自定义键值只接受严格 grammar 白名单，**不含 `;`（tmux 命令分隔符）和 `\`（转义）**：

```text
^([A-Za-z0-9]|C-[a-z0-9]|M-[A-Za-z0-9]|S-[A-Za-z]|F[1-9][0-9]?|Enter|Space|Tab|Escape|[][|,.<>/-])$
```

`bind` 行始终由模板生成器产出，绝不把用户输入直接拼进 tmux 命令，故 `\;` 类复合绑定
明确 out of scope。写入前还会在随机 socket 的隔离 tmux server 上**沙盒试载**完整
`tmux.conf + keymap.conf`，语法坏了自动回滚。

### 恢复默认

**删除 `private/keymap.conf` 后重新部署**（`scripts/deploy.sh update --yes` 或重跑
`scripts/setup`）即恢复全部默认键位——挂载行 `source -q` 找不到文件就静默跳过。

## 12. 排查快捷键没有反应

确认是否在 tmux 里：

```bash
echo "$TMUX"
```

如果输出为空，说明当前 shell 不在 tmux 中。

确认本项目是否已部署：

```bash
~/.hat-config/scripts/deploy.sh status
```

重新部署：

```bash
~/.hat-config/scripts/deploy.sh update --yes
```

确认 prefix：

```bash
tmux show-options -g prefix
tmux show-options -g prefix2
```

预期：

```text
prefix C-b
prefix2 C-s
```

确认本项目快捷键：

```bash
tmux list-keys -T root M-C
tmux list-keys Enter
tmux list-keys A
tmux list-keys S
tmux list-keys T
```

如果绑定存在但按键没有可见反馈：`prefix ]` 是立即建窗、无提示；`prefix [` 会弹出一个 popup 让你输入目录，若 popup 没出现，检查 tmux ≥ 3.2（`display-popup` 支持）与脚本可执行权限。

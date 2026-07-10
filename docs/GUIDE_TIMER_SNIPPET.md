# Timer 面板 + Snippet 内容库使用指南

这份文档讲两个相互关联的模块：

- **Snippet 内容库**：可复用文本片段库（命令、提示词、模板），随手粘贴或挂定时。
- **Timer 面板**：给 tmux window 设延时/定点输入的定时任务，跨窗管理。

二者共用同一套内容：timer 可以把历史「存进 snippet 库」，也可以「从 snippet 库取内容」建 timer。

---

## 一、Snippet 内容库

### 是什么

一个按目录分组的文本片段库。每条片段可以：

- **立即粘贴**到当前 pane（`Alt-s` → Paste snippet）；
- 被 **timer 面板取用**（建 timer 时 `Ctrl+P`）。

### 存储位置与格式

存在 `~/.hat-config/snippets/` 目录（随 Syncthing 跨设备同步、可 git 版本化）：

- **子目录 = 分组**：`snippets/git/`、`snippets/timer/` …；根目录下的文件归 `ungrouped`。
- **每个文件一条片段**，格式：

  ```
  # 一句话描述（可选，首行以 # 开头）
  正文从第二行开始
  可以多行
  用 {{变量名}} 表示占位
  ```

- **收藏**记录在 `~/.hat-config/snippets/.favorites`（每行一条相对路径 `分组/名字`），不污染片段文件本体。
- **内置 `timer` 分组**：timer 相关内容的默认归处。

### 打开与导航（hjkl 约定）

`Alt-s` 打开 agent palette → 选「Paste snippet」。面板按分组**可折叠**显示，`★ favorites` 组置顶。

| 键 | 作用 |
|---|---|
| `j` / `k` | 上下导航 |
| `l` / `Enter` | 组标题=展开/折叠；片段行=粘贴（有 `{{var}}` 先逐个填值）|
| `a` | 新建片段 |
| `e` | 编辑选中片段 |
| `x` / `d` | 删除选中片段（确认）|
| `s` | 收藏 / 取消收藏（进/出 `★ favorites` 组）|
| `f` | 进入搜索（搜片段名/描述/正文）；搜索中 Enter 留词、Esc 清词 |
| `h` / `Esc` | 返回 |

### 新建片段的两种方式

**A. 面板内新建（推荐，不用记路径）**

`Alt-s` → Paste snippet → 按 `a`，填四个字段：

- **Name**：文件名（不能含 `/`、不能以 `.`/`_` 开头）
- **Group**：分组（= 子目录；留空 = ungrouped）
- **Description**：列表里显示的一句话
- **Content**：正文（**Enter = 换行**，可多行；`{{var}}` 占位）

`Ctrl+S` 保存。

**B. 手动建文件（适合批量/脚本化）**

```bash
mkdir -p ~/.hat-config/snippets/git
printf '# 切分支并拉取\ngit checkout {{branch}} && git pull\n' \
  > ~/.hat-config/snippets/git/checkout-pull
```

之后面板里 `git` 分组下就会出现 `checkout-pull`，粘贴时提示填 `branch`。

### 变量 `{{var}}`

正文里的 `{{name}}` 是占位符。粘贴（或被 timer 取用）前会逐个提示输入，填完替换后再粘贴/回贴。同名变量只提示一次。

### 两种消费模式（内部机制）

- **粘贴模式**：`Alt-s` 进入时，`Enter` → `tmux send-keys -l` 把（替换变量后的）内容打到当前 pane，然后关闭 palette。
- **返回模式**：timer 面板 `Ctrl+P` 进入时，`Enter` → 把内容**回贴到 timer 的 Content 字段**，不直接粘贴。

---

## 二、Timer 面板

### 时间基准

Timer 的墙上时间默认使用**东八区（UTC+8）**。可在 `Alt-s → Settings → General → Timer timezone` 修改：按 `Space` 选择 `auto`（跟随系统时区），或按 `Enter` 输入 IANA 时区名 / UTC offset（如 `Asia/Shanghai`、`UTC+8`、`+08:00`）。

- `13:10` 表示东八区当天或次日 `13:10`；
- `daily` 按东八区自然日计算；
- 面板 `Next`、新增提示和 Window Nav 的 timer 时间均按当前 Timer timezone 显示；
- `5m`、`1h30m` 等相对时长以及 `reset` 的绝对额度重置时刻不受时区换算影响。
- 修改时区后，已有的 `HH:MM` / `daily` timer 会立即按新时区重排；duration 和 `reset` timer 保持原触发时刻。

### 打开

| 键 | 作用 |
|---|---|
| `prefix t` | 打开**本 window** 的 Timer 面板（独立弹窗）|
| `prefix T` | 打开 Todo 视图（原 `prefix t`）|

> 也可在 `prefix w`（window 导航）里对某 window 行按 `t` 打开那个 window 的 timer 面板（内嵌）。

### 三栏布局（仿 todo，一屏同显）

打开即同时看到三栏，`Tab` 切换**焦点栏**（标签高亮），按键作用于焦点栏：

```
┌ This Window ─────────┬ History ─────────┐
│ 当前 window 的 timer   │ 本 window 用过的    │
├ Other Windows ───────┤ timer 内容（历史）  │
│ 其它 window 的 timer   │                   │
│ (状态/名字/目录)        │                   │
└──────────────────────┴───────────────────┘
```

`Esc` / `g` 退出弹窗。

#### This Window 栏

当前 window 的 timer 列表。

| 键 | 作用 |
|---|---|
| `j` / `k` | 导航 |
| `Enter` / `Space` | 启停该 timer |
| `a` | 新建 timer |
| `r` | 一键建「额度重置后输 `continue`」timer（trigger=reset、Send Enter on、一次性；已有同款则跳过）|
| `e` | 编辑 |
| `x` | 删除（确认）|

**Add / Edit 表单字段**：

- **Content**：到点要打进 ai pane 的文本
- **Trigger**：`5m` / `1h30m` / **`3h20m`**（复合时长）/ `30s` / `1d`，或 `13:10`（定点 HH:MM）；裸整数 = 分钟；**`reset`**（或 `quota`）= 在本窗 AI 客户端**额度重置后**触发（见下）
- **Loop**：`none`（默认）/ 时长（如 `5m` 周期）/ `daily`（定点每天）；`reset` 触发的 Loop 只接受 **`reset`**（每次额度重置都触发，Max exec 依然封顶）或留空（一次性）
- **Max exec**：最大执行次数（`0` = 无限）
- **Send Enter**：执行后是否补一个回车（`Space`/`y`/`n` 切换）
- **Auto del**：**完成后自动删除**（默认 off）——一次性 timer 用完即删；循环 timer 到 Max exec 后删；`Space`/`y`/`n` 切换

表单内 **`Ctrl+P`** 打开 snippet 选择器（定位 `timer` 分组），选一条内容回贴到 Content。**`Ctrl+S` / `Alt+Enter`** 在任意字段直接提交（Cmd+Enter 进不了终端 TUI——默认被 Ghostty 占用为全屏切换；想用需在 Ghostty 里把 `cmd+enter` 映射成 `text:\x1b\r`）。

#### Other Windows 栏

聚合**其它 live window** 的 timer，每行带该 window 的：

- **状态图标**：`●` busy / `○` idle / `?` asking / `L` limited（额度满）（源自窗口名 `[B]`/`[I]`/`[?]`/`[L]` 前缀）
- **窗口名字** 和 **目录**（`@agent_dir`）

`Tab` 把焦点切到这栏后，`Enter` 启停 / `e` 编辑 / `x` 删除都作用于所选 timer **所属的 window**（跨窗管理）；`v` 把所选 timer **复制到当前 window**（trigger/loop/max/sendEnter/auto-del 原样克隆）。**已结束（tmux 中已不存在）的 window 的 timer 不显示**。

#### History 栏

**全局** timer 历史模板库：所有 window 用过的 timer 内容跨窗口合并展示（按 `内容+trigger+loop+max+sendEnter` 组合去重、最近使用在前），**持久化**——timer 删了、window 关了、tmux server 重启（window id 变了）历史都还在。

| 键 | 作用 |
|---|---|
| `Enter` | 用该历史**预填新建表单**（可改完再提交）|
| `v` | **直接复制**为当前 window 的 active timer（不经表单）|
| `s` | ★ 把它**存入 snippet 库**（弹窗填名字，默认分组 `timer`）|
| `x` | 删除该历史项（同组合跨窗口一并删除）|

### 触发机制

最多每 5 秒轮询所有 timer（挂在 `agent tmux sync-names` 周期心跳上），到点就向该 window 的 ai pane `tmux send-keys` 内容（可选回车）；按 Loop / Max 决定是否再排下次或停用。一个「其它 window 的 timer」始终 fire 到它**自己的** window，不受你在哪看面板影响。

**注入前清场**：fire 时若 pane 屏幕上正显示 Claude 的额度弹窗（usage limit dialog，会吞掉注入的按键），先发一个 `Escape` 关掉再打字。只在「屏幕匹配到额度弹窗文案且会话非 busy」时才发 Esc——busy 时发 Esc 会打断正在生成的 turn，绝不盲发。

### `reset` 触发：额度重置后自动开始

Trigger 填 `reset`（或 `quota`），提交时从磁盘探测本窗 AI 客户端的额度重置时刻，把触发点定在**重置后约 90 秒**：

- **Codex**：读该窗会话 rollout JSONL 最新一条 `token_count` 事件的 `rate_limits.{primary,secondary}.resets_at`（绝对 epoch，权威）。有用量 ≥95% 的窗口就等它们全部重置（取最晚），否则取 5h 窗口边界。
- **Claude**：磁盘上只有**撞限之后**写进 session JSONL 的 429 记录（`"resets 12:40am (America/Los_Angeles)"` 人类可读文案），解析出下一次该时刻。

**三级调度：确切 → 保底 → 休眠**（Claude 侧）：

1. **确切**：撞限后 session JSONL 里的 429 文案给出精确重置时刻，触发点 = 该时刻 +90s（防服务端延迟）。
2. **保底**：还没撞限时，读 statusline 落盘的 **rate_limits 缓存**（`state/agent-tracker/claude-rate-limits.json`，由本项目自建的 statusLine 脚本 `tmux/scripts/claude_statusline.sh` 每次渲染状态栏时写入，来自 Claude Code 注入的第一方 five_hour/seven_day `resets_at`；有缓存滞后，恰好偏保守）。触发点定在保底时刻 +90s，timer 标记 `quota_fallback`。
3. **休眠**：两者都拿不到（缓存也没有）→ 休眠态（Next 列 `--`）。

**唤醒/升级**：之后**任一窗口**的 Claude 撞限（额度是账号级的），最多每 5 秒的周期轮询把所有「休眠或保底态」的 `reset` timer 统一**升级为确切重置时刻 +90s**。所以可以提前布防（每个工作窗按 `r` 放一个 continue），撞限时自动接管。

**Loop = `reset`（每次额度重置）**：触发一次后重新走「确切→保底→休眠」调度，等下一轮；**Max exec 依然生效**（到次数即停用/删除）。适合长期挂着的「撞限自续」窗口。

典型组合：`prefix t` → `r` 一键（等价 `a` 新建 → Content `continue` / Trigger `reset` / Send Enter on）；想每次撞限都自动续，Loop 填 `reset`；不想留记录加 Auto del。

### `[L]` limited 状态（Claude 额度满）

Claude 会话最近一个 turn 死在 429 rate_limit 且重置时刻未到时，窗口名前缀变 **`[L]`**（取代 `[I]`/`[?]`），Window Nav 显示红色 `L` activity 图标；daemon 侧视同 asking，首次未读时抬起 🔔 + 通知并进入「需要处理」。用户进入看过后 acknowledge 清掉 🔔，窗口即离开「需要处理」，但额度恢复前仍保留 `L` 状态。重置时刻同时写进窗口选项 `@agent_limit_reset_at`（epoch 秒），可供脚本读取。重置时刻一过（或新 turn 跑通）自动恢复正常状态。

---

## 三、两个模块的联动

```
建 timer ──Ctrl+P──▶ 从 snippet（timer 分组）取内容 ──回贴──▶ Content
   │
   └─历史─▶ s ★ ──存入──▶ snippet 库（timer 分组）──下次可被 Ctrl+P 取用
```

常用的定时指令存进 `timer` 分组一次，之后建 timer 直接 `Ctrl+P` 取，不用重打。

---

## 相关代码

改这两块前读 `docs/ARCHITECTURE.md`。关键文件：

- `agent-tracker/cmd/agent/snippet.go` — snippet 数据层（分组/收藏/CRUD/接口）
- `agent-tracker/cmd/agent/snippet_panel.go` — snippet 面板 UI
- `agent-tracker/cmd/agent/window_timer.go` — timer 数据 + 历史 + 触发
- `agent-tracker/cmd/agent/window_timer_panel.go` — timer 三栏面板 UI
- `tmux/scripts/open_window_timer.sh`、`tmux/tmux.conf`（`prefix t`/`prefix T`）

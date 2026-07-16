# Architecture — tmux + agent-tracker 全栈

本文档面向**维护者**：改 `agent-tracker/`（Go）或 `tmux/` 这套代码前先读本文，改完同步更新。
使用向导见 `docs/GUIDE.md` / `docs/GUIDE_TMUX.md`，快速上手见 `README.md`。

## 三层架构

```
┌─ Claude Code ──────────────┐   上报：sessions-json 轮询（+ Stop hook 抓 session id）
│  tracker-mcp (MCP server)  │ ──────────────┐
│  claude_report.sh (hook)   │               │  Unix socket
└────────────────────────────┘               ▼
                                  ┌─ tracker-server (launchd daemon) ─┐
   palette / TUI ───socket───────▶│  内存维护任务状态（in_progress /  │
   (agent palette)                │  completed/acknowledged），广播     │
                                  └───────────────┬───────────────────┘
                                                  │  agent tracker state（JSON）
   tmux ─────hooks/status─────────────────────────▼
   status bar（session tabs + ⏳/🔔 图标）、pane-focus-in/after-select-window
   调 `agent tmux on-focus`（拼窗口名）+ `agent tracker command acknowledge`
```

三个 Go 二进制（`agent-tracker/cmd/`）：

- **tracker-server** — 常驻 daemon。监听 Unix socket，处理 `start_task`/`finish_task`/`update_task`/`mark_asking`/`acknowledge`/`delete_task`/`notify`/`notifications_toggle`/`set_notification_group_mode`。任务状态在**内存**，经 socket 回 `agent tracker state` 的 JSON 暴露，不持久化（旧的 `agents.json` workspace registry 已随 agent-workspace 重机制一并剔除）。
- **tracker-mcp** — MCP server，给 Claude 暴露 `tracker_mark_start_working` 等 tool，转成 socket command 发给 daemon。
- **agent** — 多用途 CLI/TUI：`agent tracker command <sub>`（发 socket 命令）、`agent tracker state`（查状态）、`agent tmux on-focus`（聚焦时拼窗口名+rename）、`agent tmux sync-names`（状态栏每 3 秒发轻量触发、内部把完整轮询限流为 5s，把所有 agent 窗口名同步成各自主 AI pane 的 Claude/Codex 会话标题）、`agent palette`（bubbletea 工作空间总览）、`agent tmux right-status`（状态栏右侧渲染）、`agent ime switch`（cgo 调 Carbon `TISSelectInputSource` 切输入源到 ABC，由 tmux prefix 的 root-table 绑定调用，见 `cmd/agent/ime_darwin.go`；不连 daemon 的快路径）。

## 数据流

**启动**：`agent`（shell 启动器 `tmux/scripts/agent`）→ `$TMUX` 未设则 new-session+attach，已设则 new-window → fzf 选 client+provider（回车=默认 Claude）→ 写 `@agent_client`/`@agent_provider` + 三 pane(ai/git/run) → ai pane 起客户端。

**状态同步**：不走 Claude hook，由 `sync-names` 周期轮询 agent 运行时状态驱动。status bar 每 3 秒刷新，完整 sync 按 poll interval（默认 3s）限流；焦点/切窗 hook、以及**事件驱动**（见下「事件驱动即时刷新」）可即时突破该节奏触发：
- Claude：读 `~/.claude/sessions/<pid>.json` 的 `status`（busy/idle/asking/waiting/paused/**shell**，Claude Code 实时写入）。
- Codex：从 pane 进程树识别 `codex` CLI，并用一次批量 `lsof` 建立各 Codex PID 当前打开的 `rollout-*.jsonl` 映射；沿 pane 进程树收集 rollout 后，在 `~/.codex/state_5.sqlite` 中只匹配 `source='cli'` 的根 thread（排除同进程打开的 subagent rollout），因此同 cwd 的多个 Codex window 仍各自绑定正确会话。仅在 `lsof` 不可用或启动初期尚未打开 rollout 时回退到按 pane cwd 查询最新 CLI thread。随后读取该 thread 的 `rollout_path` JSONL；`task_started` 后未见终止事件视为 busy，`task_complete`、`turn_aborted` 或 `thread_rolled_back` 视为 idle（Code editor 操作可能以 abort/rollback 结束 turn，不一定写 `task_complete`）。状态解析会跳过超过 1MB 的单条大 payload（通常是 tool output），继续读取后续小型状态事件，避免超长 JSONL 行让扫描提前停在 busy。`asking` 只由需要用户动作的信号推断：尚未收到相同 `call_id` 的 `function_call_output` / `custom_tool_call_output` 的显式 `request_user_input` tool call，或未完成 turn 的最后有效事件停在 tool call 且超过 `codexToolCallQuietWindow`（5s，常见于权限审批弹窗等待 `function_call_output`）。显式提问的 pending 集合会在匹配输出、新 `task_started` 或终止事件到达时清除，避免 rollout 中已解决的历史提问把会话永久钉在 asking。普通 assistant/commentary 消息之后仍可能继续 reasoning 或调用工具，静默时长不代表等待用户，始终保持 busy。例外：当前 `turn_context.approvals_reviewer=="auto_review"` 时 generic tool call 由自动审批接管，静默期间也保持 busy；尚未收到对应输出的显式 `request_user_input` 仍始终视为 asking。Codex 的最终 turn 错误不一定写 rollout 终止事件，因此还按 root thread 查询 `~/.codex/logs_2.sqlite` 中 `level=INFO`、`target=codex_core::session::turn` 的最新 `Turn error:`；该错误晚于最后真实模型进展时覆盖为 error。更晚的 `task_started`、user message、reasoning、assistant message 或模型 tool call 才解除 error；`token_count`、`task_complete`、world state、subagent 消息和单独 tool output 不会把失败伪装成成功。查询按 `(thread_id, ts)` 索引并在单轮 sync 内缓存；数据库缺失、schema 不兼容或查询失败时退回 rollout 状态。
- 窗口名加 `[B]`(busy)/`[I]`(idle)/`[?]`(asking)/`[L]`(limited)/`[E]`(error) 实时前缀（`statusTag`）。`error` 无 spinner、停止刷新 last-busy，但 daemon task 保持 `in_progress` 并以独立 attention kind 抬 🔔/错误通知；ack 只清未读，真实恢复前 `[E]` 保留。**limited（额度满，Claude + Codex 均支持）**：非 busy 时才检查——Claude 看会话 JSONL tail 的最近 turn 事件是否 429 rate_limit 且解析出的重置时刻未到（`cmd/agent/quota.go` 的 `claudeLimitResetFromJSONL`）；Codex 看该 thread rollout 最新 `token_count` 的 `rate_limits.{primary,secondary}` 是否有窗口 `used_percent>=95`（`codexLimitResetFromMeta`→`codexExhaustedResetAt`，与 `pickCodexReset` 共用同一耗尽判定、但不回退到未耗尽窗口的边界，避免和「只是估算下次重置」的 reset timer 语义混淆）。命中时把重置 epoch 写窗口选项 `@agent_limit_reset_at`（reconcile 复用、脚本可读），恢复后清除。`limited` 优先于 generic error。**易错点**：Codex 会为不同 `limit_id`（如 `codex` 与 `premium`）各写一条 `token_count` 快照，未被计量的类别 `primary`/`secondary` 均为 null——取 rollout tail 最新快照时必须跳过这类 null 快照，否则会被其覆盖导致误判未耗尽（`codexRateLimitsFromRollout` 已做此过滤）。
- 与 daemon 对账（`reconcileTask` → 纯函数 `reconcileActions(metaStatus, daemonStatus)` 决定要发的命令、再逐条 `sendTrackerCommand`，映射可单测）：busy 且无进行中任务→`start_task`；busy→idle→`finish_task`；asking/limited/error 保持 `in_progress`，通过 `mark_asking` 的 `attention=asking|limited|error` 区分。daemon 的 completed/attention unread 经聚焦 `acknowledge` 清除，驱动状态栏 **🔔**。tab 🔔 图标由 daemon `reconcileWindowIcons` 预计算写进窗口选项 `@agent_icon`（本地任务铃 + 远端 `@agent_remote_bell` 同源折叠），`window-status-format` 原生展开、零子进程；busy 由窗口名 `[B]` 前缀表示、不出 ⏳。
  - **`shell` 活动态（HAT-515）**：Claude 结束一个 turn 但仍有**后台任务（`run_in_background`）或 subagent 在跑**时，sessions-json 写 `status:"shell"`（非 idle）。`reconcileActions` 对 `shell`（in_progress 时）发 `mark_asking(false)`——既清 asking、又在 daemon 侧作废待发完成（清 `PendingCompleteAt`），保持任务 in_progress；非 in_progress 则 no-op（不凭空造任务）。**这是「等待后台返回不应误发完成通知」的关键**：`shell` 不是 idle、不触发 `finish_task`。

**事件驱动即时刷新（daemon `cmd/tracker-server/{watcher,coalescer}.go`）**：固定 poll interval（3s+）对状态变化反映偏慢。两处机制让「状态变化 / 通知」突破固定节奏，且**防抖合并**避免高并发下每事件各刷一次：
- **检测（watcher）**：daemon 用 fsnotify 监听 `~/.claude/sessions/`（Claude 每次状态变更都改写 `<pid>.json`）。**关键：不对每个写事件盲触发**——Claude 在 busy 时约 8 次/秒心跳式改写该文件（只动 `statusUpdatedAt`），watcher 读改动文件的 `status` 字段、**仅在状态值真正变化时**才触发（`sessionStatusOf` + 每文件 `lastStatus` 去重）；长 turn 里 status 恒 busy → 零触发，turn 结束 busy→idle → 触发一次。触发经 `detectCoalescer`（window 400ms）合并后 exec `agent tmux sync-names --wait`。
- **`--wait`（事件路径专用阻塞锁）**：普通 periodic/nav 触发用非阻塞 flock（撞到在跑的 sync 直接丢弃，由那次覆盖）；但事件触发若在 sync **进行中**落下，那次 sync 读的是**变化前**的状态、会漏掉本次变化 → 要等下个 periodic（~3s）。故事件路径传 `--wait` 用**阻塞 flock**：等在跑的那次结束再跑、读到新值，保证亚秒反映（实测状态跳变 300–400ms 内反映）。
- **渲染合并（coalescer）**：daemon 每个 reconcile 命令原本各起 goroutine 各跑一次 `broadcastState`（推 palette UI）+ `refresh-client -S`（重渲 status bar），一次 sync 改多个窗口 → N 次 fork tmux。改由 `broadcastCoalescer`/`refreshCoalescer`（window 150ms）合并：**leading-edge 立即触发 + 冷却窗内的事件合并成一次 trailing**，突发 N 事件 → 最多 1 次/window。`coalescer` 是纯状态机、带单测。
- 3s periodic 保留作兜底；事件驱动只加即时性、**空闲零开销**（无写事件→无工作）。
  - `finishTask` 把任务标记 completed 时**清除 `Asking` 标志**（已完成不再等待输入），否则上一个停在权限确认处退出的 agent 会给同 pane 复用的 task 留下残留 `asking=true`，把 Window Nav 状态钉成 `?`、状态栏钉成 🔔。Window Nav 面板也只在 `status==in_progress` 时才让 `asking` 覆盖窗口名前缀的实时状态。
  - `acknowledge` 与 finish 自动 ack 均为 **window 级**（非 `isActivePane`）：🔔 按 window 渲染，聚焦窗口内任意 pane（含 git/run pane，而 task 挂在 ai pane）都应清掉该 window 下所有 task，避免焦点不在 ai pane 时 🔔 不灭。Window Nav 的「需要处理」只看这份未读 🔔；ack 后即使窗口 activity 仍是 `asking`/`limited`（`?`/`L`），也回到普通分组。finish 自动 ack 用 `windowIsBeingWatched`（window 选中 **且** 终端前台），见下「易错点」通知小节。
- 好处：无需 settings.json hook、无 `$TMUX_PANE` 依赖、不靠 AI 自觉调 MCP。原 Notification→notify hook 已移除（冗余且无 summary 静默失败）；Stop→finish_task hook 保留仅因 `claude_report.sh` 顺带抓 Claude session id 供 restore（见下）。

**client/provider 实时**：检测到主 AI pane 有 Claude 会话时 client 推断为 `claude`，provider 从 claude 进程的初始 env `ANTHROPIC_BASE_URL`（`ps eww` 读，映射 `~/.hat-env/providers/*.env`）实时反查（`providerForPID`）——退出重进换 provider 名字跟着变。

**命名锚点**：窗口名锚定**主 AI pane（`@agent_pane_role=ai`，回退 active pane）那个 AI 会话的标题**。Claude：`window_naming.go`/`claude_session.go` 经 `pane_pid` 进程树找到 claude 进程 pid，读 `~/.claude/sessions/<pid>.json` 的 `name`。Codex：识别 pane 子进程里的 `codex` CLI，以进程打开的 rollout 精确匹配 `threads.title` 和 `model`，而非仅按 cwd 取最新 thread。自动标题在**数据层截断到 100 rune**（`truncateWindowTitle`，尾加 `…`，按 rune 计数不切碎 CJK），之后才有显示层的进一步限宽：状态栏 `window-status-format` 用 `#{=/24/…:window_name}` 限宽，Window Nav 按列宽自适应截断。**数据层这道截断是内存安全边界，不是美观选择**——显示层的 `#{=/24/…:window_name}` 救不了内存：tmux 必须先把完整 `window_name` 展开成字符串才能截到 24 字符，每次状态栏重绘 + 每次 sync-names 都按全长分配一次，而 tmux 3.6b 不释放这些分配。Codex 用**整个 prompt** 当 session title（`liveTitle = codexMeta.Title`），`prefix ]` 的标题框也接受粘贴，所以超长标题是常态而非边缘情况：实测一个 6485 字节的窗口名让 tmux server 以 ~6MB/min 增长（两天堆到 6.8GB，heap 里 47 万个 ~14KB 块）。改这里前先读本段。有标题→`项目名·客户端·标题`；无标题→回退 `composeWindowName`（`项目名·客户端#编号`）。未设 `@agent_client`（没走启动器）时，若探测到主 pane 有活的 Claude/Codex 会话则推断 client；无 AI 会话的窗口不改名。触发：①事件即时——`pane-focus-in`/`after-select-window`/`client-session-changed` hook 跑全量 `sync-names`；②被动跟随——状态栏每 3 秒调用 `sync-names --periodic`，内部按 5s 限流。所有入口共享内核 `flock` single-flight：已有 sync 在跑时，新触发直接 coalesce，不启动第二个 worker；进程退出/被 kill 时锁由内核自动释放。`ps`/`lsof`/`sqlite3`/`tmux` 等外部查询均有 3s timeout，单轮另有 20s deadline，避免单次异常永久占用。`sync-names` 每次复用同一份 `ps`、Codex `lsof` 和 session/thread 状态索引，`rename` 仅在名字变化时执行；读取缺省 `@agent_*` option 使用 `show-options -q`，不再把预期的未设置状态写成 status-right 错误。

**原始 prompt 查看**：Window Nav 选中 AI window 后按 `p`，即时按主 AI pane 定位会话并读取第一条 user prompt：Claude 读 `~/.claude/projects/<cwd-slug>/<sessionId>.jsonl`，Codex 读 `~/.codex/state_5.sqlite` 的 `rollout_path` JSONL。详情页按 `c` 调 `pbcopy` 复制。完整 prompt 不持久化到 tmux option，只在查看/复制时从本地 session 记录读取。

**命名/ack**：`pane-focus-in`/`after-select-window` hook **分别**调用两件独立的事：
- `agent tmux on-focus`（本地 CLI，`agentWindowName` 拼名后按需 `rename-window`）。
- `agent tracker command acknowledge`（→ daemon 标记已读，🔔 消失）。

## 关键文件

| 文件 | 职责 |
|---|---|
| `agent-tracker/cmd/tracker-server/main.go` | daemon：socket 监听 + 任务状态 + 广播 |
| `agent-tracker/cmd/tracker-mcp/main.go` | MCP server；`resolveContext` 在 `tmux_id` 空时落 `autodetectContext` fallback |
| `agent-tracker/cmd/agent/main.go` | CLI/TUI 入口；命名拼装（`composeWindowName`/`splitSessionLabel`/`agentNameBase`/`applyOnFocusRename`） |
| `agent-tracker/cmd/agent/{claude_session,codex_session,window_naming,sync_names,ssh_window,orientation}.go` | 会话/状态读取与窗口命名，按职责拆分：claude_session=Claude session/JSONL 读取；codex_session=Codex rollout/SQLite；window_naming=`agentWindowName`/`agentAIPane`/provider·model 探测；sync_names=`agent tmux sync-names` 主循环 + `reconcileActions`；ssh_window=ssh 检测/透传抓手；orientation=布局朝向 reconcile |
| `agent-tracker/cmd/agent/tracker_cli.go` | `agent tracker command/state` 分发 |
| `agent-tracker/cmd/agent/ime_darwin.go` | `agent ime switch`：cgo + Carbon TIS 切输入源到 ABC（`ime_other.go` 为非 darwin 桩） |
| `agent-tracker/cmd/agent/palette*.go` | bubbletea 工作空间 palette |
| `agent-tracker/internal/paths/paths.go` | **路径单一来源**（state/socket/bin/config），见下「路径四分类」 |
| `agent-tracker/internal/{tracker,ipc}` | 任务模型 / socket envelope |
| `tmux/scripts/agent` | shell 启动器（fzf picker + 三 pane 布局） |
| `tmux/scripts/session_manager.py` | session 编号（`<index>-<label>`）增删/重排 |
| `tmux/scripts/{new_session,switch_session_by_index,move_session,...}.sh` | session 切换/移动/重命名/布局 |
| `tmux/scripts/claude_report.sh` | Claude hook → daemon 上报 wrapper |
| `tmux/tmux-status/{left,right,tracker_cache}.sh` | 状态栏渲染 + tracker 缓存（窗口/session 图标聚合 daemon 预计算的 `@agent_icon`） |
| `tmux/tmux.conf` | binding + status bar + daemon hooks（deploy 写入 `~/.tmux.conf` managed block） |
| `scripts/deploy.sh` | 构建/装卸全流程（见下） |
| `agent-tracker/app.hat-tmux-workbench.agent-tracker.plist.tmpl` | launchd LaunchAgent 模板（daemon） |
| `agent-tracker/app.hat-tmux-workbench.workspace-save.plist.tmpl` | launchd 定时器模板（workspace 周期自动存档） |
| `tmux/scripts/{build_agent_layout,save_workspace,restore_workspace,choose_workspace,tmux_resume}.sh` | workspace 存档/恢复（见下） |

## 路径四分类（`internal/paths/paths.go`）

迁移自参考仓库 `~/.config/agent-tracker` 时**按类分流，不无脑替换**：

| 类别 | 内容 | 去向 | git |
|---|---|---|---|
| 源码 | `*.go`、go.mod/sum、plist 模板、脚本 | `hat-config/agent-tracker/`、`tmux/` | 跟踪 |
| 二进制 | tracker-server/tracker-mcp/agent | `~/.hat-config/agent-tracker/bin/` | gitignore |
| 配置 | `agent-config.json` | 保留 `~/.config/agent-tracker/` | 视来源 |
| 运行时状态 | settings.json/缓存 + **socket** | `~/.hat-config/state/agent-tracker/` | gitignore |

- **socket 三处一致**：tracker-server / tracker-mcp / agent palette 都经 `paths.SocketPath()`——改路径只改这一处，否则三者连不上。
- socket 路径为 `~/.hat-config/state/agent-tracker/agent-tracker.sock`（`paths.SocketPath()` 基于 `StateDir()`）。参考实现原本由 `XDG_RUNTIME_DIR`/`os.TempDir()` 派生（grep 不命中字面常量），迁移时已统一收口到 `paths.go`。

## 构建 / 部署（`scripts/deploy.sh`）

`install` 子流程（**顺序关键**）：
1. `preflight`：检查 `go`/`fzf`（不依赖 brew）。
2. `build_binaries`：`go build` 三二进制到 `agent-tracker/bin/`。
3. `install_daemon`：**先** `mkdir -p` state 目录（launchd 不自动建 `StandardOutPath` 父目录），再 sed 替换 plist 占位（`__BIN__`/`__STATE__` → 绝对路径，**不留 `~`/`$HOME`**），写 `~/Library/LaunchAgents/`，`chmod 644`，`launchctl bootout||true` + `bootstrap gui/$UID`，启动后校验 `state = running`。
4. `register_claude`：`claude mcp add -s user tracker-mcp`（见下偏差）+ `claude_hooks_merge` 把 Stop hook 合并进 `~/.claude/settings.json`（`EVENTS` 当前只含 Stop；`MANAGED_EVENTS` 含历史管理过的事件如 Notification，install 时清理其残留）。
5. `install_workspace_timer`：装第二个 launchd 任务 `app.hat-tmux-workbench.workspace-save`（见下 Workspace 存档）。
6. `register_claude`：见上。
7. `register_alias`：幂等装入 `~/.hat-env/shared/alias-common` 的 `alias agent=...` 与 `alias tmux-resume=...`。
8. `write_managed_block`：tmux managed block → `~/.tmux.conf`。

`uninstall` 对称反向；`status` 报告 daemon 与 timer 状态。

### daemon 自愈 watchdog（维护必读）

daemon plist 配 `KeepAlive=true` + `RunAtLoad=true`：崩溃/被 `pkill` 都由 launchd 自动拉回（CLAUDE.md 旧称「tmux hook 自动重启」是误述，实为 KeepAlive）。**但 KeepAlive 救不回「job 被 `bootout` 整个移出 launchd」**——deploy 的 `bootout||true → bootstrap` 两步间被打断、或某次 bootstrap 静默失败，job 就从 launchd 消失、KeepAlive 一并失效，🔔/通知全哑且不会自恢复（曾踩坑：daemon 没了不自知）。

兜底由 `tmux/scripts/ensure_tracker_daemon.sh` 覆盖，挂在已有的**每 3 秒心跳**上：`tmux-status/tracker_cache.sh` 每 3 秒 `agent tracker state` 连 socket 刷 cache，**连不上（daemon 不可达）时**后台触发该 watchdog。watchdog 自带 30s 节流（`/tmp/agent-tracker-heal-<uid>` 的 mtime），`launchctl print` 判断 job 是否还在：不在则 `bootstrap` 重装、在但不工作则 `kickstart -k`。故 daemon 任何方式挂掉通常 ≤30s 内自恢复。**不引入新进程/timer**——复用状态栏每 3 秒 render 这条本就跑的心跳。

## Workspace 存档 / 恢复

崩溃保命机制，纯 bash + fzf，**不依赖 daemon/socket**（崩溃时 daemon 可能也挂了）。

- **快照格式**：`state/workspaces/snapshots/<时间戳>.tsv`，每行 6 列 `session_name⇥window_index⇥window_name⇥repo_root⇥layout⇥claude_session_id`（第 6 列可空；旧 5 列快照仍可恢复，读出第 6 列为空）。只存「是 git repo 且多 pane」的 window；`state/workspaces/last` 指向最近快照。
- **Claude session id 采集**：`claude_report.sh`（Stop hook）从 hook stdin JSON 取 `session_id`，按 ai pane 落盘到 `state/workspaces/claude-sessions/<pane_id>`（带 cwd 做 pane-id 回收守卫）。save 读出写进第 6 列；restore 若该列非空，对重建窗口的 ai 格 `send-keys -l`（字面、**不回车**）预填 `claude --resume <id>`，用户确认后自行 Enter 续接。
- **`tmux/scripts/save_workspace.sh [--auto|--stdout]`**：遍历 `tmux list-windows -a`，过滤后写快照。`--auto` 静默、内容无变化跳过、保留最新 3 个，由 launchd timer 每 180s 调；`--stdout` 只把当前 live manifest 打到 stdout、不落盘（供 `tmux-resume -f` 比对）。第 6 列 claude session id 从 `claude-sessions/<pane>` 读，map 文件是单行 `sid<TAB>cwd`，**按 TAB 拆字段**（早先误用 sed 行号会把 cwd 粘进 sid，污染列分隔）。
- **`tmux/scripts/restore_workspace.sh [manifest]`**：守卫"恰好 1 session/1 window/1 pane"才执行，把当前 session 改名 scratch 占位，逐行重建 session/window，调 `build_agent_layout.sh` 重建三格，末尾 `switch-client` + 杀 scratch。**不重启 agent**。新 session 用真实 client 尺寸 `-x/-y` 创建——否则 detached 默认 80x24 下切好的三格 attach 后被 tmux 非比例 resize（多余列全给 ai pane），比例失衡触发 reconcile 反复 reflow（可见闪烁）。
- **`tmux/scripts/build_agent_layout.sh <window_id> <path> [mode]`**：从 `new_agent_window.sh` 抽出的三格 ai/git/run 切分核心（含 lazygit + `@agent_pane_role`），新建与恢复共用（等价参考方案缺失的 `project_layout.sh`）。`mode` ∈ `auto`(默认)/`landscape`/`portrait`；`auto` 调 `orientation_for_window.sh` 按窗口尺寸定朝向。建好后把 `@agent_orientation_mode`(模式) 与 `@agent_orientation`(实际朝向) 写到 window。

### 布局朝向：auto / 横向 / 纵向（reflow 机制）

每个 agent 窗口带两个 window 选项：`@agent_orientation_mode`(`auto`/`landscape`/`portrait`，缺省视为 auto) 与 `@agent_orientation`(当前实际朝向)。三 pane 各带 `@agent_pane_role`(ai/git/run)，是重排的稳定抓手。比例：ai 主 pane 占主轴 ~66%；landscape 右列 git/run 为 76%/24%；portrait 底部 git/run 为 73%/27%。

- **`orientation_for_window.sh <window_id>`**：按 `window_width`/`window_height` 输出 landscape/portrait（硬阈值 2.0：终端 cell 高≈宽2倍，宽≥高×2 为横向）。建窗与 auto reflow 共用，保证判定一致。
- **`reflow_agent_layout.sh <window_id> <portrait|landscape>`**：无损把三 pane 重排到目标朝向。按 role 找 ai/git/run，`break-pane -d` 把 git/run 摘成游离窗口(保进程)，再 `join-pane -l <比例>` 按目标朝向接回，更新 `@agent_orientation`，恢复 reflow 前 active pane，末尾调 `update_status_position.sh`。只对「恰好 3 个带 ai/git/run role 的 pane」动手。**不再按朝向幂等跳过**——是否需要重排完全由 reconcile 决定。**每窗口串行锁**（原子 `mkdir ${TMPDIR}/agent_reflow_<id>.lock`，trap 清理，>10s 陈旧锁可抢占）：拖拽 resize 时 `window-resized` 高频触发 reflow-focus，与每秒轮询的 reconcile 是各自独立进程，并发对同一 window 跑 break-pane/join-pane 会互相打断 → 反复摘下接回 → 持续闪烁。锁让后来者直接让位（exit 0）串行化，避免打架。
- **`new_agent_window_prompt.sh <here|ask> <path>`**（`prefix ]` / `prefix [`）：建窗统一入口。`here`=直接用当前目录、无输入（`]`）；`ask`=在 display-popup 里用 fzf 从 z 目录历史（`~/.z`，脚本内按 z.sh 同款 frecency 公式现算排序、过滤已删除目录，`--tiebreak=index` 保序）选目录，`--print-query` 让无匹配时回车直接用所输路径、Esc 取消；fzf 缺失回退 `read -e`（`[`）。名字都留空交自动命名，底层调 `new_agent_window.sh`。
- **reconcile（`orientation.go` 的 `reconcileWindowOrientation`）**：让 window 实际布局与配置一致。desired = pinned 时取 mode、auto 时取 `desiredOrientation`(滞回带 22/18 防抖)。重排条件：desired≠`@agent_orientation`，**或** desired 相等但 `layoutProportionsOK()` 不通过（ai 主 pane 占比不在 60–72%，捕捉恢复/resize 中途的偏小布局）。调用点：① 最多每 5s 的 `runTmuxSyncNames` 轮询；② `agent tmux reflow-focus --window <id>` ← `after-select-window`/`window-resized` hook（后台窗尺寸挂起，切到才 resize，故 focus/resize 时即时修正）。**trailing debounce（`reflowDebounceClaim`/`reflowDebouncePending`，450ms）**：双击标题栏全屏等拖拽会连发多次 `window-resized` → 多个 reflow-focus 进程顺序执行 → 4-5 次中间布局才落定。每个 reflow-focus 进程写唯一 token(`UnixNano`，存 `${TMPDIR}/agent_reflow_debounce_<id>`)→ sleep 450ms → 仅当自己仍是最新 token 才 reconcile（连发只有最后一个执行，且用落定后的实时尺寸）；轮询路径(①)在 `reflowDebouncePending` 为真（450ms 内有在途请求）时避让，不插队改中间态。与脚本内的每窗口串行锁互补：锁防"同时打架"，debounce 防"顺序多轮"。
- **建窗默认**：`prefix ]`/`prefix [`/`new_agent_window.sh`/`agent` 启动器都不再硬传 mode，由 `build_agent_layout.sh` 调 `agent tmux layout-default` 取 General 设置（默认 auto）。
- **status line 位置**（`update_status_position.sh`）：General `status_position`(auto/top/bottom) 决定；auto 跟随当前 active window `@agent_orientation`(竖→top/横→bottom)，非 agent 窗口回退 client 视觉宽高比。由 reflow/build 末尾、`after-select-window`/`window-resized`、`client-resized`/`client-attached` 触发，幂等 `set -g status-position`。
- **General 设置**（存 `agent-config.json`，`main.go` 的 `layoutDefaultSetting`/`statusPositionSetting`/`cycle*`，CLI `agent tmux layout-default`/`status-position` 供脚本读）：`layout_default`(建窗默认布局) 与 `status_position`(状态栏位置策略)。
- **`choose_workspace.sh`**（tmux 内 `prefix g` 走 display-popup）/ **`tmux_resume.sh`**（终端 `tmux-resume`，裸终端崩溃后入口，自建干净 session 再 restore 再 attach；`-f/--force` 先 `kill-server` 再走干净重建，kill 前用 `save_workspace.sh --stdout` 取 live manifest 与所选快照按第 1-4 列 diff、不一致列差异并 `read` 询问 y/N，且因 kill-server 会杀自身 pane 故在 `$TMUX` 内带 `-f` 直接拒绝）：两者都用 fzf 选历史快照，选择 UI 抽到共用的 **`workspace_snapshot_menu.sh`**（中文 prompt/header + `--reverse`，对齐 agent 启动器风格；带 manifest 预览：列窗口/repo/朝向、`↻` 标记可 `--resume` 的窗口；时间戳转人类可读，`{2}` 字段传 manifest 路径给 preview 子命令）。
- **timer 模板** `agent-tracker/app.hat-tmux-workbench.workspace-save.plist.tmpl`：`StartInterval=180`，占位 `__SCRIPT__`/`__STATE__`/`__PATH__`/`__TMUX_TMPDIR__`，与 tracker daemon 模板同套 sed/bootstrap 流程。timer 是 interval job，`status` 里处于 `waiting` 非 `running`，故安装时不做 `state=running` 校验。

### launchd 下运行 tmux 的三个坑（save 脚本 + 通知标题踩过，维护必读）

1. **PATH 不含 Homebrew**：launchd job 默认 PATH 无 `/opt/homebrew/bin`，裸调 `tmux` 找不到 → 守卫静默 `exit 0`、不存档。故 plist 注入 `EnvironmentVariables.PATH`（deploy 时按 `command -v tmux` 的目录拼），并附带 `TMUX_TMPDIR`（从运行中 server 的 `socket_path` 推导，兜底 `/tmp`）。
2. **`-F` 输出里的控制字符被 sanitize 成 `_`**：在 launchd 上下文里，tmux 会把 `-F` 格式**结果内**的所有控制字符（TAB、换行）替换成 `_`，只有 tmux 每条记录后自加的换行存活（交互环境不触发，极难复现；与 #3 同属 launchd 非 UTF-8 locale 根因）。因此 save 脚本**绝不**用「单行 TAB 多字段」或「字段内嵌换行」解析 tmux 输出：只枚举单字段 `#{window_id}`（每行一个），其余字段逐个 `display-message -p -t "$wid" '#{field}'` 取；需要多值时只用**空格分隔的无空格安全字段**（如 `'#{pane_index} #{pane_id}'`）。写进 manifest 文件的 TAB 不受影响（那是写文件、restore 从文件读，且 restore 走交互非 launchd）。
3. **非 UTF-8 locale 让 tmux 把中文等多字节字符逐格换成 `_`**：launchd job 不带 `LANG`/`LC_CTYPE`（默认 C/POSIX locale），此时 tmux `show-options -v` / `-F` 输出里凡当前 charset 表示不了的字节（CJK、控制字符）都按显示宽度替换成 `_`（每个全角字 2 格 → 2 个 `_`；交互 shell 有 UTF-8 locale 故不触发、极难复现）。曾踩坑：daemon 经 `tmux show-options @agent_notify_name` 读通知标题，`项目/中文标题` 弹到系统通知里整段变成一条下划线（消息体是 Go 字面量、不经 tmux，故只坏标题不坏正文）。**根治**：两个 plist 的 `EnvironmentVariables` 都钉死 `LANG=en_US.UTF-8`（模板里硬编码、非 sed 占位；设 UTF-8 locale 后 tmux 按 UTF-8 解析、不再替换）。同一根因也覆盖 #2 的控制字符坑。

## 两个易错点（维护必读）

1. **MCP 注册不走 settings.json**：Claude Code **不读** `settings.json` 的 `mcpServers` 键（实测+官方文档确认）。MCP 必须经 `claude mcp add -s user`（写 `~/.claude.json`，user scope = 所有项目可用）。hooks 才走 settings.json。这是对原 design「managed settings.json 片段注册 MCP」的事实纠正。
2. **`agent tracker command` flag 顺序**：Go flag 包在第一个非 flag 参数处停止解析，故 **flags 必须在 subcommand 之前**：`agent tracker command --session-id X --pane Y <sub>`。subcommand 在前会让坐标被折进 summary、context 退化到 autodetect。（`agent tmux on-focus --flags` 无 positional subcommand，不受影响。）
3. **通知点击跳转：`-execute` 里 `open -b`，既不用 `-activate` 也不用 `-sender`**（`sendSystemNotification`/`notificationActionForTarget`，`tracker-server/main.go`）。踩坑实测：
   - `-activate <bundleid>` 在新版 macOS 不可靠（点击仍激活错误的 app，如硬编码的 iTerm2）。
   - `-sender <bundleid>` 会让 macOS 按该 app 的通知权限判定，sender app 未授权时**整条通知被静默吞掉**（实测带 `-sender com.mitchellh.ghostty` 通知根本不弹）。
   - 最终方案：`-execute` 命令 = `/usr/bin/open -b <bundleid>; <tmux> switch-client … && select-window … && select-pane …`，靠 `open -b` 激活真正的终端。bundle id 由 `frontendTerminalBundleID()` 读 `tmux show-environment -g __CFBundleIdentifier`（Ghostty 下即 `com.mitchellh.ghostty`），不硬编码。
   - **引号坑**：terminal-notifier 点击时经 `/bin/sh -c <command>` 执行且 PATH 精简，故 tmux 用**绝对路径**、target 用**单引号**包（否则 session id `$0` 被 sh 展开成壳名，跳转目标失效）。
   - terminal-notifier 进程会**驻留**到点击/替换才退出（daemon 的 `cmd.Run()` 阻塞 goroutine）；从交互 shell 直接弹的测试进程生命周期不同，**残留的旧 terminal-notifier 会污染通知中心**，调试时先 `pkill -f terminal-notifier`。
4. **状态转移通知 + 分组（通知 = 🔔 的「同生同灭」）**：通知由 daemon 在状态转移时发（不再靠 Claude hook），且与状态栏 🔔 **共用同一决策点、同一条件**——别把发/消通知和铃铛拆到两处。
   - **抬起**：busy→idle `finishTask`→`notifyResponded`（✅ 任务已完成）；asking/limited/error 通过 `markTaskAttention` 进入不同 attention kind，error 通知为「⚠️ Codex 执行出错，请查看窗口」，其它沿用「❓ 有问题需要你回答」。attention kind 变化会重新抬起未读，重复轮询同一 kind 不重复通知。所有通知都**只在用户没在看该 window 时**才发。
   - **完成宽限去抖（HAT-515）**：`finishTask` **不再「见 idle 即完成」**。Claude 在 turn 边界等待后台任务/subagent 返回时也会瞬态 idle，周期轮询会撞到而过早发完成。改为两段式：首次 idle 仅置 `PendingCompleteAt`（不改 Status、不通知）；只有 idle **连续持续超过 `completionGraceWindow`（2s）** 才真正提交 completed 并通知。其间任一活动信号（busy/`shell`/asking，经 `startTask`/`markTaskAsking` 清 `PendingCompleteAt`，清除**先于** `markTaskAsking` 的 `Asking==asking` early-return）即作废本次待发完成。提交由后续 idle 轮询驱动（现为最多每 5s 一次，故真实完成通知通常增加约 5–10s 延迟）；非 in_progress 的 idle 一律 no-op（不凭空造 completed 任务）。
   - **「正在看」= `windowIsBeingWatched`**：window 被 tmux client 选中（`isActiveWindow`）**且**终端 App 在前台（`terminalIsFrontmost`，经 `lsappinfo front` 比对 `frontendTerminalBundleID`，免 Automation 权限）。**只判 `isActiveWindow` 不够**——终端退到后台时选中的 window 仍是「active」，会把该响应的 🔔 和通知一起误压制；必须叠加前台判断。bell auto-ack 与通知抬起共用这一个判定。探测不到前台 app 时保守视为「在看」（不改旧行为）。
   - **消除**：聚焦 window 的 `acknowledge` 里，`acknowledgeTask` 返回「确有未读被 ack」时（灭 🔔 的同一条件），`clearWindowNotification` 顺带 `terminal-notifier -remove` 掉该 window 的通知组——**仅 per_window 模式**（single 模式那条共享通知由下一条替换，无需主动消）。
   - **标题**：优先读窗口选项 `@agent_notify_name`——由 `agentWindowName` 每次 sync 时写入的**完整格式** `项目/标题 (model)`（强制 path+model、无状态前缀，**不受 Window Title 的 show_path/show_model tab 开关影响**），让通知始终自描述；缺省回退 live 窗口名并由 `stripNotificationStatusPrefix` 去掉状态前缀。
   - **分组**：`-group` 由 `notificationGroup(windowID)` 按设置决定——`single`（默认，新通知替换旧、只留一条）或 `per_window`（`agent-tracker-<windowID>`，每窗口独立共存）；存 `settings.json` 的 `notification_group_mode`，经 `set_notification_group_mode` socket 命令改（General 面板 toggle）。

## 扩展指南

- **加 client/provider**：Claude provider 来自 `~/.hat-env/providers/*.env`（picker 自动枚举）；新 client 在 `tmux/scripts/agent` 的 picker + case 映射加分支，命令名须在交互 shell 可解析（alias-common 函数或 PATH 上的 CLI）。若要支持实时标题/状态，还要在 `claude_session.go` 增加对应 runtime 状态来源。
- **改命名模板**：拼装在 `composeWindowName`/`splitSessionLabel`/`agentNameBase`（`cmd/agent/main.go`）；会话标题来源在 `window_naming.go`（`agentWindowName`）、sync 主循环在 `sync_names.go`，Claude/Codex runtime 读取在 `claude_session.go`/`codex_session.go`。改完同步 `rename_test.go` 表。
- **Timer 时间基准（`cmd/agent/window_timer.go`）**：`agent-config.json.timer_timezone` 控制墙上时间，缺省为 `UTC+8`；`auto` 使用 host `time.Local`，自定义值经 `parseTimerTimezone` 接受 IANA name 或 UTC offset。`HH:MM` 定点触发和 `daily` 循环按当前 location 的日历排程；面板、Window Nav 摘要及新增提示在格式化前统一 `.In(location)`；新建 timer/history 的持久化时间戳带当前 offset。General 面板 `Timer timezone` 行以 `Space` 切 auto、`Enter` 输入自定义值；`setTimerTimezone` 保存后立即重排所有 enabled time-trigger timer。duration 与 quota reset 都是绝对时长/时刻，不改变触发 instant；存量 RFC3339 时间戳无需迁移。
- **额度探测（`cmd/agent/quota.go`）**：`quotaResetFireAt(windowID)` 从磁盘解析 AI 客户端额度重置时刻——Codex 读会话 rollout 最新 `token_count` 的 `rate_limits.*.resets_at`（绝对 epoch；≥95% 用量的窗口取最晚，否则取 5h 边界），Claude 解析 session JSONL 里 429 记录的 "resets 12:40am (TZ)" 文案（被动：撞限后才有）。供 timer 的 `reset` 触发与 `[L]` limited 状态共用。**reset timer 三级调度（确切→保底→休眠）**：`scheduleQuotaFireAt` 依次尝试 ① 确切（429 文案 / codex rollout，+90s buffer）② 保底——`claudeFallbackResetAt` 读 `state/agent-tracker/claude-rate-limits.json`（**`~/.claude/bin/cc-statusline-official` 每次渲染状态栏时落盘** Claude Code 注入 statusline 的第一方 `rate_limits.{five_hour,seven_day}.resets_at`；timer 标 `QuotaFallback`）③ 都拿不到→置零休眠。`checkAndFireTimers` 每 tick 对「休眠或 fallback 态」quota timer 先查本窗 `@agent_limit_reset_at`、再查 `anyWindowQuotaLimitedUntil()`（额度是账号级，任一窗撞限即把全部升级为确切时刻）。Loop `reset`（`windowTimerLoopQuota`）触发后重走三级调度，Max exec 照常封顶；`DeleteOnDone`（表单 Auto del）让 timer 在最终一次执行后直接删除记录。`fireTimer` 注入前经 `dismissUsageLimitDialog` 清 Claude 额度弹窗（capture-pane 匹配文案 + 非 busy 才发 Esc）。timer History 为全局模板库（`timerHistoryAll` 跨窗去重，`deleteTimerHistoryCombo` 跨窗删）。改判定阈值/文案正则时同步 `quota_test.go`。
- **加 palette 面板**：见 `cmd/agent/palette*.go`（bubbletea）。
- **加 socket command**：daemon handler（`cmd/tracker-server/main.go`）+ `tracker_cli.go` 的 case 列表 + `internal/ipc` envelope 字段。

## setup 向导分层（`scripts/setup`）

`scripts/setup` 是 deploy 之上的**决策收集层**（`/bin/bash` 3.2 兼容——macOS 自带 bash 无关联数组，i18n 用 `MSG_<lang>_<key>` 前缀变量族 + `${!v}` 间接引用；依赖表/键位表用平行数组按下标对齐）。分层：向导解析 flag + 解决决策表 → 执行各模块（deps / icon / keymap / statusline）→ 把决策映射成 `deploy.sh install` 的参数并调用。`deploy.sh` 仍可独立使用。

- **十个决策点**：`language`/`icons`/`keymap`/`deps`/`tmux`/`daemon`/`ws-timer`/`stop-hook`/`statusline`/`alias`，各有一个 `decide_*` 函数只解析出 `DEC_*`。交互模式逐项 Y/n 或向导；**非交互模式一切侵入动作缺省 skip**，agent 需显式 `--<项>=install` 正向授权（`deploy.sh` 侧仍是负向 `--skip-*` 六开关，由 setup 按决策映射）。`--yes` 只解除交互确认、不改缺省。
- **模块**：
  - **deps**（`deps_scan`/`deps_auto`）：必需 `tmux(≥3.3)`/`go`/`fzf`/`jq`、可选 `z`/`lazygit`/`terminal-notifier`/`gh`（平行数组 `DEP_names/DEP_types/DEP_minver/...`）；`auto` 披露后一次同意 `brew install` 批装、无 brew 打印手动命令；`check` 必需缺失 → 非零退出 + `degraded`。
  - **icon**（见下「图标集」）：自测选集 → 写 `agent-config.json.icon_set`。
  - **keymap**（见 `docs/GUIDE_TMUX.md` 的 keymap.conf 定制节）：preset → 冲突扫描 → 逐条向导 → 写 `private/keymap.conf` → 沙盒试载。
  - **statusline**（见下「claude_statusline」）：探测现有 statusLine → install/chain/skip，把结果写 `agent-config.json.statusline_chain`（deploy 的唯一传递方式）。
- **输出契约**：`--json` 时 stdout 是纯 JSONL（每行一个 `{"step","status":"ok|skip|fail|degraded","detail"}`，末行 `{"result":"complete|degraded|failed"}`），人类文案全走 stderr（`say()`）。`agent-guide` 子命令输出静态机读 JSON 契约（flags 全表 + 决策点清单 + JSONL schema + 幂等性 + 验证命令），无副作用——供 agent 部署前读取。
- **config 写协议（跨 writer）**：setup（bash）与 Go（Settings 面板）共用同一把原子 **mkdir 锁** `~/.config/agent-tracker/.config.lock.d`（>5s 陈旧锁可抢占，与仓内 reflow 锁同模式）+ 锁内 read-modify-write（`jq` merge，保留未知字段）+ 同目录临时文件原子 `mv` 替换。首跑 XDG 无配置时从 `agent-tracker/agent-config.example.json` 初始化（`config_set_field`/`config_set_field_json`）。

## 图标集（`icons.go` / config `icon_set` / `left.sh` 数据流）

状态栏图标是 **per-machine** 配置（不跨机同步），唯一权威在 `~/.config/agent-tracker/agent-config.json` 的 `icon_set` 字段（`nerd`/`emoji`/`ascii`，缺省 nerd）。

- **Go 侧**（`cmd/agent/icons.go`）：`iconSet{CPU,Network,Memory,Window,Session,Total,Todos,FlashMoe,SepLeft,SepRight}` 三套完整实例——`iconSetNerd`（Nerd Font PUA 全套）、`iconSetEmoji`（🖥/📡/🧠/🪟/📑/Σ/☑/⚡，分隔符用**非 PUA** 的 `▐`/`▌`＝U+2590/258C，DOS box element，兼容性好）、`iconSetASCII`（`CPU/NET/MEM/WIN/SES/TOT/TODO/moe`，分隔符退化为空格）。`activeIconSet()` 按 `loadAppConfig().IconSet` 选集，非法值回退 nerd；`tmux_status.go` 的图标与 separator 全走该 getter。单测（`icons_test.go`）断言三套字段非空、emoji/ascii 无 PUA 码位（E000–F8FF/F0000+）、ascii 纯 ASCII。
- **shell 侧**（`tmux/tmux-status/left.sh`）：`jq -r '.icon_set // "nerd"'` 读同一 config，三值 case 只取 session tab 的 `separator`（`emoji→▐`/`ascii→空格`/其它→PUA）。左栏图标经 `agent tmux right-status`/`left` 由 Go 渲染，separator 是 shell 侧唯一直接用 icon_set 的地方。
- **切换**：Settings → General 的 **Icon set**（nerd/emoji/ascii 循环）写 config 原子替换，秒级生效；setup 首装时的图标自测同样写这一字段。

## claude_statusline（缓存 schema / chain / 单写者迁移）

`tmux/scripts/claude_statusline.sh` 是本项目自建的 Claude Code statusLine 入口（`bash` 3.2 兼容，无 `timeout` 二进制、用 perl alarm 实现超时），由 deploy 注册进 `~/.claude/settings.json.statusLine`。每次渲染做三件事：

1. **缓存 rate_limits 快照**（供额度 reset timer 的保底触发）：从 stdin 的 statusline payload 取 `.rate_limits`，加一个 `written_at` epoch 戳，原子写 `state/agent-tracker/claude-rate-limits.json`。**schema 与 `quota.go` 的 `claudeFallbackResetAt` 对齐**：顶层 `five_hour`/`seven_day`，各带 `used_percentage` 与 `resets_at`（epoch）；`written_at` 是本脚本额外加的、`quota.go` 忽略。`claudeFallbackResetAt` 读它、按与 codex 相同的耗尽窗口规则（5h↔primary / 7d↔secondary）挑 reset 时刻，作为 timer `reset` 触发在确切 429 时刻拿到前的 fallback（见 quota.go 三级调度）。
2. **chain 委派**（可选）：若 `agent-config.json.statusline_chain.command` 非空，把 stdin 管进该命令（2s 硬超时，`run_chain` 用 perl setsid + 负 pid 组 kill 收割 pipeline 子进程），成功且有输出则原样输出、退出；失败/超时/空输出回退内建渲染并按小时节流告警。
3. **内建渲染**：cwd（`$HOME` 缩为 `~`）+ model + 5h/7d 额度百分比与倒计时。
- **单写者迁移**：注册本项目 statusline 的前提是**移除其它 rate-limits 写入者**——若用户已有别的 statusLine 也在写 `claude-rate-limits.json`，双写会互相覆盖。setup 的 statusline 模块检测到旧写入器时先备份（`.bak`）再删其写入行、沙盒验证语法未坏才提交；无法验证/用户拒绝则回退 skip（degraded），绝不静默双写。原 `statusLine` 配置以完整 JSON object 存入 `statusline_chain.original` 供 uninstall 对称恢复（`deploy.sh` 的 `register_statusline`/`unregister_statusline`：仅在「仍是我们的脚本或仍等于 recorded original」时才改写，否则中止不 clobber）。

## Overlay 契约表（private overlay，公开仓视角）

个人 / 机器本地文件走 gitignore 的 overlay，保持公开仓干净。三层防线：① `.gitignore`；② `scripts/git-hooks/pre-commit`（staged 出现保留路径即拒绝，deploy 安装）；③ 发布门 tree allowlist 终审。

| 私有路径 | producer | consumer | 加载时机 | 缺失语义 |
|---|---|---|---|---|
| `private/keymap.conf` | setup 键位模块 | tmux（`tmux.conf` 固定行 `source -q`） | tmux 启动 / reload | 静默用默认键位 |
| `private/docs/` | 迁移一次性 + 日常沉淀 | 人类 / Claude 会话 | 按需 | 无影响 |
| `CLAUDE.md`（仓根） | 用户维护 | Claude Code（仓根固定） | 会话启动 | Claude 无项目上下文 |
| `.tasks/` | task 工作流 | task 工作流 | 按需 | 无 open 任务 |
| `snippets/private/` + `snippets/.favorites` | snippet 面板 / 手建 | snippet 数据层（递归读） | 面板打开 | 只见公开示例 |

- **`agent-config.json` 不属于 overlay 契约**：真实配置唯一在仓外 XDG 路径 `~/.config/agent-tracker/agent-config.json`（`paths.ConfigFile()`），本就不在公开仓风险面内。仓内 tracked 的同名文件是含个人 keys 的历史残留——处置＝脱敏改名 `agent-config.example.json` + 移除原名 + gitignore 原名防误加；setup 首跑无 XDG 配置时从 example 初始化。
- **`git clean -fdx` 风险**：会删所有 gitignore 文件、抹掉整个 overlay。文档（README）明示风险与恢复（Syncthing versioning / 私有备份）。

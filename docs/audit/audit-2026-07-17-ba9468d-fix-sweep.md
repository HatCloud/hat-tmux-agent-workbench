# 整体审阅 + 修复清扫报告 — hat-config

日期：2026-07-17（东八区）　|　基线：`audit-2026-07-15-10eae14.md`（昨日全量审计）　|　本轮提交：`09df42b..ba9468d`（6 commits）　|　性质：**增量复审 + 修复执行**（区别于上一份的只读审计）

---

## 执行摘要

上一份审计（@10eae14）留下的 backlog 中，簇 A（RCE）与簇 B（daemon 健壮性）已随 HAT-586 修复；本轮把**剩余的簇 C/D/E/F/G/H 全部清掉**，并修复了两个本轮新发现的真实 bug（僵尸任务、完成宽限提交滞后——后者直接对应用户「刷新经常不及时」的反馈）。全部改动经 go build/vet/test、shellcheck、shell 测试、真机部署（本机 + mini）验证。

**当前状态：上一份审计的 backlog 除「簇 I 内存监控（新功能）」外全部完成。** 项目剩余的已知问题只有小项（见「遗留」）。

## 本轮新发现并修复的 bug（非 backlog）

1. **僵尸任务永久残留（fix, `09df42b`）**：daemon 的任务只有显式 `delete_task` 一条删除路径，窗口关闭时其 in_progress 任务永久留在内存/cache。实证：mini 的 cache 里 `@3`/`@4` 已关窗任务挂了数小时，把远端聚合状态钉死在 `[B]`（即你最初问的「0 号窗一直 [B]」的一半根因——另一半是真实的未读 asking）；本机 cache 也有 3 条 8–11 小时僵尸。修复：`orphan_sweep.go` 每 15s 核对 tmux 存活窗口清孤儿任务，空存活集视为不可信跳过（防 tmux 瞬态不可达误清）。**真机验证**：本机 cache 16→4 条、mini 3→1 条，全部对应存活窗口。
2. **完成 🔔 提交滞后（fix, `09df42b`）**：busy→idle 的 2s 完成宽限到期后没有主动重查，提交要搭下一次 periodic 轮询便车（`poll_interval` 3s + `status-interval` 3s 叠加，最坏 5s+）。修复：宽限开始时 `time.AfterFunc(grace+200ms)` 触发一次 coalesced sync-names，完成通知稳定在 ~2.5s。这是「通知/状态经常不及时」的最大单点改善。

## Backlog 修复明细

| 簇 | Findings | 做了什么 | commit |
|---|---|---|---|
| C 热路径 | I-3 | 🔔 tab 图标改 daemon 预计算写 `@agent_icon`（`reconcileWindowIcons`，挂 broadcastState + remote bell 沿），`window-status-format` 原生展开——**每窗口每 3s 的 tmux+cat+jq 三连 fork 归零**；`window_task_icon.sh`/`session_task_icon.sh` 删除，left.sh 的 session 图标改聚合 `@agent_icon` | `09df42b`/`ed18bf9` |
| C 热路径 | I-4 | sync-names pass 级选项 memo：一次 `display-message`（0x1F 分隔）批量读 17 个 `@agent_*` 选项替代每窗口 ~25 次 `show-options` fork；写经 `setWindowOption`/`unsetWindowOption` 写穿透（保同 pass `@agent_error_*` 写后读依赖），带单测 | `71372ac` |
| C 热路径 | M-1 | `readSessionJSONL` tail-first（256KB），tail 无 ai-title 才回退全量扫 | `71372ac` |
| D 一致性 | I-5 | `internal/statustag` 单一真身：`[B]/[I]/[?]/[L]/[E]` 词表 + shell→busy 等别名折叠，cmd/agent 渲染与 tracker-server 远端解析共用（原四处硬编码跨两 binary），带单测 | `399dd20` |
| D 一致性 | I-6 | pane role 常量化（`paneRoleAI/Git/Run`）+ `warnPaneRoleDrift`（role 集漂移 stderr 可见，按签名去重） | `399dd20` |
| D 一致性 | I-7 | 朝向阈值**保持不同数值**（设计如此），两侧互加交叉引用锚点声明共享物理假设、明示勿统一 | `399dd20` |
| D 一致性 | I-8 | `_abbrev_dir` 补 mirror 注释；Go `TestAbbrevPath` 与 shell `agent_helpers_test.sh` 同组用例钉死双语实现 | `399dd20` |
| E 死代码 | I-9/I-10/I-11 | Devices/Start-agent prompt 残留（2 类型+4 枚举+11 字段+JSON 键）、9 个零调用函数、8 个孤儿脚本（原 6 + 2 个 icon 脚本；删前复核 ~/.hat-env 零活引用）；watch_pane 生态的 4 个窗口选项全生态零写入方，连带 tmux.conf 2 条死 unset hook 一并清除。净 -634 行 | `ed18bf9` |
| F 文档 | I-12 | `docs/handoff/agent-workspace-assessment.md` 幽灵引用删除（debt.md 已随 HAT-586 建档）；CLAUDE.md 数处「每秒」表述改为与 `status-interval 3` 一致 | `eb44c6a`+未入库 CLAUDE.md |
| G 拆分 | I-13 | `claude_session.go` 2100+ 行按职责拆 6 文件（claude_session 339 / codex_session 456 / window_naming 636 / sync_names 296 / ssh_window 291 / orientation 224），纯机械搬移零行为变更 | `ba9468d` |
| H 工具闸门 | 工具缺口 | shellcheck（已装未接入）纳入完整验证命令，警告基线清零；shell 测试纳入验证命令 | `eb44c6a` |

## 验证证据

- `go build ./... && go vet ./... && go test ./...` 全绿（4 包，新增 statustag/orphan_sweep/window_opts/abbrev_path 等测试）。
- `shellcheck -S warning` 全部脚本零违规；4 个 shell 测试全 PASS。
- 真机部署：`deploy.sh install --yes --skip-statusline` 完成（本机 + mini rsync+远端重建+daemon 重启）；运行中 tmux 已 reload 新 format，`@agent_remote_status` 正常镜像，两机 cache 僵尸清零。
- statusLine 注册步骤按守卫跳过（既有自定义 statusLine 无 `statusline_chain` 记录，deploy 拒绝覆盖——守卫行为正确，非本轮问题）。

## 遗留（小项，非阻塞）

1. **HAT-587（debt.md）**：`#()` command-substitution 注入面未验证（`pane_title` 可被 OSC 转义任意设置）。需 attached client 渲染循环驱动，detached 自动化测不了——维持追踪。
2. **簇 I 内存监控**（上一份审计的用户追加需求）：新功能未做。要点已记录在上一份报告附录（`ps rss` 严重低估、需 `phys_footprint`；「定期自动回收」不可行）。
3. **Poll interval <3s 档位实际被 `status-interval 3` 钳住**：periodic 触发源是状态栏重绘。事件驱动路径（Claude 亚秒）不受影响；Codex 无事件源仍 3s 起。要真 1s 需 daemon 自持 ticker——收益有限，暂记录不改。
4. **Settings「Poll interval」自由输入钳制 500ms–60s** 与上条同理，<3s 段位是名义值。
5. deploy 的 daemon 健康检查偶发误报「not healthy after bootstrap」（bootout→bootstrap 竞态，进程实际已起）；重跑即过。可在 deploy 加重试宽限，未做。
6. 测试覆盖率仍低（cmd/agent ~10%）；本轮新增逻辑均带测试，存量欠账未补。

## 与上一份报告的衔接

- 上一份的 False Positive Log 与「不宜修正的刻意设计」（朝向阈值、hjkl incidental duplication、CLAUDE.md 不进 git 等）本轮全部遵守，未「修正」任何 opt-out 项。
- 上一份报告中 left.sh 注释「daemon cache 会残留已关闭窗口的 stale in_progress task」——该残留本轮已根治（orphan sweep），但 session ⏳ 仍按窗口名 `[B]` 聚合（该判据依旧正确且更便宜）。

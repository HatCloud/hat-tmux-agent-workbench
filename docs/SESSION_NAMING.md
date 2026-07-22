# Agent Session Naming 能力矩阵

本文记录 `internal/agentclient` 各 adapter 对会话名称的 read / provenance detection / write 能力及其证据。新增 agent 时必须补这一矩阵：先验证真实 CLI 版本、帮助、协议或本地持久化形状；可实现则接入 `SessionNamer`，不可实现才明确标为 tracker-owned fallback。

## 统一语义

`SessionNameState` 不把“显示标题”都叫作 Session Name：

- `user`：用户通过 agent 自身 rename/name 能力设置的名称，窗口仲裁级别 1。
- `generated`：agent 自己根据 prompt/summary 生成的默认标题，级别 4。
- `none`：已确认支持命名，但尚无用户名称。
- `unknown`：当前版本或存储形状无法可靠判定，不冒险覆盖。

`Writable` 只表示该 adapter 已验证安全写入路径。自动命名会先把名称与 `client:sessionId` provenance 写入 tmux option，再调用 adapter；这样 adapter 下一轮读到的“user”值若等于本 tracker 生成值，仍按级别 3 处理。值不同才代表用户后来原生 rename，升到级别 1。

## 2026-07-21 检测结果

| Agent（本机版本） | 读取 / 判断名称 | 外部写入 | 当前支持 |
|---|---|---|---|
| Claude Code 2.1.217 | `~/.claude/sessions/<pid>.json.name` 或 project JSONL 最新 `custom-title` = user；`ai-title` = generated default | 原子追加 `{"type":"custom-title","customTitle":…,"sessionId":…}` 到当前 project JSONL；只允许 `.claude/projects` 内已存在 transcript，写前拒绝已有 user name | read + detect + write |
| Codex CLI 0.145.0 | `threads.title` 与 session index 首条 `thread_name` 是默认显示标题；同一 id 后续出现不同 `thread_name` 是当前 CLI `/rename` 的可观测信号，取最后值；非空 `threads.name` 是 app-server 显式名称兼容来源 | 启动本地 `codex app-server --listen stdio://`，握手后调用 `thread/name/set`；写前经同一名称解析器重读 | read + detect + write |
| Grok Build 0.2.106 | `summary.json.generated_title == session_summary` = generated default；两者不同 = `/rename` user name；文件无效或 provenance 字段不完整时为 unknown | 当前 CLI/ACP 未发现可验证的外部 rename 合同；`SetSessionName` 明确返回 unsupported，使用 tracker-owned alias | read + detect；write unsupported |

无副作用复核命令（只读 version/help，不访问或修改会话）：

```bash
claude --version
claude --help | rg -- '-n, --name|--name'
codex --version
codex app-server --help
codex app-server generate-json-schema --help
grok --version
grok --help
grok sessions --help
grok agent stdio --help
```

当前期望版本输出依次包含 `2.1.217`、`Set a display name for this session`、`codex-cli 0.145.0`、app-server/schema 生成子命令，以及 `grok 0.2.106`；Grok 的 sessions 子命令只有 list/search/delete，stdio help 也不公布 rename。`thread/name/set` 和 Grok **交互式** `/rename` 的具体合同以本节链接的官方协议/命令文档为准；后者不等于外部写入 API。

## 证据与边界

### Claude Code

- 本机 `claude --help` 暴露 `-n, --name <name>`，说明 CLI 有原生 session display name；[Anthropic CLI reference](https://docs.anthropic.com/en/docs/claude-code/cli-usage) 是官方 CLI/`--resume`/headless 合同入口，但页面抓取版本尚未列出 `--name`，故以当前安装版 help 为运行时真相。
- 本机真实 transcript 中人工 rename 产生独立 `custom-title` record，和模型生成的 `ai-title` 分开；当前 adapter 按这一 shape 写入。这里不是公开 RPC，升级 Claude 后若 record shape 变化必须重新验证，解析失败应退到 tracker-owned 名称而非修改未知格式。

### Codex

- OpenAI 官方开源 [app-server README](https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md) 明确：`thread/name/set` 可为已加载 thread 或持久化 rollout 设置/更新 user-facing name，并发出 `thread/name/updated`。
- 本机生成的 app-server JSON schema 含 `ThreadSetNameParams {threadId,name}`；对不存在 UUID 的握手探针成功进入 handler 并返回 “no rollout found”，证明当前二进制已注册该方法。0.145.0 的真实样本在创建时先写一条 `thread_name='Hat配置同步规范'`；执行 `/rename 测试` 后为同一 id 追加 `thread_name='测试'`，同时 SQLite 为 `threads.title='测试'`、`threads.name=NULL`。因此“首条非空 index 名”不能当用户 rename；adapter 只有观察到同一 id 的值发生后续变化，或读到非空 `threads.name`，才报告 `user`。index 损坏或 legacy DB 无法补证时报告 `unknown`，不冒险覆盖。
- adapter 不直接写 Codex 索引/SQLite，以 app-server 为唯一写路径。`Detect` 与 `SessionName()` 共用 `queryThreads` + `sessionNameState`，避免状态栏和其它消费者再次各自推断 provenance。

### Grok Build

- xAI 官方 [Modes and Commands](https://docs.x.ai/build/modes-and-commands) 明确 `/rename <title>`（别名 `/title`）可重命名当前 session，`/sessions` 也能 switch/rename/close。
- xAI 官方 [Headless & Scripting](https://docs.x.ai/build/cli/headless-scripting) 公开了 `grok -p` 与 ACP `grok agent`，但没有会话 rename 请求。本机 0.2.106 的 `sessions`/`agent stdio` 静态 help 同样无 rename；此前对 0.2.102 的 ACP 初始化探针也没有 advertise session rename capability，尝试内部候选方法返回 method-not-found，因此当前没有可验证的外部写入合同。
- 本机 session 样本中默认 `generated_title` 与 `session_summary` 一致，人工 `/rename` 改变前者，所以 adapter 可以读取并区分默认标题和用户名称。这是对当前存储 shape 的实测推断，不授权直接编辑私有 `summary.json`；自动生成名称降级保存在 tracker window option。若未来公开稳定 RPC/CLI，再实现 native write 并补回归测试。

## 新 adapter 接入清单

1. 固定并记录被测版本；先查 `--help`、官方文档/协议和真实无敏感值样本。
2. 分别回答：能否读取名称、能否区分用户名称与默认标题、能否从外部安全写入。
3. 三项可行时实现 `SessionNamer`，并测试 unnamed、user-named、写入、拒绝覆盖和旧版本降级。
4. 只读或完全不支持时仍须实现强制接口，返回 `Writable=false` / `ErrSessionNameUnsupported`，并在本文件留下证据；编排层会使用 tracker-owned level-3 名称。
5. 若支持自动命名上下文，实现 `FirstPrompter`，只返回第一条用户 prompt，不把完整 transcript 持久化到 tmux。

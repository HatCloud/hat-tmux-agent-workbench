# Technical Debt

Open / resolved 技术债追踪。条目格式：`- [ ]`（open）/ `- [x] ... — resolved: <出处>`（resolved）。

- [ ] `#()` command-substitution 注入面未验证：`pane-border-format`（`tmux/tmux.conf:53`）等位置把 `#{pane_title}` 包进 `#(...)`，而 `pane_title` 可被任意程序经 OSC 转义序列（`\033]2;...\007`）任意设置——实测已确证 OSC 可改 `pane_title`。若 `#()` 内的 format 展开可逃逸到 shell，风险高于本次已修复的 run-shell 注入（status line 每 3s 自动展开、无需用户按键）。未验证原因：`#()` 是 tmux 的异步 job，需 attached client 的渲染循环驱动，detached server 自动化测不了。待验证位置：`tmux.conf:53,178,185,187`。发现于 `2026-07-15-rce-daemon-fix`（HAT-586），追踪见 **HAT-587**。
- [ ] Grok 无 `reset`/quota timer 信号源：`quotaResetFireAt` 仍仅 Claude 429 / Codex rate_limits；Grok 窗 `prefix t` → `r`/Trigger=reset 会 dormant。发现于 `2026-07-17-grok-adapter`（HAT-596）。
- [x] Agent client S4 partial cutover：Claude/Codex limited/error/provider 富化仍在 `cmd/agent` legacy 路径；消费方未完全「零 client 字面分叉」。发现于 `2026-07-17-grok-adapter`（HAT-596）。 — resolved: `2026-07-21-adapter-modularize`（全面收编：探测全部下沉 adapter，`cmd/agent` 只消费 `LiveSession`，单 ps 快照）。
- [ ] `restore_workspace.sh` 的 resume 命令表（claude/codex/grok → `--resume`/`resume`）硬编码在脚本内，Go 侧无单一真身（原 `ResumeArgver` 接口零消费已删）；新增 client 时需手动同步脚本 case。发现于 `2026-07-21-adapter-modularize`。
- [ ] Codex thread 的 CWD 回退（无 rollout 绑定时）改用 codex 进程 cwd（lsof `-Ffn` 的 `fcwd`）而非 tmux `pane_current_path`——两者在 codex 启动后 shell 再 cd 的场景可能不一致（影响仅限「thread 尚未写 rollout」的启动瞬间窗口命名）。发现于 `2026-07-21-adapter-modularize`。
</content>

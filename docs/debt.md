# Technical Debt

Open / resolved 技术债追踪。条目格式：`- [ ]`（open）/ `- [x] ... — resolved: <出处>`（resolved）。

- [ ] `#()` command-substitution 注入面未验证：`pane-border-format`（`tmux/tmux.conf:53`）等位置把 `#{pane_title}` 包进 `#(...)`，而 `pane_title` 可被任意程序经 OSC 转义序列（`\033]2;...\007`）任意设置——实测已确证 OSC 可改 `pane_title`。若 `#()` 内的 format 展开可逃逸到 shell，风险高于本次已修复的 run-shell 注入（status line 每 3s 自动展开、无需用户按键）。未验证原因：`#()` 是 tmux 的异步 job，需 attached client 的渲染循环驱动，detached server 自动化测不了。待验证位置：`tmux.conf:53,178,185,187`。发现于 `2026-07-15-rce-daemon-fix`（HAT-586），追踪见 **HAT-587**。
- [ ] Grok 无 `reset`/quota timer 信号源：`quotaResetFireAt` 仍仅 Claude 429 / Codex rate_limits；Grok 窗 `prefix t` → `r`/Trigger=reset 会 dormant。发现于 `2026-07-17-grok-adapter`（HAT-596）。
- [ ] Agent client S4 partial cutover：Claude/Codex limited/error/provider 富化仍在 `cmd/agent` legacy 路径；消费方未完全「零 client 字面分叉」。发现于 `2026-07-17-grok-adapter`（HAT-596）。
</content>

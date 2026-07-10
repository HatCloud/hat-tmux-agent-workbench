# Todo 管理使用指南

Palette 内置了一个三作用域 todo 管理器，支持全局 / 会话 / 窗口三层范围，与 tmux window 直接联动。

tmux 基础与快捷键速查见 [GUIDE_TMUX.md](./GUIDE_TMUX.md)；底层实现见 [ARCHITECTURE.md](./ARCHITECTURE.md)。

## 1. 打开方式

- **Palette 菜单**：`alt-s`（或 `prefix o`）→ 选 **Todos**
- **直接打开**：`agent palette --open=todos`

## 2. 界面布局

面板分三栏：

```
┌─────────────────┬──────────────┐
│ Window Todos    │ Global Todos │
│（当前 window）  │（全局）      │
├─────────────────│              │
│ Other Windows   │              │
│（其他 window）  │              │
└─────────────────┴──────────────┘
```

- **左上**（Window）：当前 tmux window 专属的 todos
- **左下**（Other Windows）：其他 window 的 todos，仅作参考，不可编辑
- **右**（Global）：全局 todos，所有 session/window 可见

## 3. 作用域说明

| 作用域 | 可见范围 | 典型用途 |
|---|---|---|
| Global | 所有 session 和 window | 跨项目待办、长期任务 |
| Session | 当前 tmux session | session 级别的中期计划 |
| Window | 当前 tmux window | 当前项目 / 任务的具体 next actions |

新建 todo 时默认落在「当前 window」作用域，可在添加模式里切换。

## 4. 键位速查

### 导航

| 键 | 功能 |
|---|---|
| `j` / `k` | 上下移动选中项 |
| `n` / `i` | 切换焦点到 Window 栏 / Global 栏 |
| `Tab` | 在 Window ↔ All Windows 两个左侧子栏之间切换（焦点在 Global 时无效） |

### 核心操作

| 键 | 功能 |
|---|---|
| `a` | 新建 todo（输入标题后 `Enter` 确认，`Esc` 取消） |
| `Space` | 切换完成状态（完成 ↔ 未完成） |
| `e` 或 `E` | 编辑选中 todo 的标题 |
| `p` | 复制当前 todo（在同一作用域下创建副本） |
| `y` | 复制标题文字到系统剪贴板（方便粘贴到其他地方） |
| `d` 或 `x` | 删除（弹出确认：`y` 确认，`n` 取消） |
| `Enter` | 跳转到该 todo 对应的 tmux window（仅 Window 作用域有效；Global todo 无法跳转） |

### 优先级与排序

| 键 | 功能 |
|---|---|
| `1` | 设置为高优先级 |
| `2` | 设置为中优先级（默认） |
| `3` | 设置为低优先级 |
| `Ctrl+K` | 手动上移 |
| `Ctrl+J` | 手动下移 |

### 跨作用域移动

| 键 | 功能 |
|---|---|
| `I`（大写） | 把选中的 Window todo 提升到 Global |
| `N`（大写） | 把选中的 Global todo 移到当前 Window |

### 其他

| 键 | 功能 |
|---|---|
| `c` | 显示 / 隐藏已完成的 todos |
| `Esc` | 退出 todo 面板 |

## 5. 防误删保护

删除带**未完成** Window todos 的 agent window 时，系统会拒绝操作并提示剩余 todo 数量。需先把 todos 清空（完成或删除）或用 `I` 提升到 Global，才能继续关闭窗口。

## 6. 数据存储

todos 持久化在 `~/.cache/agent/todos.json`，JSON 格式，按三个作用域分类存储。整个 `~/.cache/agent/` 不纳入 git，属于本机运行时数据。

旧版使用 `~/.tmux-todos/*.yaml` 存储，首次打开时自动迁移到新格式。

# LazyGit 使用指南

这份文档说明 LazyGit 的基本使用方式，以及它在本项目 tmux 工作流里的角色。

LazyGit 是一个终端里的 Git TUI（文本界面客户端），用键盘可视化操作 stage、commit、branch、rebase、merge、stash、冲突解决等常见 Git 任务，目标是替代 `git status` / `git add -p` / `git log` / `git rebase -i` 这一类命令行操作中容易出错的部分。

tmux 基础见 [GUIDE_TMUX.md](./GUIDE_TMUX.md)，整体工作流见 [GUIDE.md](./GUIDE.md)。

## 1. 本项目中的定位

本项目创建的 AI 工作窗口（默认两 pane，可选第三个 run pane）会在 `git` pane 中自动启动 LazyGit：

```bash
lazygit
```

如果当前环境没有安装 LazyGit，回退到：

```bash
git status --short --branch
```

启动命令不附加参数。本机配置固定使用标准布局，并禁用按窗口尺寸自动切换 portrait 布局：

```yaml
gui:
  screenMode: normal
  portraitMode: never
  expandFocusedSidePanel: true
  expandedSidePanelWeight: 8
```

左侧使用手风琴布局：当前聚焦的 panel 获得主要高度。默认聚焦 Files 时，
Local branches 和 Commits 各保留约两行内容；切换到它们时，对应 panel 会临时展开。

## 2. 启动

在任何 git 仓库根目录下：

```bash
lazygit
```

建议加 alias：

```bash
alias lg='lazygit'
```

加到 `~/.zshrc` 或 `~/.hat-env/shared/alias-common` 里都行。

也可以从 git 仓库内任意子目录启动 LazyGit，它会自动定位到 git root。

## 3. 面板布局

LazyGit 启动后默认是五个面板：

```text
+----------------------------------------------------------+
| Status                                                  1|
+----------------+-----------------------------------------+
| Files          | Commits                                4|
|                |                                         |
|                +-----------------------------------------+
|                | Stash                                   |
|                |                                         |
+----------------+-----------------------------------------+
| Branches                                             3   |
+----------------------------------------------------------+
```

面板含义：

| 编号 | 面板 | 用途 |
| --- | --- | --- |
| `1` | Status | 当前 HEAD、push/pull 状态、未保存改动数量。 |
| `2` | Files | 工作区里的改动文件列表，已暂存和未暂存分开显示。 |
| `3` | Branches | 本地分支、remote 分支、tags。 |
| `4` | Commits | 当前分支的提交历史。 |
| `5` | Stash | 当前的 stash 列表。 |

切换方式：

- 数字键 `1` ~ `5` 直跳。
- `←` / `→` / `Tab` / `Shift-Tab` 在面板间顺序切换。
- `?` 打开完整键位菜单。
- `q` 退出 LazyGit。
- `esc` 返回上一级或关闭弹窗。
- `x` 关闭当前菜单或文件视图。

面板内部移动：

- `↑` / `↓` 或 `k` / `j`：上下移动选中项。
- `enter`：展开 / 折叠目录，或进入详情视图。
- `space` 或 `ctrl-b`：在 Main 面板里向前 / 向后翻页。

## 4. 最常用 6 个动作

这一节是日常 90% 的操作。

### 4.1 暂存文件

在 `Files` 面板选中文件：

```text
space    切换该文件的 staged / unstaged 状态
a        暂存所有变更或撤销全部暂存
enter    展开文件，查看逐行 diff
```

进入文件内部后，Main 面板会显示该文件的 diff。逐行 stage：

```text
space    切换当前行的 staged 状态
v        进入范围选择模式
a        切换整 hunk 选择模式
```

`v` 模式下用 `↑` / `↓` 选范围，最后按 `space` 一次性 stage；`a` 模式下 `space` 整 hunk stage。

### 4.2 提交

回到 `Files` 面板：

```text
c        打开 commit message 编辑器
enter    写入 commit message 后提交
```

`c` 会用 `$EDITOR` 打开文件，写完保存退出即可。LazyGit 默认会把多行 commit message 折叠成一段，更习惯多行的可以在 config 里关掉。

修改上次提交：

```text
A        把已暂存的改动 amend 到上次提交
```

`A` 大写，等价于 `git commit --amend`。

### 4.3 推送和拉取

任意面板：

```text
P        push 到上游分支（对应 git push）
p        pull 远程变更（对应 git pull）
```

如果当前分支没有上游，LazyGit 会询问目标分支名。

### 4.4 撤销和重做

任意面板：

```text
z        undo（撤销上一步操作）
Z        redo（Shift-z，恢复被撤销的操作）
```

`z` / `Z` 是 LazyGit 最有用的功能之一，它通过 reflog 记录操作。重要边界：

- 能撤销：commit、branch 创建 / 删除 / 检出 / 重命名、merge、rebase、cherry-pick、revert、reset。
- 不能撤销：工作树的文件改动、stash 内的改动、未跟踪文件（untracked）。
- 即使是 LazyGit 之外做的操作也能撤销，比如手动 `git reset` 之后打开 LazyGit 仍然能 `z` 回滚。

### 4.5 查看帮助

```text
?        打开键位菜单
```

按对应键再按 `?` 可以临时看该面板所有键位。

## 5. 提交历史编辑（Commits 面板）

把光标移到 `Commits` 面板：

```text
s        squash，把选中 commit 合并到下一个，message 一起合并
f        fixup，同上但丢弃选中 commit 的 message
r        重写 commit message
d        drop，删除该 commit
A        amend，把已暂存的改动追加到该 commit
i        基于该 commit 的父提交启动交互式 rebase
e        同上，启动交互式 rebase
S        把所有 fixup! / squash! 开头的 commit 自动 apply
t        在该 commit 上生成 revert commit
C        复制 commit（cherry-pick copy）
V        在当前分支上粘贴已复制的 commit（cherry-pick paste）
g        reset options，包含 mixed / soft / hard 三种
```

`s` 和 `f` 的区别：`squash` 会弹一个合并后的 message 编辑器让你修改，`fixup` 直接丢弃旧 message。

`i` / `e` 进入交互式 rebase 模式后，每个 commit 左侧会出现动作标记，用 `↑` / `↓` 切换 commit，用下面这些键设置动作：

```text
s        squash 到上一个 commit
f        fixup 到上一个 commit（丢弃 message）
d        drop（删除）
e        edit（停在该 commit 让你手动改）
p / w    pick（默认动作）
Ctrl-j   下移 commit 改变顺序
Ctrl-k   上移 commit 改变顺序
```

改完按 `m` 打开 rebase 选项菜单（continue / abort / skip），或者：

- 一切顺利：rebase complete 提示下按 `enter` 直接 continue。
- 出冲突：进入冲突解决 UI（见下文），resolve 完按 `enter` continue。
- 想中断：按 `m > abort`，或多次按 `esc`。

## 6. 分支管理（Branches 面板）

切到 `Branches` 面板：

```text
n        新建分支
space    检出选中分支
-        切回上一个检出的分支
M        把选中分支 merge 到当前检出分支
r        把当前检出分支 rebase 到选中分支
d        删除选中分支
R        rename / reset 上游
```

注意方向：

- `M` 是 "merge selected into current"，等价于 `git merge <selected>`。
- `r` 是 "rebase current onto selected"，等价于 `git rebase <selected>`。

新建分支后会立即检出。新建 + 切回 base 两步可以用 `n` 后再 `space` 回到原分支。

`d` 删除本地分支时，如果分支未合并，LazyGit 会确认是否强制删除（对应 `git branch -D`）。

## 7. 冲突解决

Merge 或 rebase 出现冲突时，Main 面板会切换到冲突视图。

```text
M        打开 merge / rebase 选项菜单（continue / abort / skip）
space    选择当前 hunk（保留当前分支版本）
b        选择所有 hunks（保留当前分支版本）
up / k   上一 hunk
down / j 下一 hunk
left / h 上一 conflict（一个文件可能有多个冲突点）
right / l 下一 conflict
z        撤销上一次 hunk 选择
e        用外部编辑器打开冲突文件
```

`space` 选择当前 hunk 意味着"保留当前分支的版本"，对应 `git checkout --ours`；再按一次 `space` 取消选择，对应 `git checkout --merge`（恢复到未解决的冲突标记）。

`M` 菜单里：

```text
continue    继续 merge / rebase（所有冲突标记都处理完后才能选）
abort       放弃 merge / rebase，回到操作之前的状态
skip        rebase 专属，跳过当前 commit
```

常见流程：

1. 进入冲突视图。
2. 用 `space` / `b` 选择保留的版本，或者直接按 `e` 在编辑器里手改。
3. 确认没有冲突标记残留（搜 `<<<<<<<`）。
4. 按 `enter` 或 `M > continue` 继续。
5. 如果搞砸了，`M > abort` 回到起点。

## 8. Stash

切到 `Stash` 面板：

```text
s        把当前 working tree 存进 stash
a        stash all，包括 untracked
A        stash all including ignored（不太常用）
g        pop stash
p        drop stash（删除）
space    apply stash（应用但不弹出）
```

stash 命名上 LazyGit 默认用 `WIP on <branch>`，需要自定义可以按 `r` 重命名。

## 9. Submodule

LazyGit 没有专门的 submodule 面板，操作 submodule 需要进入 submodule 目录再起一个 LazyGit 实例：

```bash
cd path/to/submodule
lazygit
```

或者在外层仓库的 `Files` 面板里：

```text
enter    进入 submodule 目录
```

按 `enter` 后 LazyGit 会切换上下文到该 submodule，可以在里面独立 stage / commit / push。最后按 `q` 退出，再回到外层仓库。

## 10. 配置文件

LazyGit 默认配置位置：

- macOS：`~/Library/Application Support/lazygit/config.yml`
- Linux：`~/.config/lazygit/config.yml`
- Windows：`%AppData%\lazygit\config.yml`

修改配置前先在 LazyGit 内按 `e` 打开当前配置，编辑保存即可生效。

覆盖机制：

- `CONFIG_DIR` 环境变量：换整个配置目录，同时影响 state 文件。
- `LG_CONFIG_FILE` 环境变量：指向具体配置文件路径。
- `--use-config-file <path>` 启动参数：同上。

常用自定义项：

```yaml
gui:
  theme:
    activeBorderColor:
      - green
      - bold
  showFileTree: true
  showRandomTip: false
git:
  autoFetch: true
  autoRefreshInterval: 60
  branchPatterns:
    - "*"
  skipHookPrefixes:
    - "skip-ci-"
```

## 11. 推荐工作流

### 11.1 一次普通提交

```text
1. 切到 Files 面板（按 2）
2. 选中文件 → space 暂存
3. 按 c 写 commit message → enter
4. 按 P 推到上游
```

### 11.2 修改最近一次 commit

```text
1. Files 面板继续修改文件
2. space 暂存新改动
3. 按 A（大写）amend 到上次 commit
4. 按 P 强制推送（force push 会被 LazyGit 拦截确认）
```

### 11.3 整理最近几个 commit

```text
1. 切到 Commits 面板（按 4）
2. 用 ↑ / ↓ 移动到要改的 commit
3. 按 i 进入交互式 rebase
4. 用 s / f / d / e 标记每个 commit 的动作
5. 用 Ctrl-j / Ctrl-k 调整顺序
6. 一切顺利按 enter continue；冲突时按 M > abort 重来
```

### 11.4 创建并切换分支

```text
1. 切到 Branches 面板（按 3）
2. 按 n，输入新分支名
3. 新分支自动检出
```

### 11.5 合并分支

```text
1. Branches 面板：选中要被合并的分支
2. space 检出目标分支（通常是 main）
3. 再次选中源分支
4. 按 M 把源分支 merge 到当前分支
```

### 11.6 处理合并冲突

```text
1. Main 面板切换到冲突视图
2. space / b 选保留版本，或按 e 用编辑器改
3. 搜 <<<<<<< 确认没有遗漏
4. enter 或 M > continue
```

## 12. 常见问题

### 撤销没法覆盖工作树改动

`z` / `Z` 是基于 reflog 的，只对 commit / branch 级别的操作有效。文件级别的修改需要手动恢复：

- 还没 stage：`git checkout -- <file>` 或 `git restore <file>`。
- 已经 stage：`git restore --staged <file>` 然后恢复。
- 完全乱了：放弃所有改动 `git checkout . && git clean -fd`（慎用，会丢未跟踪文件）。

### force push 被拦截

按 `P` 推送到上游时，如果远端历史已经被改写（被 rebase 过、被 push --force 过），LazyGit 会弹确认菜单让你选择：

- `force push`：覆盖远端。
- `force-with-lease push`：只有远端没有别人新改动时才覆盖，更安全。
- `cancel`。

推荐优先 `force-with-lease`。

### 启动后中文显示乱码

LazyGit 用 Nerd Font 才能正确显示图标。终端字体需要设为 Nerd Font 系列，例如 `JetBrainsMono Nerd Font` 或 `Hack Nerd Font`。

### 配色不喜欢

LazyGit 主题：

```text
:        打开命令面板
theme    选择预设主题
```

也可以直接在 config 里改 `gui.theme`。

### 怎么退出 sub-process

LazyGit 内部弹出的 vim / nano / git fetch 进程，按 `:q` 或 `Ctrl-c` 退出即可回到 LazyGit。

### commit 颜色含义

- 🟢 绿：该 commit 已包含在 master 分支。
- 🟡 黄：该 commit 未包含在 master 分支。
- 🔴 红：该 commit 还没 push 到上游。

这是 README FAQ 的官方说明，用来一眼判断当前 commit 状态。

## 13. 故障排查

### LazyGit 启动报 "not a git repository"

当前目录不在 git repo 内。`cd` 进 git root 或 `git init` 初始化一个。

### 启动后所有按键无反应

可能是 `$TERM` 不被 LazyGit 识别：

```bash
echo $TERM
```

期望值是 `xterm-256color` 或 `screen-256color`。临时切换：

```bash
TERM=xterm-256color lazygit
```

长期方案是在 shell rc 里 export：

```bash
export TERM=xterm-256color
```

### Mac 上 brew 装完后找不到命令

```bash
brew install lazygit
which lazygit
```

如果 `which` 没输出，确认 brew 的 bin 目录在 PATH 里：

```bash
brew --prefix
export PATH="$(brew --prefix)/bin:$PATH"
```

### tmux git pane 没启动 LazyGit

确认 LazyGit 已在 PATH 中：

```bash
command -v lazygit
```

如果输出为空，tmux git pane 会回退到 `git status --short --branch`，不会报错。手动启动：

```bash
lazygit
```

或修改 tmux 脚本里启动命令，让它使用绝对路径。

### 想看完整键位

在 LazyGit 内：

```text
?        主菜单
x        关闭弹窗
```

或者直接读仓库里的 keybindings 文档（和 LazyGit 内菜单同步）：

- `https://github.com/jesseduffield/lazygit/blob/master/docs/keybindings/Keybindings_en.md`

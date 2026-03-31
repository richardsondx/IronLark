# IronLark

[English](README.md) | [French](README.fr.md) | [Spanish](README.es.md) | [日本語](README.ja.md) | [中文文档](README.zh-CN.md)

IronLark 是一个面向 SSH 场景的 AI 终端运维助手。它的目标是在你已经登录到远程机器之后，直接在终端里帮助你检查、修复、监控并汇报问题，而不必离开当前会话。

本页是中文概览。最完整、最新的文档仍以英文版 `README.md` 为准。

## 为什么使用 IronLark

如果你希望有一个真正适合 SSH 会话的智能运维助手，IronLark 很适合下面这些场景：

- 检查服务器、日志、配置、进程、端口和代码仓库
- 在一次性命令和 `lk agent` 之间保持上下文
- 在后台持续执行恢复任务，之后再回来查看服务是否稳定
- 持续监控服务，保留证据，并处理明显的重启类故障
- 在本机保留 watcher、recovery、incident 和审计历史
- 使用 `lk ps` 作为紧急控制面板，快速停止卡住的运行

## IronLark 如何工作

IronLark 的设计重点是减少真实终端工作流中的摩擦：

- 它会先查看当前机器和仓库的本地上下文，再决定下一步
- 对于简单且安全的检查操作，它会直接执行，而不会频繁打断你
- 对于有风险的命令和文件修改，它会在明确的审批边界处停下
- 它会记住你刚刚发现的内容，让后续提问不会像从零开始
- 它会把后台任务、incident 和恢复历史保留在本机，方便之后追溯

它的目标不是在终端里充当一个泛用聊天机器人，而是帮助你更快地从“这台机器出问题了”走到“我知道发生了什么、改了什么、接下来该做什么”。

## IronLark 最适合的场景

IronLark 特别适合：

- 通过 SSH 排查在线服务器问题
- 恢复服务并在稍后回来查看结果
- 在多个终端会话之间跟踪 incident
- 直接在目标机器上谨慎地编辑配置文件

如果你的重点是更广泛的 IDE 驱动开发工作流，请参考英文版 `README.md` 中更完整的说明。

## 快速开始

### 本地机器

```bash
curl -fsSL https://raw.githubusercontent.com/richardsondx/IronLark/main/install.sh | sh
mkdir -p ~/.config/lark
cat > ~/.config/lark/.env <<'EOF'
OPENAI_API_KEY=your_key_here
EOF
lk init
lk version
lk model
lk config test
lk "hello"
```

### 通过 SSH 登录远程服务器

```bash
ssh root@your-server-ip
curl -fsSL https://raw.githubusercontent.com/richardsondx/IronLark/main/install.sh | sh
lk init
lk "what can you help me do on this server?"
lk agent
```

## 运维工作流

### 恢复服务

```bash
lk recover "restore openclaw and keep going until it is stable"
```

### 监控服务

```bash
lk watch openclaw
```

### 查看后台任务

```bash
lk ps
lk watch list
lk recover list
```

## 常用命令

- `lk "task"`：默认 execute-first 的一次性任务
- `lk --plan "task"`：执行前先显示可见计划
- `lk agent`：面向 SSH 的交互式会话
- `lk edit <path> [instruction]`：带 diff 审核的文件编辑
- `lk run "<command>"`：带安全边界的 shell 命令执行
- `lk context`：查看当前持久上下文
- `lk policy list`：查看本机策略规则
- `lk ps`：查看正在运行的 IronLark 进程

## Open Source

- 许可证：GNU Affero General Public License v3.0 (AGPL-3.0)
- 命令保持为：`lark` 和 `lk`
- 项目名称：IronLark

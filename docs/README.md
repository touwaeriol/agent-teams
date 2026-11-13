# agent-teams 设计文档

agent-teams 是一个 Web 平台：把多台服务器上的 Claude Code、Codex 组织成“员工”，按项目编组，由每个项目的“项目统筹”和人对话、写计划、派任务，员工在各自节点上完成任务并把代码交付到 Git 仓库。

界面设计稿（可点击原型 + 8 张流程图）：https://claude.ai/artifact/6E413CWL7EMq59NYC8qn44

## 文档目录

| 文档 | 内容 |
| --- | --- |
| [01-architecture.md](01-architecture.md) | 组件、部署形态、技术选型、Web API 概要 |
| [02-data-model.md](02-data-model.md) | 数据表 |
| [03-daemon-protocol.md](03-daemon-protocol.md) | 节点守护进程与控制面之间的协议 |
| [04-mcp.md](04-mcp.md) | 对外 MCP 服务和员工内部工具 |
| [05-scheduling.md](05-scheduling.md) | 任务派发、槽位、通道、负载均衡、员工状态机 |
| [06-memory.md](06-memory.md) | 员工记忆、任务交接、项目知识 |
| [07-git.md](07-git.md) | Git 连接与凭证签发 |
| [08-agent-adapters.md](08-agent-adapters.md) | Claude Code / Codex 适配器 |
| [09-mvp.md](09-mvp.md) | 第一版范围、里程碑、需要先验证的事 |
| [10-ui.md](10-ui.md) | 页面清单及各页用到的数据 |

## 术语

| 术语 | 含义 |
| --- | --- |
| 控制面 | Web 服务端：API、MCP、调度器、存储、凭证签发。所有状态以控制面为准。 |
| 节点 | 一台服务器。上面跑一个守护进程，登录一个 Claude Code 账号（也可以有一个 Codex）。 |
| 守护进程 | `agent-teams-daemon`，每个节点一个，主动用 WebSocket 连控制面。负责扫描运行时、建员工目录、启动和监管会话、转发事件、提供 Git 凭证 helper 和员工的本地 MCP 工具。 |
| 运行时 | 节点上扫描到的一个 agent 安装：类型（Claude Code / Codex）、路径、版本、登录状态、额度、槽位。 |
| 槽位 | 一个运行时同时能跑的会话数。额度和机器资源都由它间接控制。 |
| 员工模板 | 平台级。定义身份、Agent 类型、提示词模板、权限、技能、命名规则、办公室位置。 |
| 身份 | 模板的大类：统筹、决策、执行、验收。决定默认权限、能接的任务、能用的工具。 |
| 项目 | 若干 Git 仓库（代码存档点）+ 按模板生成的员工 + 一个项目统筹 + 一个对话。 |
| 员工 | 项目里按模板、在某个节点上生成的实例。等于节点上的一个目录，同一时间最多一个会话。 |
| 项目统筹 | 身份为“统筹”的员工，每个项目恰好一个。人只和它对话，它写计划、派任务、汇总。 |
| 会话 | 员工的一次 agent 进程运行（Claude Code 会话或 Codex 线程），可以停止和续跑。 |
| 子代理 | 会话内部由 agent 自己派生的并行（subagent / agent team）。平台只记账，不调度。 |
| 通道 | 项目内按角色组限制同时工作的人数，例如 decision 通道最多 3 人。 |
| 请求 | 员工等待人或统筹答复的权限请求或提问。 |
| 任务交接 | 当前任务的进度文件 `HANDOFF.md`，会话重启后靠它接着做。 |

## 和设计稿的差异

- 设计稿里目录前缀写的是 `~/arw`，文档统一为 `~/agent-teams`。
- 第一版只支持 GitHub：仓库通过粘贴 `https://github.com/owner/repo` 链接添加。GitLab、其他 HTTPS 仓库、SSH 协议以后再加（设计稿里出现的 GitLab / Gitea 只是以后的样子）。见 [07-git.md](07-git.md)。

## 和 agent-remote-worker 的关系

[agent-remote-worker](https://github.com/touwaeriol/agent-remote-worker) 是一个 Claude Code 插件，主会话经 SSH 把任务派给其他机器上的 `claude --bg` 会话。agent-teams 吸收它已验证的做法：

- worktree 隔离、交付分支、推送前 rebase；
- 权限请求和提问的编号、原地等待、答复回写；
- `claude -p /usage --output-format json` 读额度；
- 自动信任员工根目录（写 `~/.claude.json` 的 `hasTrustDialogAccepted`）的规则和限制。

不同之处：控制面不再经 SSH 执行命令，改为守护进程主动连接；会话由守护进程持有进程（stream-json），不依赖未公开的收件箱消息格式。

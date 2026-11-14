# agent-teams

在你自己的服务器上组建 Claude Code 团队：你只和一个负责人（leader）对话，他把需求拆成任务，派给分布在各台机器上的员工并行完成，结果自动回到他手里，再由他向你汇报。

它既是一个**多用户 SaaS 平台**（网页注册、各自管理自己的服务器和团队、管理员后台），也是一个**可组合的多智能体 Python 库**（流水线、并行、讨论、主管、不阻塞的 leader）。

## 平台怎么用

```
你的浏览器 ──▶ 控制台（agent-teams serve）◀──WebSocket── 节点 A：agent-teams node ─ claude（leader、员工）
                     │  PostgreSQL / Valkey               节点 B：agent-teams node ─ claude（员工）
                     └─ /mcp：leader 通过 MCP 调用任务板
```

1. **注册登录**：开放注册（管理员可以关闭）。第一个注册的账号，以及 `AGENT_TEAMS_ADMIN_EMAILS` 里的邮箱，自动成为管理员。
2. **添加节点**：节点是你自己的服务器，先装好并登录 Claude Code（`claude` 命令可用）。在"节点"页添加后会显示一条安装命令（令牌只显示一次），在节点上执行即可：

   ```bash
   curl -fsSL http://控制台/install-node.sh | AGENT_TEAMS_NODE_TOKEN=… bash -s -- --console http://控制台 --name gpu-1
   ```

   守护进程以 systemd 用户服务运行，主动连回控制台，节点不用开放端口；网页上能看到它是否在线、正在运行几个员工。令牌可以随时更换或删除，旧连接会立刻断开。
3. **配置团队**：在"团队"页写 YAML，用 `leader` 选出负责人，每个成员用 `node` 指定在哪个节点上运行：

   ```yaml
   leader: lead
   employees:
     - name: lead                     # 负责人：节点 gpu-1 上一个持续的 Claude Code 会话
       type: claude_code
       node: gpu-1
       instructions: 你是技术负责人。把需求拆成任务派给工程师，检查结果后用中文汇报。
     - name: backend
       type: claude_code
       node: gpu-1
       description: 后端工程师：改 API、跑测试
       allowed_tools: ["Bash(git *)", "Bash(pytest *)"]
     - name: frontend
       type: claude_code
       node: gpu-2
       description: 前端工程师
   ```

   保存时会校验：节点要存在，负责人必须是 `claude_code`（平台不提供 API key，所有模型调用都走你节点上自己登录的 Claude Code）。
4. **对话**：新建对话，直接说需求。负责人通过 MCP 工具 `assign_task` 派任务，派完立刻回复你；员工在各自的目录（`<root>/<员工名>`）里干活，你可以继续聊。任务完成后报告自动送回负责人，他再向你汇报。右侧任务板实时显示每个任务的状态。
5. **管理员**：在"管理"页管理用户（改角色、停用、删除、单独设置节点上限）和平台设置（开放注册、默认节点上限）。停用一个用户会让他立刻下线、断开他的节点并停止他的对话。

**可靠性**
- 节点断网、控制台重启或升级，员工都照常运行。守护进程会把输出写到本地日志，控制台恢复后续传或回放；"进行中"的任务会自动接回，不会重新执行。
- 负责人是一个持续的 Claude Code 会话：第一轮用 `--session-id` 建立，之后每轮用 `--resume` 接着聊，对话历史由节点上的 Claude Code 自己保存。
- 节点权限很窄：控制台只能让节点在 `--root` 目录下启动 `claude`/`codex`，其他命令和目录一律拒绝。取消任务时，守护进程会结束这个员工的整个进程组。

**数据隔离**：每个用户只能看到自己的对话、节点和团队。一个对话的 MCP 令牌只能操作这个对话的任务板；节点令牌在数据库里只存哈希。

## 部署

需要 Docker，以及服务器本机已有的 PostgreSQL 和 Valkey（或 Redis）。

```bash
git clone <仓库地址> agent-teams && cd agent-teams
createdb agent_teams            # 或者在 .env 里指向一个已有的库（表名都带 at_ 前缀）
deploy/deploy.sh                # 第一次运行生成 .env，提示你填 DATABASE_URL
deploy/deploy.sh                # 再运行一次：构建、启动，打印访问地址
```

- `deploy.sh` 会自动选 3001–3009 中第一个空闲端口，并按本机 IP 生成 `AGENT_TEAMS_PUBLIC_URL`。节点安装命令和 leader 的 MCP 地址都用这个地址，放在反向代理后面时要改成对外地址。
- 容器使用 host 网络，所以通过 `127.0.0.1` 直接连本机的 PG 和 Valkey。
- 查看日志：`docker compose logs -f agent-teams`；升级：`git pull && deploy/deploy.sh`。
- 服务是明文 HTTP，对公网开放时建议在前面加一层 Nginx/Caddy 提供 HTTPS（登录 cookie 和节点令牌都经过它）。

不用 Docker 时：`pip install ".[server]"`，设置好上述环境变量后运行 `agent-teams serve`。

## 作为 Python 库使用

```bash
pip install -e ".[dev]"     # 只用库和命令行：pip install -e .；网页服务：".[server]"；节点守护进程：".[node]"
```

```python
import asyncio
from agent_teams import Agent, AnthropicLLM, Leader

llm = AnthropicLLM()  # 默认 claude-opus-5-5、自适应思考、high effort；需要 ANTHROPIC_API_KEY
writer = Agent("writer", llm, description="写手", instructions="写出要求的内容。")
critic = Agent("critic", llm, description="审查", instructions="指出文字里的问题。")
lead = Leader("lead", llm, [writer, critic])  # 不阻塞：派任务立即返回，员工并行

print(asyncio.run(lead.run("写一首关于秋天的短诗")).output)
```

命令行（YAML 定义团队，节点用 SSH 或本机）：

```bash
agent-teams check examples/multi_node.yaml
agent-teams chat  examples/company.yaml -v        # 和负责人对话，-v 显示派活过程
agent-teams run   examples/writing_team.yaml "Write about tidal energy"
python examples/offline_leader.py                 # 不需要 API key 的演示
```

| 编排 | 行为 |
|---|---|
| `Leader` | 负责人 + 不阻塞的任务板：派任务立即返回，员工后台并行，结果自动回报。`brain` 可以是 LLM，也可以是节点上的 Claude Code |
| `Sequential` | 流水线：依次执行，后一个成员拿到前一个的输出 |
| `Parallel` | 所有成员并发处理同一任务，可选 `aggregator` 汇总 |
| `RoundRobin` | 成员轮流发言，可选 `finalizer` 总结 |
| `supervisor()` | 每个成员是主管的工具，委派是同步的 |

成员类型：`agent`（LLM，需要 API key）、`claude_code`（节点上的 Claude Code）。所有团队都实现 `Runnable.run(task)`，可以任意嵌套。工具就是普通函数（`@tool`，schema 由类型注解生成）。`Hooks` 可以观察运行过程中的每个事件。

## 项目结构

```
src/agent_teams/
  agent.py, leader.py, teams.py   Agent、不阻塞的 Leader、其他编排策略
  tasks.py, conversation.py       任务板（依赖、取消、完成通知）、消息队列
  claude_code.py                  ClaudeCodeWorker：在节点上执行 claude -p 的员工
  claude_session.py               ClaudeCodeSession：节点上持续的 Claude Code 会话（leader）
  nodes.py, remote.py, daemon.py  节点：SSH/本机、守护进程节点（控制台侧）、守护进程
  tools.py, hooks.py, config.py   工具、事件、YAML 配置
  llm/                            LLM 协议、AnthropicLLM、ScriptedLLM
  server/                         平台：app/auth/routes/admin、accounts/store（PG）、
                                  hub、workspaces、mcp_bridge、broker（Valkey）、static
```

## 开发

```bash
ruff format . && ruff check .   # 圈复杂度 ≤ 6、嵌套 ≤ 3
mypy                            # strict
pytest                          # 不调用真实 API；平台端到端测试用本地 uvicorn + 假 claude
TEST_DATABASE_URL=postgresql://… TEST_VALKEY_URL=valkey://… pytest   # 加上 PG/Valkey 集成测试
```

## 许可证

MIT

# agent-teams

基于 Claude API 的可组合多智能体团队框架：用几行 Python 或一个 YAML 文件，把多个 Agent 组织成流水线、并行评审、轮流讨论或主管分派的团队，并且团队可以任意嵌套。

## 特性

- **统一接口（组合模式）**：`Agent` 和所有团队都实现 `Runnable.run(task) -> Result`，团队可以包含团队。
- **主员工模式**：选一个 leader 和他对话，他派任务给员工，派发不阻塞，结果自动回报。
- **四种编排策略**：`Sequential`（流水线）、`Parallel`（并发 + 可选汇总）、`RoundRobin`（轮流讨论 + 可选裁决）、`supervisor`（主管通过工具分派给成员）。
- **零侵入工具**：普通函数（同步或异步）即工具，参数 schema 由类型注解和 docstring 自动生成，用 pydantic 校验；需要上下文时声明一个 `RunContext` 参数即可注入。
- **可替换模型后端**：Agent 只依赖 `LLM` 协议；内置 `AnthropicLLM`（Claude）和 `ScriptedLLM`（测试/离线演示）。
- **观察者钩子**：`Hooks` 订阅运行、模型回复、工具调用等事件，不改业务代码；钩子异常只记录日志，不影响运行。
- **声明式配置**：YAML/JSON 定义团队，pydantic 严格校验，附带 CLI。

## 安装

```bash
pip install -e ".[dev]"        # 只用命令行：pip install -e .；网页服务：pip install -e ".[server]"
```

需要 Python 3.10+。

每个员工背后都是一次 Claude 模型调用，程序必须带凭据才能调用模型。二选一即可：

- 设置环境变量 `ANTHROPIC_API_KEY`；
- 或运行一次 `ant auth login`，在浏览器里登录，SDK 会自动读取登录信息，不需要复制 key。

## 和主员工对话（不阻塞）

列出所有节点，用 `leader` 选一个当负责人。你只和 leader 对话，**整个过程不阻塞**：

1. leader 把需求拆成任务派给员工（`assign_task`），派完**立刻**回复你，员工在后台干活；
2. 员工干活期间你可以继续跟 leader 聊、追加需求、问进度；
3. 互不依赖的任务并行执行；需要别人结果的任务用 `depends_on` 声明，前置任务完成后自动开始，并拿到前置任务的结果；
4. 每个任务完成（或失败、被取消）后，结果自动送回 leader；他检查结果、决定是否派后续任务，全部完成后向你汇报。

```yaml
# examples/company.yaml
llm: {model: claude-opus-5-5}
leader: lead
nodes:
  - {name: lead, instructions: "你是团队负责人……"}
  - {name: researcher, description: "调研员", instructions: "……"}
  - {name: writer, description: "写手", instructions: "……"}
  - {name: reviewer, description: "审查员", instructions: "……"}
```

```bash
agent-teams chat examples/company.yaml -v     # -v 实时显示任务派发和完成
```

```
Talking to lead. /tasks shows progress, /reset clears, /exit quits.
you> 调研一下三家竞品，然后写一份对比方案
lead: 已安排 researcher 调研（t1），writer 等调研完成后写方案（t2），完成后向你汇报。
you> 顺便把预算控制在 10 万以内
lead: 好的，已记下，会在方案里体现。
you> /tasks
t1 [done] researcher: 调研三家竞品……
t2 [running] writer (after t1): 写对比方案……
lead: 方案完成了：……
```

- `/tasks` 查看任务板，`/reset` 清空对话，`/exit` 立即退出（取消未完成的任务）；输入结束（Ctrl-D 或管道输入）时会等所有任务完成、leader 汇报完再退出。
- 换 leader 只需改 `leader:` 这一行；leader 必须是普通 agent 节点。
- 员工节点可以是任意类型，比如一个 `parallel` 评审小组，对 leader 来说就是一个员工。
- leader 看到的员工名单来自每个节点的 `description`，写清楚他才知道该派给谁。
- 某条消息出错（比如被拒答）时，这一轮会被回滚，对话可以继续；某个任务失败时，失败原因会报告给 leader，依赖它的任务也会标记为失败。

在 Python 里：

```python
conversation = leader.open()  # leader = Leader(...) 或 load_team(...)
conversation.post("调研竞品并写方案")  # 只入队，不等待
reply = await conversation.reply()  # 处理下一条消息（你的消息或任务报告）
print(conversation.status())  # 任务板
```

一次性运行（`agent-teams run` 或 `await leader.run(task)`）也走同样的流程：等到所有任务完成、leader 给出最终汇报后才返回。

## 网页版与部署（Docker Compose）

`agent-teams serve` 提供网页聊天界面和 HTTP API：左边是对话列表，中间和 leader 聊天，右边任务板实时显示每个任务的状态。

- **PostgreSQL**：保存对话、消息、任务和 leader 的对话历史；服务重启后可以接着聊，重启前没做完的任务会标记为"已中断"。表名都带 `at_` 前缀，可以放进现有数据库。
- **Valkey**：通过发布/订阅把回复和任务状态实时推送到浏览器（SSE）。
- **访问令牌**：所有 API 都需要 `AGENT_TEAMS_TOKEN`。网页登录一次后换成 HttpOnly cookie，令牌不会出现在 URL 和日志里。

部署到一台已经运行 PostgreSQL 和 Valkey 的服务器上：

```bash
git clone <仓库地址> agent-teams && cd agent-teams
git checkout claude/vigilant-noether-11aqhk
createdb agent_teams            # 或者在 .env 里指向一个已有的库
deploy/deploy.sh                # 第一次运行会生成 .env，提示你填 ANTHROPIC_API_KEY 和 DATABASE_URL
deploy/deploy.sh                # 填好后再运行一次：构建镜像、启动、输出访问地址和令牌
```

`deploy.sh` 会自动选 3001–3009 中第一个空闲端口、生成访问令牌，并在服务通过健康检查后打印 `http://<服务器IP>:<端口>`。容器使用 host 网络，所以通过 `127.0.0.1` 直接连本机的 PG 和 Valkey，不用改它们的监听地址。

- 改团队：编辑仓库根目录的 `team.yaml`（第一次部署时从 `examples/company.yaml` 复制），然后执行 `docker compose restart`。
- 看日志：`docker compose logs -f agent-teams`；更新代码：`git pull && deploy/deploy.sh`。
- 服务是明文 HTTP；需要公网访问时，建议放在 Nginx/Caddy 后面加 HTTPS。

## 快速开始

### Python

```python
import asyncio
from agent_teams import Agent, AnthropicLLM, supervisor, tool


@tool
def word_count(text: str) -> int:
    """Count words in text.

    Args:
        text: The text to measure.
    """
    return len(text.split())


llm = AnthropicLLM()  # 默认 claude-opus-5-5、自适应思考、high effort
writer = Agent(
    "writer",
    llm,
    description="Writes drafts.",
    instructions="Write what you are asked to.",
    tools=[word_count],
)
critic = Agent(
    "critic",
    llm,
    description="Reviews drafts.",
    instructions="Point out weaknesses in the text you receive.",
)
lead = supervisor("lead", llm, [writer, critic])

print(asyncio.run(lead.run("写一首关于秋天的短诗")).output)
```

### YAML + CLI

```yaml
# examples/writing_team.yaml
llm: {model: claude-opus-5-5, effort: high}
team:
  type: sequential
  name: writing-team
  members:
    - {name: researcher, instructions: "Collect the key facts as bullets."}
    - {name: writer, instructions: "Turn the notes into a 500-word article."}
```

```bash
agent-teams check examples/writing_team.yaml
agent-teams run examples/writing_team.yaml "Write about tidal energy" --verbose
echo "长任务文本" | agent-teams run examples/writing_team.yaml -
```

不需要 API key 的离线演示：

```bash
python examples/offline_demo.py     # 流水线 + 并行
python examples/offline_leader.py   # 不阻塞的 leader
```

## 编排策略

| 策略 | 行为 | 主要参数 |
|---|---|---|
| `Sequential` | 依次执行，后一个成员拿到原始任务和前一个成员的输出 | `handoff` |
| `Parallel` | 所有成员并发处理同一任务；有 `aggregator` 时由它汇总 | `aggregator`, `merge` |
| `RoundRobin` | 成员按轮次发言，每次都能看到完整记录；有 `finalizer` 时由它总结 | `rounds`, `finalizer`, `merge` |
| `Leader` | 负责人 + 不阻塞的任务板：派任务立即返回，员工后台并行，结果自动回报 | `instructions`, `max_turns` |
| `supervisor()` | 返回一个 Agent，每个成员都是它的工具；委派是同步的（等员工做完才继续） | `instructions`, `tools` |

`handoff` / `merge` 都是普通函数，可以直接替换以定制信息传递方式，不需要继承。

YAML 中的节点 `type` 可以是 `agent`（默认）、`leader`、`sequential`、`parallel`、`round_robin`、`supervisor`。工具用 `"module:attribute"` 引用，可以是普通函数，也可以是 `Tool`。每个节点都可以用 `llm:` 覆盖全局模型配置。

## 扩展点

- **自定义模型后端**：实现 `async complete(system, history, tools) -> Reply`，即满足 `LLM` 协议。
- **自定义团队**：继承 `Team`（或 `Runnable`），实现 `_run(task, ctx)`。
- **观察运行过程**：

  ```python
  from agent_teams import Hooks, RunContext, ToolStarted, trace

  hooks = trace(Hooks())  # 内置单行追踪
  hooks.on(ToolStarted, lambda e: ...)  # 或订阅具体事件
  await team.run("task", RunContext(hooks))
  ```

- **透传 API 参数**：`AnthropicLLM(options={...})` 可以传入 Messages API 支持的任意参数。

## Claude 后端默认行为

`AnthropicLLM` 默认使用流式请求（避免长输出超时）、`thinking: {type: "adaptive"}`、`effort: high`，并开启服务端拒答回退（`fallbacks: "default"`，beta `server-side-fallback-2026-07-01`）。如果不需要回退，传 `fallbacks=False` 即可。思考块会原样回传，以保持多轮推理的连续性。

## 项目结构

```
src/agent_teams/
  types.py      与模型无关的数据类型（Turn、ToolCall、Reply、Result）
  runnable.py   Runnable 基类（统一入口、事件）
  agent.py      Agent：工具调用循环
  teams.py      Sequential / Parallel / RoundRobin / supervisor
  leader.py     Leader：负责人 + 任务板
  tasks.py      TaskBoard：不阻塞派发、依赖、取消、完成通知
  conversation.py  Conversation：消息队列，用户和任务报告按序交给 leader
  server/       网页服务：FastAPI、PostgreSQL 存储、Valkey 推送、静态页面
  tools.py      @tool、as_tool、Toolkit（并发执行、失败隔离）
  hooks.py      事件与 Hooks、trace
  context.py    RunContext（钩子 + 调用路径）
  config.py     YAML/JSON 配置与校验
  cli.py        命令行
  llm/          LLM 协议、AnthropicLLM、ScriptedLLM
```

## 开发

```bash
ruff format . && ruff check .   # 格式与规范（含圈复杂度 ≤ 6、嵌套 ≤ 3）
mypy                            # strict 类型检查
pytest                          # 单元测试（不调用真实 API）
```

## 许可证

MIT

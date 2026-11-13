# agent-teams

基于 Claude API 的可组合多智能体团队框架：用几行 Python 或一个 YAML 文件，把多个 Agent 组织成流水线、并行评审、轮流讨论或主管分派的团队，并且团队可以任意嵌套。

## 特性

- **统一接口（组合模式）**：`Agent` 和所有团队都实现 `Runnable.run(task) -> Result`，团队可以包含团队。
- **四种编排策略**：`Sequential`（流水线）、`Parallel`（并发 + 可选汇总）、`RoundRobin`（轮流讨论 + 可选裁决）、`supervisor`（主管通过工具分派给成员）。
- **零侵入工具**：普通函数（同步或异步）即工具，参数 schema 由类型注解和 docstring 自动生成，用 pydantic 校验；需要上下文时声明一个 `RunContext` 参数即可注入。
- **可替换模型后端**：Agent 只依赖 `LLM` 协议；内置 `AnthropicLLM`（Claude）和 `ScriptedLLM`（测试/离线演示）。
- **观察者钩子**：`Hooks` 订阅运行、模型回复、工具调用等事件，不改业务代码；钩子异常只记录日志，不影响运行。
- **声明式配置**：YAML/JSON 定义团队，pydantic 严格校验，附带 CLI。

## 安装

```bash
pip install -e ".[dev]"
```

需要 Python 3.10+。调用 Claude 前需要设置 `ANTHROPIC_API_KEY`（或用 `ant auth login` 登录）。

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
writer = Agent("writer", llm, description="Writes drafts.",
               instructions="Write what you are asked to.", tools=[word_count])
critic = Agent("critic", llm, description="Reviews drafts.",
               instructions="Point out weaknesses in the text you receive.")
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
python examples/offline_demo.py
```

## 编排策略

| 策略 | 行为 | 主要参数 |
|---|---|---|
| `Sequential` | 依次执行，后一个成员拿到原始任务和前一个成员的输出 | `handoff` |
| `Parallel` | 所有成员并发处理同一任务；有 `aggregator` 时由它汇总 | `aggregator`, `merge` |
| `RoundRobin` | 成员按轮次发言，每次都能看到完整记录；有 `finalizer` 时由它总结 | `rounds`, `finalizer`, `merge` |
| `supervisor()` | 返回一个 Agent，每个成员都成为它可调用的工具 | `instructions`, `tools` |

`handoff` / `merge` 都是普通函数，可以直接替换以定制信息传递方式，不需要继承。

YAML 中的节点 `type` 可以是 `agent`（默认）、`sequential`、`parallel`、`round_robin`、`supervisor`。工具用 `"module:attribute"` 引用，可以是普通函数，也可以是 `Tool`。每个节点都可以用 `llm:` 覆盖全局模型配置。

## 扩展点

- **自定义模型后端**：实现 `async complete(system, history, tools) -> Reply`，即满足 `LLM` 协议。
- **自定义团队**：继承 `Team`（或 `Runnable`），实现 `_run(task, ctx)`。
- **观察运行过程**：

  ```python
  from agent_teams import Hooks, RunContext, ToolStarted, trace

  hooks = trace(Hooks())                   # 内置单行追踪
  hooks.on(ToolStarted, lambda e: ...)     # 或订阅具体事件
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

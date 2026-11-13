# 05 调度

调度器在控制面，纯逻辑放 `packages/core`，输入是当前状态快照，输出是要执行的动作，方便单测。

## 三层限制

一个员工要“上工位”（启动会话），下面三层都要满足：

| 层 | 限制 | 数据 |
| --- | --- | --- |
| 节点 | 运行时在线、已登录、额度剩余 ≥ `min_remaining_pct`；运行时活跃会话 < `slots`；节点活跃会话 < `max_sessions` | runtimes、sessions |
| 项目 | 员工所属通道活跃人数 < `max_active` | project_channels |
| 员工 | 状态为 `idle`（没被暂停、节点在线），同一时间只有一个会话 | employees |

例外：**项目统筹不受通道限制，并且优先占用槽位**（排在所有排队任务前面），否则人在对话里说话可能半天没人回。统筹每轮处理完就停，占用时间短。

子代理不占槽位，但消耗同一账号的额度；额度检查会把它算进去（额度是账号整体的）。

## 派发流程

触发时机：新任务、任务依赖完成、会话结束、节点上线、额度刷新、配置变化、员工恢复。每次触发对受影响的项目跑一遍：

```
for task in 项目里 state ∈ {pending, queued} 且依赖都完成, 按 (priority desc, created_at asc):
    candidates = 符合 task.requirement 的员工
                 且 status = idle
                 且 所在节点 / 运行时 / 通道 满足三层限制
    if 点名了员工:
        该员工可用 → 分配；否则 task.state = queued（等这个人）
    elif candidates 为空:
        task.state = queued
    else:
        employee = pick(candidates, project.balance_strategy)
        分配：task.assignee = employee，task.state = running，下发 session.start
```

`requirement` 的匹配：

- `{identity}`：项目里该身份的所有员工；
- `{template}`：该模板生成的员工；
- `{employee}`：点名。

## 负载均衡（pick）

| 策略 | 规则 |
| --- | --- |
| `quota`（默认） | 选所在运行时**额度剩余最多**的员工；相同时选所在节点活跃会话最少的；再相同选空闲最久的 |
| `round_robin` | 按 (项目, 模板) 记一个节点游标，轮流选下一个节点上的空闲员工 |
| `prefer_node` | 优先 `prefer_node_id` 上的员工，满了再按 `quota` 选 |

`quota` 的效果是几个节点的账号消耗趋于一致。运行时没有额度信息（例如 Codex 按 API 计费）时，该员工的额度视为 100%，退化为按活跃会话数均衡；这类模板建议直接用 `round_robin`。

## 粘性：返工回到原员工

任务一旦开始，`assignee` 固定。返工、追问、人或统筹给这个任务发的消息，都送给同一个员工：它的会话记录和 worktree 在那台机器上，换人会丢上下文。

- 原员工忙（在做别的任务）：消息排进它的队列，当前任务结束后处理。
- 原员工所在节点长时间离线：任务转为 `blocked`，由人选择“等节点恢复”或“交给别人重做”（后者新员工从 HANDOFF.md 和交付分支接着做）。

## 员工状态机

状态存在 `employees.status`，由会话事件和调度动作驱动。办公室里人物站在哪由状态直接决定。

| 状态 | 含义 | 办公室位置 |
| --- | --- | --- |
| `provisioning` | 守护进程在建目录 | 不显示（或门口） |
| `idle` | 空闲 | 休息区沙发 |
| `queued` | 有任务派给它，但节点槽位 / 通道 / 额度不满足 | 等候凳 |
| `working` | 会话运行中（可带子代理） | 工位 |
| `waiting` | 会话停在权限请求或提问上 | 工位，举手气泡 |
| `blocked` | 汇报受阻或会话出错，等人处理 | 维修台 |
| `paused` | 人手动暂停接任务；当前一轮做完后停下 | 休息区沙发（带暂停标记） |
| `offline` | 所在节点离线 | 原地灰显 |
| `removing` | 正在回收 | 不显示 |

| 从 | 到 | 触发 |
| --- | --- | --- |
| idle | working | 分配任务且三层限制满足 |
| idle | queued | 点名分配给它，但限制不满足 |
| queued | working | 限制满足 |
| working | waiting | `request.open` |
| waiting | working | 请求被答复 |
| working | idle | 任务 `done`，或统筹一轮处理完 |
| working | blocked | `report(blocked)` 或 `session.error` |
| blocked | working / idle | 人重试 / 放弃或改派 |
| 任意 | offline | 节点离线 |
| offline | 离线前的状态 | 节点重连并对账后 |
| 任意 | paused | 人点暂停（working 时先做完这一轮） |

“排队”有两种，界面上要分清：**任务排队**（没有可用员工，任务在项目队列里）和**员工排队**（员工被点名但条件不满足，人坐在等候凳上）。

## 会话生命周期细节

- **启动**：调度器写 `sessions(state=starting)` → 下发 `session.start` → 守护进程回 `ok` 后 `running`。
- **一轮结束**：`turn.finished` 后，若员工没调用 `report(done)` 且还有待处理消息，继续送下一条；否则会话进入 `idle`（进程保持一段时间，默认 10 分钟无消息后停止，下次续跑）。
- **上下文快满**：`usage.context_pct ≥ 80` 时，控制面给会话发一条系统消息：“上下文快满了，先用 write_handoff 更新交接”。Claude Code 自己压缩后，SessionStart 钩子会重新注入记忆和交接（见 [06-memory.md](06-memory.md)）。
- **续跑失败**（会话记录损坏、版本不兼容）：开新会话，靠记忆 + HANDOFF.md 接上，`end_reason = context_restart`。

## 额度刷新

守护进程每 30 分钟、以及每个会话结束时读一次额度（Claude Code：`claude -p /usage --output-format json`，不消耗额度）。低于 `min_remaining_pct` 的运行时不再分配新会话，已有会话不受影响；办公室里该节点的机柜显示“额度不足”。

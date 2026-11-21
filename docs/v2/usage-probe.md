# 订阅余量的数据来源（node.usage）

节点守护进程会上报本机 Claude Code、Codex 登录的订阅余量（protocol.md §9.1 的 `node.usage`），节点页据此显示每个节点还剩多少额度。本文写清楚每个数字从哪里来、是不是官方接口、可靠程度和版本兼容风险。调研基于 2026-11-19 的锁定版本：`@anthropic-ai/claude-agent-sdk` 0.3.325（自带 Claude Code CLI 2.1.325）和 `@openai/codex` 0.169.0。

## 结论

| 运行时 | 来源 | 是否公开 | 可靠程度 | 版本兼容风险 |
| --- | --- | --- | --- | --- |
| Claude Code（订阅登录） | SDK `Query.usage_EXPERIMENTAL_MAY_CHANGE_DO_NOT_RELY_ON_THIS_API_YET({skipBehaviors:true})`，也就是 CLI 的 `get_usage` 控制请求，和交互式 `/usage` 用的是同一份数据 | SDK 公开导出，但明确标为**实验性**（名字和返回结构都可能改） | 高：数字和 `/usage` 界面一致；CLI 自己负责凭证和刷新 | **中到高**：升级 SDK 时方法可能改名或删除。降级处理见下文 |
| Codex（ChatGPT 登录） | `codex app-server` 的 JSON-RPC `account/rateLimits/read`；读失败时退回到最近一次 `account/rateLimits/updated` 推送 | app-server 协议是 Codex 官方给 IDE 插件用的接口，有生成的 TS 类型（`node/src/codex-protocol`），不算公开的 REST API | 高：就是 Codex 自己 `/status` 显示的那份数据 | 低到中：方法在 v2 协议里，有 `gen:codex` 生成类型，升级时能在 typecheck 阶段发现字段变化 |
| API Key 登录或未登录 | 不请求任何接口 | — | — | — |

## 1. Claude Code

### 1.1 选用的方法：SDK 的 get_usage 控制请求

SDK 0.3.325 的 `Query` 有一个方法 `usage_EXPERIMENTAL_MAY_CHANGE_DO_NOT_RELY_ON_THIS_API_YET(opts?: {skipBehaviors?: boolean})`，它向 CLI 发 `{subtype: "get_usage", skip_behaviors}` 控制请求，返回 `SDKControlGetUsageResponse`：

- `subscription_type`：`pro` / `max` / `team` / `enterprise`，API Key 或第三方提供商时为 null。
- `rate_limits_available`：为 false 表示这个登录没有订阅额度（API Key、Bedrock、Vertex，或者 token 缺少 profile 权限）。
- `rate_limits`：claude.ai 用量接口返回的各个窗口，每个窗口是 `{utilization: 0–100, resets_at: ISO}`：`five_hour`、`seven_day`、`seven_day_opus`、`seven_day_sonnet`，以及 `model_scoped[]`（按模型划分的每周窗口，带服务端给的 `display_name`，比如 `Fable`）。还有很多代号字段（大多是 null）和 `extra_usage`，我们不用。
- `skipBehaviors: true` 可以跳过扫描本机最近 7 天的对话记录（那部分是 `/usage` 里“哪些行为在消耗额度”的分析），只拿额度数据。

守护进程的做法（`node/src/usage.ts` 的 `readClaudeUsage`）：用和启动探测（`probeClaude`）一样的参数起一个空的 `query()`，等 `initializationResult()`，调用上面这个方法，然后关闭。环境变量用守护进程给员工会话的同一套（`claudeEnv`：`CLAUDE_CONFIG_DIR` 是守护进程配置里的 Claude 配置目录，`HOME` 是 agent-home），所以读的正是员工会话实际在用的那份登录。一次读取在本机大约耗时 0.6 秒（会起一个 CLI 子进程），不调用模型，不消耗额度。

映射到 `node.usage` 的 `windows[]`：

| CLI 字段 | key | window_minutes |
| --- | --- | --- |
| `five_hour` | `five_hour` | 300 |
| `seven_day` | `weekly` | 10080 |
| `seven_day_opus` | `weekly_opus` | 10080 |
| `seven_day_sonnet` | `weekly_sonnet` | 10080 |
| `model_scoped[i]` | `weekly_model`（`label` = `display_name`） | 10080 |

`utilization` 为 null 的窗口跳过；数值限制在 0–100；`plan` 填 `subscription_type`。

### 1.2 凭证：我们完全不碰

- Linux 上 CLI 的 OAuth 凭证在 `$CLAUDE_CONFIG_DIR/.credentials.json`（`claudeAiOauth.accessToken`、`refreshToken`、`expiresAt`），macOS 上在钥匙串。守护进程把这个文件列为员工会话不能读的路径（`Daemon.secretPaths()`）。
- 用 get_usage 时，读 token、token 过期后刷新、把新 token 写回凭证文件，都是 CLI 自己做的，和员工会话平时的行为完全一样。守护进程不读凭证文件，不自己刷新，也不写凭证文件。
- 消息里只有百分比、重置时间和套餐名，没有 token、邮箱、组织 id。失败原因会先经过 `errorText()` 脱敏（遮掉 `sk-…`、`eyJ…`、`Bearer …` 和邮箱，最多 200 个字符）。

### 1.3 失败和降级

| 情况 | 上报 |
| --- | --- |
| 启动探测得到 `auth` 是 `api_key` 或 `none` | `status:"unsupported"`，不起 CLI |
| SDK 没有这个方法（升级后改名或删除） | `status:"error"`，`error:"unsupported_sdk"` |
| 调用这个方法报错（CLI 不认识这个控制请求、CLI 内部出错等） | `status:"error"`，`error:"fetch_failed: <脱敏后的简短原因>"` |
| `rate_limits_available:false` | `status:"error"`，`error:"rate_limits_unavailable"` |
| `rate_limits` 为 null（CLI 没取到数据：token 过期且刷新失败、网络错误等） | `status:"error"`，`error:"fetch_failed"`，有套餐名就带上 `plan` |
| 12 秒没读完（守护进程会立即关闭 query，结束 CLI 子进程） | `status:"error"`，`error:"timeout"` |
| CLI 起不来等其他异常 | `status:"error"`，`error:"fetch_failed: <脱敏后的简短原因>"` |

任务简报里提到的 `token_expired`：由于刷新是 CLI 自己做的，守护进程看不到“过期”这一状态，只能看到 CLI 最后有没有拿到数据，所以统一报 `fetch_failed`。

### 1.4 不采用的方案：直接调 OAuth 用量接口

CLI 内部用的是未公开的 `GET https://api.anthropic.com/api/oauth/usage`（`Authorization: Bearer <accessToken>`，`anthropic-beta: oauth-2025-04-20`），返回的就是上面 `rate_limits` 那份数据。直接调它需要守护进程自己读凭证文件，token 过期还得自己处理（不能刷新，否则会和 CLI 抢 refresh token），macOS 上还要读钥匙串。get_usage 能拿到同样的数据又不用碰凭证，所以不采用。如果以后 SDK 去掉了 get_usage 又没有替代方法，这是备选方案，但要先解决凭证的问题。

### 1.5 版本兼容风险

- 方法名里写明了 `EXPERIMENTAL_MAY_CHANGE_DO_NOT_RELY_ON_THIS_API_YET`，SDK 注释也说“稳定后会改名”。代码通过动态查找来调用这个方法，方法不存在时降级成 `unsupported_sdk`，不会让守护进程崩溃，也不会影响会话。
- 升级 SDK 时要检查：方法是否还在（或者改成了什么名字），以及 `rate_limits` 里 `five_hour`、`seven_day*`、`model_scoped` 的结构有没有变。`node/test/usage.test.ts` 按当前结构写了映射测试。
- SDK 还有一个 `SDKUsageReport.rate_limits.limits[]`（服务端原样给出的计量行：`kind`、`group`、`percent`、`scope`），这是 `/usage` 结果里附带的结构化数据，只有真正执行 `/usage` 时才会出现，所以不适合拿来定时采集。如果以后 get_usage 也开始返回 `limits[]`，可以改用它，新增的计量类型就不需要改代码。

## 2. Codex

### 2.1 选用的方法：app-server 的 account/rateLimits/read

`@openai/codex` 0.169.0 的 app-server v2 协议有 `account/rateLimits/read`（参数 `GetAccountRateLimitsParams`，我们传 `excludeResetCreditDetails: true`，跳过额外的重置额度详情查询），返回 `GetAccountRateLimitsResponse`：

- `rateLimits`：兼容旧版本的单桶视图；`rateLimitsByLimitId`：按 `limit_id` 分桶（比如 `codex`）。优先用 `rateLimitsByLimitId.codex`，没有再用 `rateLimits`。
- 每个快照（`RateLimitSnapshot`）有 `primary`、`secondary` 两个窗口，各为 `{usedPercent, windowDurationMins, resetsAt}`（`resetsAt` 是 Unix 秒），还有 `planType`（`plus`、`pro`、`team` 等）。
- 响应里还有 `accountId` 和 `rateLimitUpsell`，我们不上报。

映射：`primary` → key `primary`（通常 300 分钟），`secondary` → key `secondary`（通常 10080 分钟）；`window_minutes` 取 `windowDurationMins`，`resets_at` 由 `resetsAt` 转成 ISO 时间；`planType` 为 `unknown` 时不填 `plan`。

请求走守护进程已有的那个共享 app-server（`CodexHub`，`CODEX_HOME` 与会话相同）。如果节点上还没有 Codex 会话，第一次采集会把 app-server 启动起来，之后它常驻（和有 Codex 会话时一样）。

### 2.2 先确认登录方式

启动探测（`probeCodex`）只看 `$CODEX_HOME/auth.json` 存不存在，但用 API Key 执行 `codex login` 也会写这个文件。所以每次采集前先调 `account/read {refreshToken:false}`（只读本地状态）：

- `account.type == "apiKey"` → 按 `auth:"api_key"` 上报 `unsupported`，不读额度。
- `account == null` 而且 `requiresOpenaiAuth` → `status:"error"`，`error:"not_logged_in"`。
- 环境变量里有 `OPENAI_API_KEY` 时，启动探测就会判成 `api_key`，不起 app-server。

### 2.3 退回方案：account/rateLimits/updated 推送

有 Codex 会话在跑时，app-server 会推送 `account/rateLimits/updated {rateLimits: RateLimitSnapshot}`（就是 token_count 事件里 rate_limits 的那份数据）。这是稀疏更新：为 null 的字段表示“没变”，不表示清空。`CodexHub.noteRateLimits` 会把每次推送合并到之前的快照上，并记下收到的时间。如果 `account/rateLimits/read` 失败（老版本没有这个方法、网络错误等），而且之前收到过推送，就用推送里的值，`fetched_at` 填收到推送的时间。两者都没有时报 `status:"error"`。

### 2.4 凭证

ChatGPT 的 token 在 `$CODEX_HOME/auth.json`，由 app-server 自己读取和刷新；守护进程不读这个文件（它也在 `secretPaths()` 里）。

### 2.5 版本兼容风险

app-server 协议有版本化的生成类型（`npm run gen:codex` → `node/src/codex-protocol/`），升级 `@openai/codex` 后重新生成，字段变化能在 typecheck 阶段发现。`account/rateLimits/read` 在 v2 协议里，是 Codex IDE 插件显示额度用的接口，比 Claude 那边稳定。

## 3. 判断订阅还是 API Key

- Claude：沿用 `probeClaude`（启动时 `initializationResult().account.subscriptionType` 有值就是 `subscription`；否则环境变量里有 `ANTHROPIC_API_KEY` / `ANTHROPIC_AUTH_TOKEN` 就是 `api_key`，都没有就是 `none`）。采集时如果 CLI 返回 `rate_limits_available:false`，说明登录方式已经变了，报 `rate_limits_unavailable`。
- Codex：沿用 `probeCodex`（`OPENAI_API_KEY` → `api_key`，有 `auth.json` → `chatgpt`，否则 `none`），每次采集前再用 `account/read` 确认（见 2.2）。
- 登录方式由 `RuntimeMonitor` 持续检测（protocol.md §9.2）：节点上重新登录、退出或改了 API Key 后，最多一分钟（监听到凭证文件变化时约 2 秒）就会更新 `auth`，不用重启守护进程；`auth` 变化时守护进程会立即再采集并上报一次 `node.usage`。

## 4. 采集时机和缓存

- 收到 `welcome` 后立即采一次，之后每 5 分钟一次（`AGENT_TEAMS_USAGE_INTERVAL_S` 可以调整，`0` 表示关闭定时采集）。每个运行时的读取总共最多 12 秒（Codex 的 account/read 和 rateLimits/read 共用这个时间，控制台等 `node.usage_refresh` 的回复最多 15 秒），两个运行时并行读取。
- 控制台发 `node.usage_refresh` 时（鼠标悬停在节点卡片上），请求的运行时如果最近 60 秒内有一次成功的读取，就直接回缓存，`fetched_at` 保持原值；否则立即读取。失败的结果不缓存。同一时间只跑一次读取，正在跑的就等它跑完再复用结果。
- 采集失败只记 warn 日志，并在消息里报 `status:"error"`，不影响守护进程的其他功能。

## 5. 实测（2026-11-19，节点 claude，Claude 订阅登录，Codex 未登录）

```
claude: auth=subscription status=ok plan=max
  five_hour     20%  resets 2026-11-19T23:09:59Z
  weekly        11%  resets 2026-11-25T14:59:59Z
  weekly_model  15%  (Fable)  resets 2026-11-25T14:59:59Z
codex:  auth=none status=unsupported
```

单独调用 app-server 时，`account/rateLimits/read` 在未登录的情况下返回错误 `codex account authentication required to read rate limits`；守护进程实际不会走到这一步，因为启动探测已经判成 `none`。这次没有 ChatGPT 登录的节点可以实测 Codex 的正常数据，Codex 的映射靠单元测试覆盖。

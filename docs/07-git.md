# 07 Git

原则：**授权在控制面配一次，所有节点共用；节点上不保存长期凭证，每次拉取或推送现取短期凭证。**

**第一版只支持 GitHub。** 仓库通过粘贴 GitHub 链接添加；GitLab、其他 HTTPS 仓库、SSH 协议以后再加，数据模型里的 `provider` 字段为此保留。

## 添加仓库

用户在新建项目、项目设置或 MCP 的 `create_project` / `add_repo` 里给出仓库，接受以下写法：

- `https://github.com/owner/repo`（可带 `.git`、`/tree/<分支>` 等后缀，解析时去掉）
- `git@github.com:owner/repo.git`（只取 owner/repo，实际仍用 HTTPS 访问）
- `owner/repo`

控制面解析出 `owner/repo` 后，找一个能访问它的 GitHub 连接，调 GitHub API 确认仓库存在、读默认分支，写入 `repositories`。找不到能访问的连接时提示“先安装 GitHub App 到这个仓库”或“访问令牌没有这个仓库的权限”。

## GitHub 连接

两种方式，推荐第一种：

| 方式 | 配置 | 签发给员工的东西 | 说明 |
| --- | --- | --- | --- |
| GitHub App（推荐） | 平台用 App Manifest 流程一键创建 App，用户在 GitHub 上安装到组织或指定仓库；App 私钥加密存 secrets | 安装令牌，约 1 小时，只含所需仓库，权限 `contents:read` 或 `contents:write`（建 PR 再加 `pull_requests:write`） | 能按员工身份区分读写，令牌短、可审计 |
| 访问令牌（快速起步） | 用户粘贴一个 fine-grained personal access token | 原令牌，经 helper 下发，不落盘 | 配置最快；读写权限所有员工一样，按身份的只读限制靠控制面拒绝签发写操作 |

## 凭证 helper 流程

员工目录的 `.gitconfig`（会话启动时以 `GIT_CONFIG_GLOBAL` 生效）：

```ini
[user]
  name = cc5
  email = cc5@agents.local
[credential "https://github.com"]
  helper = /home/dev/agent-teams/daemon/bin/git-credential-agent-teams
  useHttpPath = true        # 让 helper 拿到仓库路径，才能按仓库签发
```

1. git 需要凭证，调用 helper：`get`，stdin 给出 `protocol / host / path`。
2. helper 读环境变量 `AGENT_TEAMS_SESSION_TOKEN`，经 `daemon.sock` 发给守护进程。
3. 守护进程发 `credential.get {session_token, host, path, operation}` 给控制面。`operation`：fetch/clone 为 `read`；push 为 `write`（helper 无法直接区分时按员工模板的 `git_access` 取最大权限，push 范围由下面的分支规则限制）。
4. 控制面校验：会话有效 → 员工属于哪个项目 → 仓库是否在项目里 → 身份的 `git_access` 是否允许 → 签发（App 令牌按 (安装, 仓库, 权限) 缓存到过期前 5 分钟）→ 写 `credential_issuances`。
5. 返回 `username=x-access-token`、`password=<令牌>`，git 继续。helper 对 `store` / `erase` 不做任何事，令牌只在内存里。

守护进程自己 clone / fetch（建 worktree）时走同一条路径，用该员工的会话令牌。员工要用 `gh` 时，守护进程在会话环境里注入 `GH_TOKEN`，同样来自这套签发流程。

## 推送范围

GitHub 的令牌不能限制到分支，所以“开发只能推 `emp/<自己>/*`”要靠多层约束：

1. **员工目录里的 pre-push 钩子**：拒绝推送到不属于自己的分支（防误操作，员工能绕过）。
2. **仓库侧规则**：给 `main` 等受保护分支开分支保护 / Ruleset，只允许通过 PR 合并。平台在添加仓库时检查并提示。
3. **合并由验收身份完成**：`pr_policy = pr_to_default` 时，员工只建 PR；合并由开发负责人员工或人来做。
4. **之后**：控制面提供 Git 代理（smart HTTP 反向代理，解析 push 的 ref 更新），可以严格限制分支，代价是 Git 流量经过控制面。

## 交付

- 任务启动时守护进程：`git fetch` → `git worktree add tasks/<任务号>/<仓库> -b <交付分支> <base_ref>`。
- 员工完成后推送交付分支；推送前先 fetch，远端已有该分支时 rebase 到它上面（沿用 agent-remote-worker 的做法）。
- `pr_policy = pr_to_default`：控制面用 GitHub API 建 PR。
- 任务记录里保存交付分支和 PR 链接；项目对话的“交付”区显示状态。

## 仓库缓存

- 每个员工独立 clone 到 `repos/<仓库>`，避免多个员工抢同一仓库的锁。
- 之后可加节点级只读镜像（`git clone --reference`）省磁盘和带宽。
- 回收员工前检查所有 worktree 有无未推送的提交或未提交的改动，有则拒绝（`employee.removed ok:false`），由人决定。

## 以后

| 平台 | 做法 |
| --- | --- |
| GitLab | Group / Project Access Token，集中保存，经 helper 下发 |
| 其他 HTTPS（Gitea、Bitbucket…） | 访问令牌，经 helper 下发 |
| SSH 协议 | 不下发私钥：守护进程在本机提供 ssh-agent socket，签名请求转给控制面完成（控制面不再登录节点，无法用 ssh-agent 转发） |
| GitHub Enterprise | `host` 改为企业域名，API 地址可配 |

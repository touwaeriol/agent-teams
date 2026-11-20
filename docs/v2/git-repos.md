# 通用 Git 仓库接入

状态：**第一期已实现**（2026-11-20，用户确认按 §12 的推荐做；节点怎么拿凭据后来改为 §5 的“直接交给员工的 git、隔离”）。实现与设计的出入见 §14；原型目录已删除，正式页面是设置 → “Git 仓库”（`web/src/pages/settings/GitRepos.vue`）。

和 `remote-servers.md`（凭据只在主节点、信封加密、主机密钥固定）、`architecture.md` §8（分支、合并队列、CI、PR）和 §10.4（GitHub 凭证不落盘）一起读。

## 1. 一句话

“设置 → GitHub 连接”改名为 **“设置 → Git 仓库”**：除了现在的 GitHub App 连接，还能**单独添加任意一个 Git 仓库**（GitLab、Gitea、Bitbucket、自建 Git 服务器，或者用个人令牌的 GitHub 仓库），用 **HTTPS 令牌**或 **SSH 私钥**验证。仓库属于用户，项目和讨论串像选 GitHub 仓库一样选它。凭据加密存在主节点；员工的 git 用到时按需交给节点（只在内存、按会话密钥隔离，§5），节点直连上游读写。PR、CI、webhook 这类 GitHub 独有的能力，在通用仓库上按 §8 降级：“提交 PR 即完成”变成“推送讨论串分支即完成”，“我来验收”在平台里看提交和改动再由平台合并，CI 关卡关闭。

## 2. 需求与边界

### 2.1 用户的需求（2026-11-20 原话）

> 将原来的 GitHub 改成 Git 仓库，然后支持指定一个单独的 Git 仓库，使用证书或者 token 去验证，后续就可以使用这个仓库。

| 编号 | 需求 | 章节 |
| --- | --- | --- |
| G1 | 设置里的“GitHub 连接”变成“Git 仓库”，GitHub App 继续可用 | §6、§11.1 |
| G2 | 能单独添加一个任意 Git 仓库（不限 GitHub） | §3、§11.2 |
| G3 | 验证方式：SSH 私钥（“证书”）或 HTTPS 令牌（token） | §3.2、§4 |
| G4 | 加好后，项目和讨论串都能用这个仓库 | §7、§11.3 |
| G5 | 凭据做法参照服务器：不下发到节点，或只下发短时凭据 | §4、§5 |

### 2.2 不做什么（第一期）

- 不做 GitLab / Gitea 的 MR、流水线 API 适配（以后，§8.4）。第一期通用仓库**只用 Git 协议**，不调任何托管平台的 API。
- 不做 Git LFS（先不支持，提示里说明）。
- 不做“列出某个 GitLab 账号下所有仓库”：一次添加一个仓库，用户贴地址（需求就是“单独的仓库”）。
- 不把长期凭据下发到节点，也不提供“直接下发”的开关（§5.4 说明为什么）。
- 不做 SSH 证书（CA 签发）登录托管平台：托管平台基本只认公钥或令牌。界面上的“证书”统一叫“SSH 私钥”，并提供“平台生成密钥对、你把公钥填成部署密钥”。

## 3. 数据模型

两张新表 + `at_project_repos` 一个新列。迁移编号取下一个空号（写本文时最新是 `00330_remote_servers.sql`，暂记 `00340_git_repos.sql`），完整列注释实现时补进 `data-model.md` §2.15。

### 3.1 为什么把“凭据”和“仓库”分开

同一个 GitLab 账号下的十个仓库通常共用一个令牌；部署密钥则一个仓库一把。所以：

- `at_git_credentials`：一条凭据（令牌或私钥），**限定一个主机**（`gitlab.com`、`git.example.com:2222`），只能给这个主机上的仓库用。
- `at_git_repos`：一个仓库（地址 + 用哪条凭据）。公开仓库可以不挂凭据。

添加仓库的表单里“凭据”是下拉框：同主机已有的凭据 + “新建凭据”（展开填写），所以大多数时候用户感觉不到这是两张表。

### 3.2 `at_git_credentials`

| 列 | 类型 | 说明 |
| --- | --- | --- |
| `id` | TEXT PK | `gc_…` |
| `user_id` | TEXT NOT NULL FK | 属于谁，所有查询按它过滤 |
| `name` | TEXT NOT NULL | 显示名，例如“GitLab 个人令牌”“web 部署密钥”；同一用户内唯一 |
| `host` | TEXT NOT NULL | 小写主机，非默认端口带 `:端口`（SSH 默认 22、HTTPS 默认 443 时不写） |
| `kind` | TEXT NOT NULL | `https_token` \| `ssh_key` |
| `username` | TEXT NOT NULL DEFAULT '' | HTTPS 用户名（GitLab 用 `oauth2`、GitHub 用 `x-access-token`、Gitea 用账号名，按 §3.5 的识别结果预填）；SSH 时是登录用户，默认 `git` |
| `cred_ciphertext`、`cred_nonce`、`wrapped_dek`、`dek_nonce`、`kek_id` | BYTEA / TEXT | 信封加密（§4.1），和 `at_remote_servers` 同一套列 |
| `has_passphrase` | BOOL | 私钥是否带口令（口令和私钥一起密封） |
| `key_fingerprint` | TEXT | 私钥对应公钥的 SHA256 指纹 |
| `public_key` | TEXT | `ssh-ed25519 AAAA… agent-teams`，只有平台生成的密钥对才有，方便用户再次复制填到部署密钥 |
| `generated` | BOOL | 是平台生成的密钥对（私钥用户从没见过） |
| `expires_at` | TIMESTAMPTZ NULL | 令牌到期时间，用户可选填（GitLab 令牌必有到期日），到期前提醒（§11.2） |
| `cred_updated_at`、`created_at`、`updated_at` | TIMESTAMPTZ | |

### 3.3 `at_git_repos`

| 列 | 类型 | 说明 |
| --- | --- | --- |
| `id` | TEXT PK | `gr_…`，节点上的目录名 `git/<id>` 用它 |
| `user_id` | TEXT NOT NULL FK | |
| `slug` | TEXT NOT NULL | 仓库在平台里的名字：`<host>/<path>`，例如 `gitlab.com/acme/web`、`git.example.com/team/sub/app`（去掉 `.git`，小写主机）。同一用户内唯一。它就是 `at_project_repos.repo`、`at_thread_repos.repo`、工具参数 `repo` 里写的那个值（§3.6） |
| `name` | TEXT NOT NULL DEFAULT '' | 可选的短名，界面显示用；空时显示 slug 最后一段 |
| `url` | TEXT NOT NULL | 用户贴的克隆地址，规整后存：`https://host/path.git` 或 `ssh://user@host:port/path.git`（scp 写法 `git@host:path` 规整成 ssh://） |
| `transport` | TEXT NOT NULL | `https` \| `ssh` |
| `credential_id` | TEXT NULL FK → `at_git_credentials` ON DELETE RESTRICT | NULL = 公开仓库，只能读（推送一定失败，测试连接时就提示） |
| `provider` | TEXT NOT NULL | `github` \| `gitlab` \| `gitea` \| `bitbucket` \| `generic`，按主机自动识别、用户可改（§3.5）；第一期只用来决定网页链接和用户名预填 |
| `web_url` | TEXT NOT NULL DEFAULT '' | 仓库网页地址（识别出来的或用户填的），用来拼“比较分支”“查看提交”链接；空时界面不给链接 |
| `default_branch` | TEXT NOT NULL DEFAULT '' | 测试连接时 `ls-remote --symref HEAD` 读到的 |
| `access` | TEXT NOT NULL DEFAULT '' | `''`（没测过）\| `read` \| `write`，最近一次测试的结果 |
| `host_key`、`host_key_fp`、`host_key_mac`、`host_key_confirmed_at` | | 只有 SSH：第一次测试连接时固定的主机密钥，和服务器完全同一套做法（`remote-servers.md` §5.3） |
| `last_checked_at`、`last_error` | | 最近一次测试 / 同步的时间和错误 |
| `created_at`、`updated_at` | | |

约束：`(user_id, slug)` 唯一；`credential_id` 的凭据必须属于同一用户、`host` 与 url 的主机相同（应用层检查 + 外键带 `user_id` 的复合外键，做法同 `at_project_servers`）。

### 3.4 `at_project_repos` 加一列

```sql
ALTER TABLE at_project_repos
  ADD COLUMN git_repo_id TEXT REFERENCES at_git_repos(id) ON DELETE RESTRICT;
ALTER TABLE at_project_repos
  ADD CONSTRAINT at_project_repos_one_source
  CHECK (git_connection_id IS NULL OR git_repo_id IS NULL);
```

一行仓库要么来自 GitHub 连接（`git_connection_id`，现状），要么来自单独添加的仓库（`git_repo_id`）。删除仍被项目使用的单独仓库会被拒绝（RESTRICT，接口回 409 并列出项目），而不是像 GitHub 连接那样置空——单独仓库没有“换个连接”的余地。`at_thread_repos` 不变：它按 `repo`（slug）记，来源从项目行查。

### 3.5 地址识别

用户只贴一个地址，表单自动推出其余字段：

| 输入 | transport | host | path | provider | 预填用户名 |
| --- | --- | --- | --- | --- | --- |
| `https://gitlab.com/acme/web.git` | https | gitlab.com | acme/web | gitlab | `oauth2` |
| `git@gitlab.com:acme/web.git` | ssh | gitlab.com | acme/web | gitlab | `git` |
| `ssh://git@git.example.com:2222/team/app.git` | ssh | git.example.com:2222 | team/app | generic | `git` |
| `https://gitea.example.com/team/app` | https | gitea.example.com | team/app | generic（可手选 gitea） | 空（必填） |
| `https://github.com/acme/web` | https | github.com | acme/web | github | `x-access-token`，并提示“GitHub 仓库建议用 GitHub App 连接，能用 PR 和 CI” |

拒绝：带内嵌凭据的地址（`https://user:token@…`，提示把令牌填到下面的框里）、`file://`、`git://`（不加密）、本机和内网地址（复用 `remote-servers.md` §5.6 的地址限制和 `ssh_allowed_private_cidrs` 放行表，防止主节点被当跳板扫内网）。

### 3.6 slug 与工作目录

现在的 `ghops.Dir` 把 `owner/name` 变成 `owner__name`。通用仓库的 slug 有主机和多级路径，规则改为：`/` → `__`，`:` → `_`（例如 `git.example.com_2222__team__sub__app`）；GitHub 仓库的目录名不变（不带 `github.com/` 前缀），已有工作目录不受影响。同一项目里 slug 唯一，所以目录不会撞。

## 4. 凭据加密存储

### 4.1 信封加密

和服务器凭据完全同一套（`remote-servers.md` §11.1，代码 `server/internal/credvault`）：每条凭据一把随机 DEK（AES-256-GCM），DEK 用从 `AGENT_TEAMS_SECRET_KEYS` 派生的 KEK 包裹。新增一个 AAD 构造函数：

```
credvault.GitCredAAD = "at-git-cred:v1 | <user_id> | <credential_id> | <kind> | <host> | <username>"
```

- 改主机、凭据类型或用户名，旧密文就解不开，接口要求同时提交新凭据（422 `credential_required`，和服务器一样）。所以数据库被人改了 `host`，凭据也解不开、不会发到别的主机。
- 仓库的 SSH 主机密钥行带 MAC（HMAC-SHA256，密钥从 KEK 用 HKDF 派生，覆盖 `repo_id、host、key_type、public_key`），直接往表里写主机密钥不起作用。
- `atctl admin reseal` 增加一步：重新包裹全部 `at_git_credentials`，和服务器凭据在同一个事务里。

### 4.2 只写不读

- 接口响应（`GitCredentialView`）只有元数据：类型、主机、用户名、指纹、有没有口令、公钥（平台生成的）、到期时间、更新时间。没有任何接口能取回令牌或私钥明文，平台工具也没有。
- 编辑时凭据框显示“已保存，留空不修改”。
- “生成密钥对”：主节点用 `crypto/ed25519` 生成，私钥直接密封入库，**网页从头到尾只见到公钥**。这是 SSH 方式推荐的做法：用户不用在自己电脑上生成、也不用把私钥贴进浏览器。

### 4.3 谁在什么时候解开凭据

只有主节点上的三处代码会解开凭据，解开后只在内存里用完即弃：

1. **测试连接**（§11.2）。
2. 员工会话或节点作业按需领取（§5.2），以及主节点镜像同步上游、合并队列推送（§5.3）。
3. **主节点 Git 引擎**做合并队列、建分支、合并讨论串（§8.2）。

都写审计（`at_audit_log`：`git_credential_create/update/delete`、`git_repo_create/update/delete/test`、`git_repo_host_key_pin/reset`）。

## 5. 节点怎么 clone / push：凭据按需交给员工的 git（隔离）

> 2026-11-20 用户改定：不走主节点中转，“把仓库配置到节点 cc 的环境里让它能用，但要隔离，仅限 cc worker 进程能用”。最初的中转方案（镜像 + `git http-backend` + 中转令牌 + pre-receive）已删除；主节点的镜像只留给合并队列自己用（§8.2）。

### 5.1 结构

```
 节点（守护进程 + 员工会话）                                         主节点 atctl                上游（GitLab / Gitea / 自建）
┌──────────────────────────────────────────────────────┐        ┌──────────────────────┐    ┌──────────────┐
│ 员工会话里的 git（Claude Code / Codex 的子进程）        │        │ cred.request 归属校验 │    │              │
│  remote = 上游地址（https://… 或 git@…:…），不含凭据    │        │ 解开凭据 → 回给节点   │    │              │
│  HTTPS：凭证助手 ─┐                                   │        └──────────▲───────────┘    │              │
│  SSH：GIT_SSH_COMMAND = agent-teams-node git-ssh ─┤   │                   │                │              │
│                   ▼                               │   │                   │                │              │
│  cred.sock（0700 目录，按会话密钥校验）→ 守护进程 CredBroker ── cred.request ┘                │              │
│  ← 令牌 / 私钥（只在内存）                                                                   │              │
│  git ────────────────────────────── 直连上游 ─────────────────────────────────────────────►│              │
└──────────────────────────────────────────────────────┘                                    └──────────────┘
```

- 节点上的 remote URL 就是上游地址，**不含凭据**；`git.prepare` / `git.push` 用 `remote_url` 告诉守护进程（守护进程自己的镜像、共享克隆、worktree 都照旧，`architecture.md` §8.1）。节点要能访问上游。
- 会话的 `session.open` 带 `git_hosts`（项目里有凭据的独立仓库的主机和传输方式）。守护进程据此给员工进程配：
  - HTTPS 主机：`credential.https://<host>.helper` 指向平台的凭证助手（和 GitHub 同一套，`useHttpPath=true`）；
  - SSH 主机：`GIT_SSH_COMMAND` 指向 `agent-teams-node git-ssh` 包装。

### 5.2 隔离：谁能拿到凭据

- **只有员工会话的进程树**：凭证助手和 git-ssh 包装都要带会话密钥（每次打开会话新生成，只在员工进程的环境变量里）去问守护进程的 `cred.sock`（目录 0700、属于守护进程用户），守护进程再用 `cred.request` 问主节点。统筹会话拿不到；别的会话的密钥对不上。
- **主节点逐次判断**：会话属于本节点、`open_gen` 当前、项目里有这个仓库（按主机 + 路径匹配）；作业只能拿自己那个仓库。凭据有效期报 30 分钟，到期守护进程重新问，所以在设置里替换或删除凭据、把仓库移出项目，最晚 30 分钟后就不再下发。
- **不落盘**：令牌只在守护进程内存和这一次 git 子进程里（`GIT_ASKPASS` / 凭证助手的输出）；SSH 私钥只在 git-ssh 包装进程的内存里，经一次性的 ssh-agent 套接字（私有临时目录，连接结束即删）交给 ssh，私钥文件、`~/.ssh`、仓库配置里都没有它。主节点把私钥解成不加密的 PKCS#8 只为让 Node 能加载，口令不下发。
- **主机密钥固定**：SSH 连接用设置里测试连接时固定的主机密钥（`StrictHostKeyChecking=yes`，只信这一把），`IdentitiesOnly=yes` 只提供这把私钥。
- **受保护分支**：和 GitHub 仓库一样由节点的 pre-push 钩子拒绝（`AT_PROTECTED_REFS`：讨论串分支、默认分支等）。注意这是客户端检查，`--no-verify` 能绕过；需要更严时请在上游平台设分支保护。
- **边界**：同一个系统用户下的其他进程理论上能读到员工进程的环境变量或内存（GitHub 令牌也是如此）；要完全隔离请让节点单独用一个系统用户或容器（`deploy/node`）。

### 5.3 主节点自己的镜像

合并队列、建/删讨论串分支在主节点上做（§8.2）：每个仓库一份裸镜像 `GIT_MIRROR_DIR/<repo_id>.git`，用保存的凭据和上游同步，合并后推回上游。镜像只是缓存，丢了从上游重建。

## 6. 和现有 GitHub App 怎么共存

**共存，不迁移 GitHub App。** GitHub App 是能力最全的来源（1 小时单仓库令牌、PR、CI、webhook、仓库列表），保持原样。

| 来源 | 存在哪 | 能力 | 变化 |
| --- | --- | --- | --- |
| GitHub App 连接 | `at_git_connections`（`kind = app`） | 全部 | 无。设置页里放进“GitHub 连接”分区 |
| GitHub 个人令牌（PAT）连接 | `at_git_connections`（`kind = token`） | PR、CI（令牌给的权限内）；员工会话直接拿到 PAT | **第二期迁走**（§6.1） |
| 单独添加的仓库 | `at_git_repos` + `at_git_credentials` | Git 读写，没有 PR / CI（§8） | 新增 |

### 6.1 PAT 连接的去向

PAT 连接本来就是“长期令牌下发到员工会话”（`remote-servers.md` §10.3 第 1 条，Q8 没定）。有了单独仓库，它的合理归宿是：**变成 `github.com` 主机上的一条 HTTPS 令牌凭据，项目里用到的每个仓库变成一个单独仓库**，令牌从此只在主节点。

- 第一期：PAT 连接照常工作，GitHub 页（新“Git 仓库”页的 GitHub 分区）在 PAT 卡片上加提示“个人令牌会下发到员工会话，建议转成单独仓库（令牌只留在平台）或改用 GitHub App”，并给“转成单独仓库”按钮。
- 转换由 `atctl admin convert-pat <connection_id>` 或按钮触发（Go 代码，不放 SQL 迁移，因为要解开 Fernet 再按信封加密重新密封）：建凭据、为每个用到的 `owner/name` 建 `at_git_repos`（slug 写 `github.com/owner/name`）、改 `at_project_repos` 指向它。**slug 变了**，所以只在没有进行中讨论串的项目上转，有的话按钮提示先结束讨论串。
- 用 PAT 的单独 GitHub 仓库会失去 PR / CI（第一期通用仓库不调 API）。所以按钮旁边写清楚：“要保留 PR 和 CI，请改用 GitHub App”。以后 §8.4 的 GitHub 令牌 API 适配做了，这个差别就没了。

### 6.2 代码里的分岔点

现在 `server/internal/ghops` 的 `Repo` 同时承担“找凭据”和“调 GitHub API”。改成一个小接口，GitHub 的实现就是现有代码：

```go
// server/internal/forge
type Repo interface {
    Slug() string
    Caps() Caps                       // §8.1
    DefaultBranch(ctx) (string, error)
    RefSHA(ctx, branch) (string, error)
    EnsureBranch(ctx, branch, base) (string, error)
    TrialMerge(ctx, base, head, msg) (MergeResult, error)   // GitHub: mq 分支 + merges API；Git: 镜像里 merge
    FastForward(ctx, branch, sha) error
    DeleteRef(ctx, branch) error
    Compare(ctx, base, head) ([]string, error)
    // 只有 Caps().PR 为真才调用；Git 实现返回 ErrUnsupported
    CreatePR / OpenPRFor / GetPR / MergePR / CheckRuns …
}
```

`delivery` 包里现在直接调 `ghops.GitHub` 的地方改为调 `forge.Repo`，并在 PR / CI 相关分支前判断 `Caps()`。`ghops.Find`、`ReposOf` 变成 `forge.ReposOf(project)`，按 `at_project_repos` 的来源分别构造。

## 7. 项目和讨论串怎么用

- **新建项目 / 项目设置 → 仓库**：选仓库的下拉框分两组：“GitHub”（各连接的仓库，现状）和“单独添加的仓库”。下拉框底部有“+ 添加新的 Git 仓库…”，打开和设置页同一个对话框，加好后直接选中，不用离开当前页面。
- 列表里每行仓库带来源图标和能力标签：GitHub 仓库不变；单独仓库显示“Git · 推送分支交付”（没有 PR），鼠标悬停说明 §8 的降级。
- **讨论串**：`open_thread(repos=[{repo, base}])`、`assign_task(repo)`、`enqueue_merge(repo)`、`open_pr(repo)` 的 `repo` 参数写 slug（`gitlab.com/acme/web`）。员工提示词的“## 仓库”一节给每个仓库加一行能力说明，例如“gitlab.com/acme/web：没有 PR 和 CI，完成后平台把讨论串分支推送到上游，由用户在平台里验收”。
- 一个项目可以混用 GitHub 仓库和单独仓库；PR、CI 按仓库各自判断（§8.2 的“全部仓库”规则照旧，只是每个仓库的“完成”条件不同）。
- 删除单独仓库前必须先从所有项目里移除（§3.4）；从项目移除时，进行中的讨论串里该仓库的 worktree 照常清理，节点最晚 30 分钟后不再拿到凭据（§5.2）。

## 8. PR / CI 怎么降级

### 8.1 能力表

| 能力 `Caps` | GitHub App | 单独仓库（第一期） | 没有时怎么办 |
| --- | --- | --- | --- |
| `short_token`（节点拿短时上游令牌） | ✓ | — | 通用仓库下发保存的凭据本身（30 分钟后重新校验，§5.2） |
| `repo_list`（列出可选仓库） | ✓ | ✗ | 一次添加一个，贴地址 |
| `default_branch` | API | `ls-remote --symref` | 等价 |
| `branches`（分支下拉） | API | 镜像里 `for-each-ref` | 等价 |
| `pr`（开 PR、合并 PR） | ✓ | ✗ | §8.2 |
| `ci`（读检查结果） | 要 `checks:read` | ✗ | CI 关卡关闭（§8.3） |
| `webhook` | ✓ | ✗ | 轮询镜像 |
| `gh` 命令 | ✓ | ✗ | 提示词说明 |
| 受保护分支 | 用户建规则集 + 节点 pre-push | **主节点 pre-receive 强制**（§5.5） | 更严 |

### 8.2 合并与交付

合并队列（成员分支 → 讨论串分支）在通用仓库上由**主节点 Git 引擎**在主节点镜像里做，流程和 `architecture.md` §8.2 一一对应：

| 步骤 | GitHub（现状） | 单独仓库 |
| --- | --- | --- |
| 建讨论串分支 | `POST /git/refs` | 镜像里 `git branch`，推上游 |
| 试合并 | `mq/<no>/<id>` 分支 + `POST /merges` | 镜像里临时 worktree：`git merge --no-ff <成员分支>`；冲突 → 同样生成修复任务 |
| 等 CI | 试合并分支的 check | 跳过（没有 CI） |
| 快进讨论串分支 | `PATCH /git/refs` | `git push <上游> <sha>:refs/heads/thread/<no>-<slug>`，带 `--force-with-lease=<旧 sha>`；上游已变（别人推了）→ 回到试合并，最多 3 次，和 422 非快进同样处理 |
| 清理 | 删 `mq/*` | 删临时 worktree（没有 mq 分支推上游） |

讨论串完成后（`architecture.md` §8.4 的 `merge_policy`）：

| 项目设置 | GitHub 仓库 | 单独仓库 |
| --- | --- | --- |
| `pr`“提交 PR 即完成”（默认） | 开 PR，check 过了就 done | **“推送讨论串分支即完成”**：讨论串分支已在上游，组长 `finish` 后讨论串 done。讨论串页显示分支名，`web_url` 识别得出时给“在 GitLab 上比较 / 创建合并请求”链接（例如 GitLab `…/-/merge_requests/new?merge_request[source_branch]=…`），用户自己去开 MR |
| `auto`“自动合并” | check 过了合并 PR | 平台把讨论串分支合并进基准分支（按 `merge_method`：squash / merge；rebase 第一期按 merge 处理），推上游；基准分支被保护推不上去 → `merge_error` 写明“上游拒绝推送到 main（受保护分支？）”，算“等你处理” |
| `manual`“我来验收” | 验收卡片 → 合并 PR | 同样的验收卡片，但**改动在平台里看**：提交列表、`git diff --stat`、每个文件的 diff（主节点在镜像里算，新接口 `GET /api/threads/{tid}/repos/{repo}/diff`）；accept → 平台合并（同 `auto`）；reject → 照旧回给组长 |

`open_pr` 工具在单独仓库上：不报错，回“这个仓库没有 PR，讨论串分支 thread/12-x 已推送到上游；按项目设置，完成后（推送即完成 / 平台合并 / 等用户验收）”，并把讨论串推进到和开 PR 后相同的 `review` 状态，后面的流程不变。`at_thread_repos.pr_*` 在单独仓库上保持空，新增 `delivered_sha`（推送 / 合并时的讨论串 head）和 `merged_sha`。

**人工在上游合并的检测**（代替 webhook）：合并循环每 2 分钟对处于 `review` 的单独仓库同步一次镜像，基准分支已包含讨论串分支 head（`merge-base --is-ancestor`）就记为已合并并按 `endThread` 收尾。

### 8.3 CI

单独仓库第一期没有 CI 结果：项目设置的 CI 区块对这些仓库显示“这个仓库是单独添加的 Git 仓库，平台读不到它的 CI，CI 关卡不生效”。合并队列不等 CI；`ci.on_missing = fail` 对这类仓库不适用（否则永远合并不了），按 `pass` 处理并在讨论串里标“无 CI”。

### 8.4 以后：托管平台 API 适配

`forge.Repo` 留好了口子。第三期可按需加：

- **GitLab**（令牌带 `api` 范围时）：MR 代替 PR，pipeline 状态代替 check，`merge_requests/:iid/merge` 代替合并；webhook 用 GitLab 的 `X-Gitlab-Token`。
- **Gitea / Forgejo**：PR API 和 commit status，与 GitHub 很像。
- **GitHub + 令牌**：直接复用现有 `ghops` 的 PAT 代码路径（令牌留在主节点，只用于 API），这样 §6.1 转换出来的仓库也能有 PR / CI。

这些都只在主节点调用，节点侧不变。

## 9. 协议改动

| 消息 | 改动 |
| --- | --- |
| `cred.request` | 不改结构；`host` 可以是独立仓库的主机（`host[:port]`），`path` 为它在主机上的路径（带 `.git`） |
| `cred.response` | HTTPS：`username` + `password`（上游令牌）；SSH：`username` + `ssh_key {private_key (PKCS#8 PEM), public_key, known_hosts}` |
| `git.prepare` / `git.push` | 加 `remote_url`（可选）：独立仓库的上游地址（HTTPS 不带凭据、`ssh://` 或 `git@host:path`）；没有就沿用 `gitBaseUrl + /<owner>/<name>.git` |
| `session.open` | 加 `git_hosts [{transport, host}]`：守护进程据此给 HTTPS 主机配凭证助手、有 SSH 主机时配 `GIT_SSH_COMMAND` |

旧守护进程收到带 `remote_url` 的作业会忽略字段、按 GitHub 地址克隆而失败：控制台按守护进程上报的版本，不把通用仓库的作业派给不支持的节点（节点卡片提示“升级后才能用单独添加的仓库”）。

## 10. 安全分析

| 威胁 | 结果 |
| --- | --- |
| 员工会话拿到凭据 | 用户选择的做法：员工的 git 能用上游令牌 / 私钥（§5.2），但只经会话密钥按需领取、不落盘、30 分钟后重新校验；令牌被员工主动打印出来就等于泄露，和 GitHub PAT 一样，请给令牌最小权限（只这个仓库、能推送即可） |
| 节点被攻破 | 能拿到项目里独立仓库的上游凭据（守护进程内存里有）。凭据按主机共用，所以影响该凭据能访问的所有仓库；建议每个仓库或每个项目单独建令牌 / 部署密钥 |
| 有人改数据库把 `host` 指向自己的机器 | AAD 含主机，凭据解不开；SSH 主机密钥带 MAC |
| 用主节点扫内网 | 主节点只为测试连接和合并队列连上游，受地址限制（§3.5） |
| 员工推到受保护分支 | 节点 pre-push 钩子拒绝（可被 `--no-verify` 绕过，需要时在上游设分支保护） |
| 恶意仓库内容攻击主节点（钩子、过滤器、`fsmonitor`） | 镜像是裸仓库，主节点的 git 一律 `-c core.hooksPath=<平台钩子目录> -c core.fsmonitor= -c protocol.file.allow=never`，不跑仓库里的任何东西；合并用的临时 worktree 同样设置，且不做 checkout 以外的操作（`merge` 不执行仓库里的钩子） |
| 主节点被攻破 | 所有凭据都暴露，和服务器凭据、GitHub App 私钥相同（`remote-servers.md` §14），不在本设计范围 |

## 11. 接口与网页

### 11.1 接口（实现时补进 `api.md`）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET / POST | `/api/git/credentials` | 列表 / 新建凭据（`kind`、`host`、`username`、`token` 或 `private_key` + `passphrase`，或 `generate: true`） |
| PATCH / DELETE | `/api/git/credentials/{id}` | 改名、替换凭据；还有仓库在用时删除回 409 |
| GET / POST | `/api/git/repos` | 列表 / 添加仓库（`url`、`name`、`credential_id` 或内联 `credential`），添加时同步做一次测试连接，失败也保存但标红 |
| GET / PATCH / DELETE | `/api/git/repos/{id}` | 详情 / 修改 / 删除（项目在用回 409，列出项目） |
| POST | `/api/git/repos/test` | **保存前**测试：表单里的地址 + 凭据（不入库），回 `ok`、`access`、`default_branch`、`host_key`、`error` |
| POST | `/api/git/repos/{id}/test` | 已保存的仓库测试连接；SSH 第一次在这里固定主机密钥 |
| POST | `/api/git/repos/{id}/host-key/reset` | 清掉已固定的主机密钥 |
| GET | `/api/git/repos/{id}/branches?q=` | 分支下拉（镜像里读） |
| GET | `/api/threads/{tid}/repos/{repo}/diff` | 验收用的提交和改动（§8.2） |

`POST/PATCH /api/projects` 的 `repos[]` 元素加 `git_repo_id`（和 `git_connection_id` 二选一）。测试连接的错误码沿用服务器的那套，加 `repo_not_found`、`no_write_access`、`lfs_unsupported`（提示性）。

### 11.2 网页

原型文件在 `web/src/prototype/gitRepos/`（`npm run dev:mock` 后打开 `/settings/git-prototype`、`?dialog=https`、`?dialog=ssh&tested=1`、`/settings/git-prototype/picker`）。截图：

| 设置 → Git 仓库 | 添加仓库（HTTPS 令牌） |
| --- | --- |
| ![](img/git-repos/git-repos-page.png) | ![](img/git-repos/git-repos-add-https.png) |
| **添加仓库（SSH，平台生成密钥对，测试通过）** | **项目里选仓库 + 无 PR 仓库的验收卡片** |
| ![](img/git-repos/git-repos-add-ssh.png) | ![](img/git-repos/git-repos-project-picker.png) |

- **设置 → Git 仓库**（原“GitHub 连接”，路由仍是 `/settings/git`，侧栏说明“仓库和访问凭据”）：标题下一句说明；右上两个按钮“添加仓库”（主按钮）和“连接 GitHub”。
  - **单独添加的仓库**：每个仓库一张卡片，样式和“服务器”卡片一致：名称、克隆地址（等宽）、来源标签（GitLab / Gitea / 自建…）；字段：认证方式（HTTPS 令牌 / SSH 私钥 + 凭据名）、访问权限（可读写 / 只读，没测过时标黄）、默认分支、主机指纹（SSH，还没固定时标黄）、凭据更新时间（令牌快到期时标黄）、使用的项目；能力行写“没有 PR 和 CI：推送分支交付，平台内验收”；按钮：测试连接、编辑、删除。
  - **凭据**：一个紧凑的表格（名称、主机、类型、指纹或用户名、被几个仓库使用、到期），可以替换和删除；平台生成的密钥对有“复制公钥”。
  - **GitHub 连接**：现有的 GitHub App / PAT 卡片原样放这里，PAT 卡片多一条提示（§6.1）。
- **添加仓库对话框**：
  1. 仓库地址（粘贴即识别，下方实时显示“HTTPS · gitlab.com · acme/web · 识别为 GitLab”）；
  2. 显示名称（可选）；
  3. 认证方式：HTTPS 令牌 / SSH 私钥 / 公开仓库（只读）；地址是 `git@` 时默认 SSH；
  4. 凭据：下拉框（同主机已有的 + “新建凭据”）；新建时 HTTPS 填用户名（预填）、令牌、可选到期日，SSH 选“平台生成密钥对（推荐）”或“粘贴私钥”，生成后显示公钥和“去 GitLab 添加部署密钥（勾选写权限）”的链接；
  5. 底部能力说明（Callout）：“单独添加的仓库没有 PR 和 CI：讨论串完成后推送分支，或由平台合并。要用 PR 和 CI，请用 GitHub App 连接。”
  6. 按钮：“测试连接”（不保存）、“添加”。测试结果面板：认证通过 / 失败原因、读写权限、默认分支、SSH 主机密钥类型和指纹（第一次连接时固定，附核对命令）。
- **项目设置 → 仓库**、**新建项目**：§7 的分组下拉框和能力标签。
- **讨论串验收卡片**（单独仓库、`manual`）：提交数、改动统计、“查看改动”展开 diff，按钮同现在。

## 12. 待用户拍板

| # | 问题 | 推荐 |
| --- | --- | --- |
| Q1 | 节点读写经主节点中转（§5），主节点要多占磁盘和带宽，可以吗？ | 已改：用户选择把凭据隔离地交给员工的 git，节点直连上游（§5） |
| Q2 | 凭据和仓库分开（一个令牌给同主机多个仓库用），还是每个仓库各填各的？ | 分开，表单里是下拉框，用户基本感觉不到 |
| Q3 | 单独仓库的“提交 PR 即完成”改成“推送讨论串分支即完成”，可以吗？ | 可以，并给“去 GitLab 创建合并请求”链接 |
| Q4 | 个人令牌（PAT）GitHub 连接第二期转成单独仓库（令牌不再下发会话，但失去 PR/CI，直到 §8.4） | 转，同时推荐 GitHub App |
| Q5 | 第一期就做 GitLab MR / 流水线适配吗？ | 不做，先把 Git 层做稳，第三期再按需要加 |

## 13. 分期

| 期 | 内容 |
| --- | --- |
| 第一期 | 表和迁移、凭据信封加密、设置页“Git 仓库”（添加 / 测试 / 编辑 / 删除，平台生成密钥对）、节点直连上游（凭证助手 + git-ssh 包装）、主节点合并镜像、节点 `remote_url`、`forge.Repo` 抽象和 Git 引擎合并队列、`merge_policy` 三档的降级、项目和新建项目里选仓库、验收 diff 接口 |
| 第二期 | PAT 连接转换、令牌到期提醒、上游人工合并检测优化、镜像配额管理界面、浅克隆 |
| 第三期 | GitLab / Gitea / GitHub 令牌的 PR 与 CI 适配（§8.4）、多副本、LFS |

## 14. 第一期实现说明

- **节点直连上游**（§5，2026-11-20 用户改定，取代最初的中转）：`delivery/cred.go` 的 `upstreamCredential` 按会话 / 作业下发上游令牌或私钥（30 分钟有效）；节点 `cred/helper.ts`（HTTPS 主机的凭证助手）、`cred/gitssh.ts`（git-ssh 包装 + 一次性 agent）、`git/jobs.ts`（作业的 askpass / git-ssh）。SSH 仓库必须先在设置里测试连接、固定主机密钥，否则不下发私钥。
- **主节点镜像**：`server/internal/gitrelay`，只给合并队列和讨论串分支用。`GIT_MIRROR_DIR`（compose 命名卷 `git-mirrors`，镜像里默认 `/var/lib/agent-teams/git-mirrors`）；没配置时独立仓库的合并会失败。合并用 `merge-tree --write-tree` + `commit-tree`，冲突照常回到组长。
- **交付**：独立仓库没有 PR / CI / 验收合并。`open_pr` 告诉组长直接 `finish`；`finish` 把讨论串分支当前的提交记为 `delivered_sha`，讨论串结束在分支上，用户自己去托管平台合并。合并队列的 CI 一律按“没有 CI”处理。§8 的 `merge_policy` 三档降级和验收 diff 接口留到后面。
- **节点要升级**：`git.prepare` / `git.push` 的 `remote_url`、`session.open` 的 `git_hosts`、`cred.response` 的 `ssh_key` 都是新字段，节点要升级到这个版本才能用独立仓库；GitHub 仓库不受影响。SSH 仓库要求节点装有 OpenSSH 客户端（节点镜像里有）。
- **还没做**：PAT 连接转换、令牌到期提醒、镜像配额界面（第二期）；GitLab/Gitea 的 MR 与流水线（第三期）；独立仓库的“CI 配置 PR”。

## 附录 A：引用的现有代码

| 位置 | 用途 |
| --- | --- |
| `server/internal/ghops/ghops.go` | 现在的仓库操作（`Repo`、`Token`、`Credential`、`Merge`、`CreatePR`、`CheckRuns`…），改成 `forge.Repo` 的 GitHub 实现 |
| `server/internal/delivery/{mergeq,pr,accept,ci,cred}.go` | 合并队列、PR、验收、CI、`cred.request` 处理，按 `Caps()` 分岔 |
| `server/internal/credvault` | 信封加密，加 `GitCredAAD` |
| `server/internal/sshdial` | SSH 拨号与主机密钥固定，测试连接和主节点镜像同步上游时复用 |
| `node/src/cred/helper.ts` 的 `gitCredentialEnv` | 凭证助手配置，加控制台 `/git/` 前缀 |
| `node/src/git/jobs.ts` 的 `GitJobs`（`gitBaseUrl`） | 作业的 remote 地址，改为优先用 `remote_url` |
| `protocol/schemas/node/cred.request.json`、`git.prepare.json`、`git.push.json` | §9 |
| `web/src/pages/settings/GitHub.vue`、`github/ConnectionCard.vue` | 改成“Git 仓库”页的 GitHub 分区 |
| `web/src/pages/settings/Servers.vue`、`servers/ServerCard.vue`、`ServerDialog.vue` | 新页面的样式参照 |
| `web/src/components/app/repo/{AddRepo,RepoRow,repo}.vue/ts`、`pages/projectSettings/RepoSection.vue` | 项目里选仓库 |

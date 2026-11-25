# 附件 200 MB 与对象存储：上线手册（#58）

本文是讨论串 #58（附件和共享文件改存 RustFS、附件放开到 200 MB、全程可引用）上生产的操作手册。
设计见交付物 `dl_01m89bx7jg5wdj464tkyhqp7ja`（共享文件 `design/58-attachments-design.md`），
接口见 `api.md` §3.4、§3.5.1、§3.5.2，表结构见 `data-model.md` §2.11、§2.13、§2.15，
节点协议见 `protocol.md` §4.3、§6.4。

生产环境：控制面在 dmit（`/root/agent-teams`，compose 项目 `agent-teams`），对象存储是 ovh 上的
RustFS（`https://rustfs.prophey.ai`，bucket `agent-teams`）。部署流程的通用部分（导出源码、构建
镜像、切换脚本、验证、回滚）照 `deploy` 的既有流程走，本文只写这次特有的步骤。

> **部署要用户明确同意。** 本文每一步都不能在没有授权的情况下执行。

---

## 0. 上线前必须知道的事

1. **存量附件 24 小时后会被永久删除。** 迁移 00460 给每一行已有附件设 `expires_at = 部署时刻 + 24h`。
   部署后 24 小时内没有被再次使用（发消息、转发、派任务、网页或节点下载…都算使用）的附件，
   会被 blobgc 标成 `expired`，字节（`data` 列）和对象一并删除，**只能靠部署前的 pg_dump 找回**。
   这是用户确认过的规则（存量附件和新附件一视同仁）。共享文件不过期。
2. **所以部署前必须 pg_dump**（§2），并且 dump 要留到确认不需要找回为止。
3. 附件不计入项目配额；共享文件上限改为单文件 200 MB、项目合计 5 GB、最多 1000 个文件。
4. 浏览器和节点直接和 RustFS 收发字节，控制面只签名、记账。所以 **dmit 和所有节点都要能访问
   `https://rustfs.prophey.ai`**，浏览器也要能访问（RustFS 的 CORS 只放行 `https://agent.prophey.ai`）。

## 1. 上线顺序

```
合入 develop ─▶ GHCR 发布新节点镜像 ─▶ 升级节点守护进程 ─▶ 备份数据库 ─▶ 部署控制面（迁移 00460、00470）
            ─▶ 验证（§5） ─▶ atctl storage migrate ─▶ verify ─▶（观察至少一个版本）─▶ purge-data
```

### 1.1 先升节点守护进程，再升控制面

- 新守护进程（hello 带 `attachments_v2`）连老控制面：一切照旧，只是两个新工具 `read_attachment`、
  `save_attachment` 会报“没有这个平台工具”。窗口很短，可以接受。
- 老守护进程连新控制面：控制面按老节点降级（`protocol.md` §4.3）：≤ 5 MB 的图片和 PDF 照旧作为
  `images` 发送（单条最多 8 张），文本附件内联进正文（> 60000 字节截断），其余附件在正文的
  `[附件]` 块里给出 id、名字、大小和“节点守护进程版本过旧”的说明；不会被老守护进程的 ajv 拒收
  （e2e 实测，见 §6）。所以控制面先升也不会坏，但老节点上的员工拿不到大文件。
- **已有的容器节点只在线更新守护进程，不重建容器**（用户决定）：
  - ovh 上的容器节点（gris-agent-team-1/2/3、wanaide01-agent-team-1）：等 develop 合入后 GHCR 发布
    `ghcr.io/touwaeriol/agent-teams-node:sha-<短sha>`，**先问用户**再改 compose 的镜像标签、
    `docker compose pull && docker compose up -d`（见项目记忆 node-daemon-upgrade.md、ovh-node-containers.md）。
    有回合正在跑的节点先别升（会打断回合）。
  - 宿主机安装的节点（claude、claude-cvp）和 docker-erio：节点包由生产控制面提供
    （`install-node.sh`），只能在控制面部署之后由用户自己升级；在此之前它们按老节点降级工作。
- 判断新旧：`at_nodes.last_hello->'features'` 含 `attachments_v2` 即为新守护进程。

```sql
SELECT name, last_hello->>'version' AS version, last_hello->'features' ? 'attachments_v2' AS v2
FROM at_nodes ORDER BY name;
```

## 2. 备份（必做）

```bash
cd /root/agent-teams
docker exec agent-teams-postgres-1 pg_dump -U agent_teams -d agent_teams -Fc > backups/pre-<sha>.dump
cp -p .env backups/env.pre-<sha>
docker exec -i agent-teams-postgres-1 pg_restore -l < backups/pre-<sha>.dump > /dev/null && echo dump-ok
```

再记一下附件和共享文件的现状，验证和回滚时对照：

```sql
SELECT count(*), pg_size_pretty(sum(size)::bigint) FROM at_attachments;
SELECT count(*), pg_size_pretty(sum(size)::bigint) FROM at_shared_files;
```

## 3. dmit 的 `.env`

加下面这些变量（**值从 `/root/agent-teams/rustfs-credentials` 取，不要贴出来、不要写进文档或记忆**；
那个文件里的键名是 `S3_ENDPOINT`、`S3_REGION`、`S3_BUCKET`、`S3_FORCE_PATH_STYLE`、`S3_ACCESS_KEY`、`S3_SECRET_KEY`）：

| 变量 | 取值 |
| --- | --- |
| `AGENT_TEAMS_S3_ENDPOINT` | `S3_ENDPOINT`（`https://rustfs.prophey.ai`） |
| `AGENT_TEAMS_S3_BUCKET` | `S3_BUCKET`（`agent-teams`） |
| `AGENT_TEAMS_S3_ACCESS_KEY` | `S3_ACCESS_KEY`（只授权这个 bucket 的服务端专用用户） |
| `AGENT_TEAMS_S3_SECRET_KEY` | `S3_SECRET_KEY` |
| `AGENT_TEAMS_S3_REGION` | `us-east-1`（可省略，默认就是） |
| `AGENT_TEAMS_S3_PATH_STYLE` | `true`（可省略，默认就是；RustFS 必须 path-style） |
| `AGENT_TEAMS_S3_PUBLIC_ENDPOINT` | 不设（浏览器和节点用同一个地址） |
| `AGENT_TEAMS_ATTACHMENT_TTL` | `24h`（可省略，默认就是；Go duration，最少 `1m`） |

写入方法（值不出现在命令行和终端上）：

```bash
cd /root/agent-teams
cp -p .env backups/env.pre-<sha>          # 已在 §2 做过就跳过
python3 -I - <<'PY'
import re
src = dict(l.split("=", 1) for l in open("rustfs-credentials").read().split("\n") if "=" in l)
add = {"AGENT_TEAMS_S3_ENDPOINT": src["S3_ENDPOINT"], "AGENT_TEAMS_S3_BUCKET": src["S3_BUCKET"],
       "AGENT_TEAMS_S3_ACCESS_KEY": src["S3_ACCESS_KEY"], "AGENT_TEAMS_S3_SECRET_KEY": src["S3_SECRET_KEY"],
       "AGENT_TEAMS_S3_REGION": src.get("S3_REGION", "us-east-1"), "AGENT_TEAMS_S3_PATH_STYLE": "true",
       "AGENT_TEAMS_ATTACHMENT_TTL": "24h"}
lines = [l for l in open(".env").read().split("\n") if not re.match(r"(AGENT_TEAMS_S3_|AGENT_TEAMS_ATTACHMENT_TTL=)", l)]
while lines and lines[-1] == "":
    lines.pop()
open(".env", "w").write("\n".join(lines + [f"{k}={v}" for k, v in add.items()]) + "\n")
PY
chmod 600 .env
grep -c '^AGENT_TEAMS_S3_' .env           # 期望 6
```

四个必填项（endpoint、bucket、access key、secret key）要么全配、要么全不配。全不配时控制面照常启动，
但所有新上传返回 503 `storage_unavailable`。部署前在 dmit 上确认能连到 RustFS：

```bash
curl -s -o /dev/null -w '%{http_code}\n' https://rustfs.prophey.ai/health/ready   # 200
```

## 4. 部署控制面

按通用流程构建镜像、切换（`deploy-<sha>.sh`）。这次的迁移：

- **00460_blob_storage**：`at_attachments` 的 `data`、`sha256` 改为可空，加 `storage_key`、`state`
  （uploading / ready / failed / expired）、`upload_id`、`expires_at`（已有行 = 部署时刻 + 24h）；
  `at_shared_files` 的 `data`、`sha256` 改为可空，加 `storage_key`、`object_id`、`declared_sha256`、
  `state`；新表 `at_shared_uploads`、`at_blob_deletions`；`at_tasks.attachments`。
  只改元数据和常量默认值，不重写存 BYTEA 的大表，执行很快。
- **00470_attachments_unowned**：`at_attachments.project_id` 可空（新建普通会话前上传的附件）；
  `at_threads.brief_attachments`；`at_deliverable_comments.attachments`。

两个迁移必须一起上（00470 依赖 00460）。迁移版本 450 → 470：

```sql
SELECT max(version_id) FROM goose_db_version WHERE is_applied;   -- 470
```

## 5. 验证清单

部署后逐项确认（用 admin 账号，在 admin 自己的节点上测；别的用户的节点调度不到）：

1. `/readyz` 三项 ok；`docker logs --since 10m agent-teams-control-plane-1` 没有 error / panic，特别是没有
   `object store unreachable`（启动时用 HeadBucket 自检，失败只记 error、不退出，之后的上传会失败）和
   `object store not configured`（`.env` 没配上）。
2. **网页上传 200 MB**：在项目对话里附一个 200 MB 的文件（例如 zip），进度条走完、能发送；
   浏览器开发者工具里分片 PUT 的目标是 `rustfs.prophey.ai`，不是 `agent.prophey.ai`。
   200 MB 以上的文件在选择时就被拒绝。
3. **节点下载**：把上面的消息发给统筹，或转发给一个讨论串、再让组长 `assign_task` 带上附件。
   节点会话的工作目录下出现 `.attachments/<id>-<名字>`，大小一致；正文末尾的 `[附件]` 块列出本地路径。
   ```sql
   SELECT id, state, sha256 IS NOT NULL AS hashed, storage_key IS NOT NULL AS in_store, expires_at FROM at_attachments ORDER BY created_at DESC LIMIT 5;
   ```
4. **共享文件**：网页上传一个 > 20 MB 的共享文件；让员工 `read_shared_file` 取回（新节点走预签名下载），
   再 `write_shared_file` 写一个 > 20 MB 的文件回去（新节点走 `POST /nodes/uploads` + PUT）。
5. **存为共享文件**：消息里的附件卡片点“存为共享文件”，共享文件页出现该文件。
6. **老节点降级**：还没升级的节点上的会话收到带附件的消息时不报错；正文 `[附件]` 块里大文件写着
   “节点守护进程版本过旧”。
7. **过期**：生产不改 TTL。可以只核对 blobgc 在跑：
   ```sql
   SELECT state, count(*) FROM at_attachments GROUP BY state;
   SELECT count(*), min(next_at) FROM at_blob_deletions;
   ```
   部署 24 小时后，没被使用的存量附件变成 `expired`，消息里显示“已过期”，下载返回 410。
8. **RustFS 匿名访问仍然 403**（bucket 私有）：
   ```bash
   curl -s -o /dev/null -w '%{http_code}\n' https://rustfs.prophey.ai/agent-teams/      # 403
   ```

## 6. 存量数据迁移：`atctl storage`

部署之后，老数据的字节还在 PG 的 `data` 列里，照样能读。用同一个镜像把它们搬进对象存储：

```bash
C="docker compose --project-directory /root/agent-teams/src --env-file /root/agent-teams/.env -f /root/agent-teams/src/deploy/docker-compose.yml"
$C run --rm migrate storage migrate --table all --dry-run   # 只统计
$C run --rm migrate storage migrate --table all             # 复制到对象存储，记 storage_key，data 保留
$C run --rm migrate storage verify --table all              # 逐行核对对象大小和 sha256
```

（`migrate` 服务和控制面用同一个镜像和同一组环境变量，入口是 `atctl`。）

- 每一步都可以中断后重跑（只处理还没处理的行）。迁移过来的共享文件对象 id 由项目和路径推出
  （`sf_legacy_…`），重跑写同一个 key。
- 存量附件 24 小时后多半会过期，迁移主要是为了共享文件和那些会继续被使用的附件。
- **`purge-data`（清掉 `data` 列）要等新版本稳定运行一段时间、确认不再回滚之后**才做，做之前再 pg_dump 一次：
  ```bash
  $C run --rm migrate storage purge-data --table all        # 每行重新校验，通过才清 data
  ```
  purge 之后回滚就必须先 `restore`（见 §7）。

## 7. 回滚

先问用户。新迁移已执行后，旧镜像的 `/readyz` 会因为迁移版本对不上而不通过，所以回滚要把迁移降回
450；**goose down 和用 dump 恢复都要用户先同意**。

| 阶段 | 做法 |
| --- | --- |
| 刚部署，还没有新上传、没跑 storage | 把迁移降到 450（见表下），`.env` 换回 `backups/env.pre-<sha>`、`src` 换回 `src.prev-<旧sha>`，再 `up -d --wait control-plane` |
| 有了新上传，或跑过 `purge-data` | 先 `$C run --rm migrate storage restore --table all`：把只在对象存储里的字节写回 `data`（只处理 ≤ 20 MB 的行）。再按上一行降迁移、换镜像 |
| 有 > 20 MB 的附件或共享文件 | 旧版本存不下它们（`restore` 不处理，00460 的 Down 会拒绝执行）。要么经用户同意删掉这些行，要么用部署前的 dump 恢复（部署之后的新数据全部丢失） |

`atctl` 没有 down 子命令。降迁移用 goose 命令行（在能连到库的地方，迁移目录用新版本的 `server/migrations`）：
`goose -dir server/migrations postgres "$DATABASE_URL" down-to 450`；或者在 psql 里按 470、460 的顺序手工执行两个文件的
`-- +goose Down` 段，再删掉 `goose_db_version` 里这两个版本的行。

降迁移时要知道：

- **00470 Down**：还没归属项目的附件（新建普通会话前上传、没用上的）整行删除，对象进删除队列。
- **00460 Down**：先删掉 `expired` 的附件行和没传完的上传，再检查：只要还有 `data` 为空的附件或共享文件行
  就报错退出（提示先 `atctl storage restore`）。被删掉的 expired 行在消息里从“已过期”变成“找不到”。
  已经过期删除的存量附件只能从 dump 找回。
- 对象存储里的对象不会因为回滚被删除；确认不再需要后可以手工清理 bucket（先问用户）。

## 8. e2e 联调结果（t40，2026-11-25）

脚本：`deploy/e2e/run_attachments.mjs`（守护进程入口 `deploy/e2e/attachments_daemon.mts`）。

- **实测（真组件）**：控制面（本分支编译的 atctl）、PostgreSQL 17、**生产 RustFS**（rustfs.prophey.ai，
  服务端专用凭证）、新守护进程（本分支 `node/`）、老守护进程（develop 181f3e8 的 `node/`，即生产版本）、
  所有 HTTP / WebSocket 往返、浏览器式分片上传、节点的下载和上传、sha256 校验。
- **假的（fake）**：AI。守护进程用测试替身代替 Agent SDK，按消息里的 `@@use <工具> <json>` 去调平台工具
  （AI 会做的调用按剧本做），按 `@@sh <命令>` 在会话目录里执行命令。
- 测试机不是 dmit，网络到 RustFS 很快（同机房级别）；dmit 到 RustFS 的实际速度要部署后再看。

| 步骤 | 结果 |
| --- | --- |
| 网页式分片上传 30 MB PDF + 42 MB zip | 通过，30 MB 约 0.1～0.4 秒；类型按文件头识别为 application/pdf、application/zip |
| 发给统筹 → `forward_to_thread` → 组长 `update_plan` + `assign_task` 带附件 → 成员会话 | 通过；成员 `.attachments/` 下两个文件 sha256 和原文件一致；从派任务到成员拿到文件约 1.5 秒；30 MB PDF 只给路径（超过 15 MB 文档上限） |
| 新节点 `write_shared_file` 25 MB（预签名 PUT）→ 另一会话 `read_shared_file` | 通过，`storage_key` 有值、`data` 为空，下载走预签名地址，sha256 一致 |
| 200 MB 共享文件：节点写入 | 消息到工具返回 3.0 秒（含生成文件、算 sha256、PUT 和 finalize）；控制面 finalize（复制到新 key + 流式算 sha256 并比对）**0.64 秒** |
| 200 MB 共享文件：节点下载 | 1.5 秒（约 133 MB/s），sha256 一致 |
| 老守护进程（无 attachments_v2）收首条消息（session.open）和后续消息（session.send） | 通过；PNG 和 1 MB PDF 作为内容块，30 MB PDF 和 zip 在 `[附件]` 块里按 id 说明“版本过旧”，.md 内容内联；老守护进程日志里没有任何 schema 拒绝 |
| 附件过期（控制面 `AGENT_TEAMS_ATTACHMENT_TTL=1m`）：节点离线时发一条带附件的消息，等附件过期再让节点上线 | 通过；最后一次使用后约 75 秒被 blobgc 标成 expired；`GET /nodes/attachments/{id}/url` 和网页下载都是 410；节点收到排队的消息，`[附件]` 块写“附件已过期（超过保留期没有再使用，文件已删除），请让用户重新上传”；消息列表里该附件带 `expired: true`；再次发送这个附件被拒（410 `attachment_expired`） |

联调中发现并修复的问题：

1. 节点预签名上传的 `write_shared_file` 在节点链路的消息处理里同步做 finalize（复制和流式算哈希）。
   慢的时候会挡住链路上的其他消息和心跳，超过 45 秒链路被判超时断开，重放时报主键冲突。
   改为调用事务提交后在后台完成（ebecce9），并加了回归测试。
2. 附件过期时节点只提示“下载失败：HTTP 410”并建议重试，改为直接提示“附件已过期…请让用户重新上传”，
   不再回退旧接口（4b673d0）。

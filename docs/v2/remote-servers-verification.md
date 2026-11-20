# 远程服务器：端到端与安全验证（#38）

本文记录讨论串 #38 收口时对远程服务器访问（`remote-servers.md`）做的端到端验证和安全红线专项：每一条都给出命令（或测试）和输出摘要。复现方法见 §1，结果汇总见 §2，残余风险见 §5。

验证时间 2026-11-20，分支 `thread/38-ssh-github`（含 t1～t6），机器是开发节点（Debian，OpenSSH 10 客户端与 sshd、rsync 3，非 root 账号，节点隔离级别 `none`，与生产节点一致）。

## 1. 环境与复现

一条命令起全部组件并跑完端到端：

```sh
server/scripts/e2e-ssh/run.sh        # 需要 go、node（已 npm ci）、PostgreSQL 二进制、OpenSSH、rsync
```

脚本做的事（`server/scripts/e2e-ssh/run.sh`）：

| 组件 | 怎么起 | 真 / 假 |
| --- | --- | --- |
| PostgreSQL | `initdb` 一个临时实例（端口 55499） | 真 |
| 控制台 | `atctl migrate up`（到 00330）→ `atctl serve`（`127.0.0.1:3738`，不配 Valkey） | 真，生产同一个二进制 |
| 放行本机网段 | `e2e-ssh allow -cidr 127.0.0.1/32`（写 `at_settings.ssh_allowed_private_cidrs`；不放行时网关拒绝回环地址，记 `address_refused`） | — |
| 服务器 pwbox（密码） | `e2e-ssh target`：x/crypto 写的 SSH 服务端，只收密码（含 keyboard-interactive），exec 用 `/bin/sh -c` **真执行**，sftp 子系统用 OpenSSH 的 `sftp-server`。非 root 的 OpenSSH sshd 校验不了密码，所以密码这台用它 | 真执行命令 |
| 服务器 keybox（私钥 + 口令） | OpenSSH `sshd -f …`（端口 2232，只收公钥），客户端私钥用 `ssh-keygen -N <口令>` 生成 | 真 |
| 守护进程 | `node/test/e2e-ssh.test.ts` 里 `new Daemon(...)` 连真控制台；`ssh-proxy` 经 CLI（tsx）由 ssh 拉起；Claude 用脚本化的假 query（组长会话依次调 `update_plan`、`assign_task` 给 dev 和 ops 派任务，成员会话不动） | 真守护进程，只有模型是假的 |
| 会话里的命令 | 拿守护进程交给 SDK 的那份会话 env，直接 spawn 真的 `ssh`、`scp`、`rsync`、`env`、`ssh-add` | 真 |

测试里的岗位：**A = `dev`**（被授权两台服务器），**B = `ops`**（成员岗位，没授权），组长 `lead`。报告写到 `$WORK/e2e-report.txt`（下文引用的输出都摘自 2026-11-20 那次完整运行，`Tests 1 passed`，约 28 秒）。

另有两组自动化测试随 CI 跑：

- 节点 `node/test/redline.test.ts`：git 凭证助手这条红线（§3.6）。
- 服务端 `server/internal/sshgw`、`server/internal/engine`（`TestSSHServersWiring`）、`server/internal/store` 的网关、证书、吊销、授权测试（§4）。

## 2. 结果汇总

| # | 项目 | 结果 |
| --- | --- | --- |
| E1 | 建两台服务器（密码；私钥 + 口令），测试连接固定 host key | ✅ 两台 `ok=true pinned=true` |
| E2 | 只给岗位 A 授权；组长授权被拒 | ✅ `coordinator`、`lead` 不可授权；给 lead 授权 422 `role_not_grantable` |
| E3 | A 会话 `ssh <服务器> 'echo ok'`、`scp`（双向）、`rsync -a` | ✅ 两台都成功，目标上文件内容正确；`exit 7` 原样返回 |
| E4 | B 会话、组长会话被拒 | ✅ 没有 ssh_config，`ssh pwbox` 解析不到；借 A 的 ssh_config 也被 ssh-proxy 拒绝 |
| E5 | 审计有对应行 | ✅ 37 行，全是 dev-1；exec 带完整命令和退出码，sftp、rsync、`auth_fail`、吊销断开都有 |
| R1 | 会话 env 里没有密码、私钥、口令、GitHub 令牌、证书 | ✅ 三个会话 `env` 都没命中 |
| R2 | 会话目录、平台 bin、节点数据目录里没有秘密 | ✅ 会话目录只有 `ssh_config`、`known_hosts`（和 socket）；数据目录 20 个文件搜不到 |
| R3 | 会话结束：目录删掉、`agent.sock` 不在、正在跑的连接被断、旧环境不能再用 | ✅ |
| R4 | 证书换到别的会话 / 别的 `open_gen` | ✅ 网关 404 拒绝（对照组同证书同会话成功） |
| R5 | 宿主机其他进程不带每会话秘密连 socket | ✅ `ssh.sock`、`cred.sock` 都拒；⚠️ `agent.sock` 同账号可用（§5） |
| R6 | git 凭证助手领令牌后，env、git 配置、磁盘上都没有令牌；会话 env 无 `GH_TOKEN` | ✅ |
| R7 | 授权撤销时正在跑的连接 | ✅ 立即断开（stderr 写原因），ssh_config 随即去掉这台 |

## 3. 逐条：命令与输出

### 3.1 建服务器与授权（E1、E2）

`POST /api/servers` 两次（pwbox 带 `auth.kind=password`；keybox 带 `auth.kind=private_key` 和 `passphrase`），再各 `POST /api/servers/{id}/test`：

```
服务器：pwbox（密码，Go 目标 127.0.0.1:2231）、keybox（私钥+口令，OpenSSH sshd 127.0.0.1:2232，has_passphrase=true）
测试连接 pwbox：ok=true pinned=true SHA256:yo020Ha03VuPd674wdCPnUjrQoFN09eenfE4QHXiXUw
测试连接 keybox：ok=true pinned=true SHA256:m4LDsNbbQ8+nyCPy8JCAtd+dsYMWZb52F/U+zxgld9Y
```

`PUT /api/projects/{pid}/servers`，两台都只给 `dev`；`GET` 回来的岗位和给组长授权的结果：

```
可授权岗位：coordinator=否(coordinator) lead=否(lead) dev=可 ops=可
给组长授权：422 role_not_grantable
```

开讨论串后三个会话的 env（守护进程交给 SDK 的那份）：

```
会话：dev-1 AT_SSH_CONFIG=有；ops-1 AT_SSH_CONFIG=无；lead-1 AT_SSH_CONFIG=无
```

### 3.2 岗位 A：ssh、scp、rsync（E3）

都在 dev-1 的会话 env 里执行（`PATH` 前面是平台 bin，`ssh` / `scp` 是包装脚本，补上 `-F $AT_SSH_CONFIG`；rsync 默认的 `-e ssh` 也走这个包装）：

```
dev-1 $ ssh pwbox 'echo ok' → exit 0 · out: ok
dev-1 $ scp payload.txt pwbox:… → exit 0；目标上内容="payload-e2e\n"
dev-1 $ rsync -a tree/ pwbox:… → exit 0；同步后 sub/b.txt="b\n"
dev-1 $ ssh keybox 'echo ok' → exit 0 · out: ok
dev-1 $ scp payload.txt keybox:… → exit 0；目标上内容="payload-e2e\n"
dev-1 $ rsync -a tree/ keybox:… → exit 0；同步后 sub/b.txt="b\n"
dev-1 $ ssh pwbox 'exit 7' → exit 7
dev-1 $ ssh root@pwbox 'id -un'（想换用户）→ exit 255 · err: root@pwbox: Permission denied (publickey).
```

`scp` 还反向拷回（`scp pwbox:… 本地`）一次，也成功。`root@pwbox` 被网关拒绝（用户名必须等于服务器名，审计 `auth_fail reason=user_must_be_server_name`）。

### 3.3 岗位 B 和组长（E4）

```
ops-1 $ ssh pwbox 'echo ok' → exit 255 · err: ssh: Could not resolve hostname pwbox: Name or service not known
ops-1 $ ssh -F <dev-1 的 ssh_config> pwbox → exit 255 · err: agent-teams ssh: this shell is not an agent-teams session with remote servers (AT_CRED_SESSION missing)
lead-1 $ ssh pwbox 'echo ok' → exit 255 · err: ssh: Could not resolve hostname pwbox: Name or service not known
lead-1 $ ssh -F <dev-1 的 ssh_config> pwbox → exit 255 · err: agent-teams ssh: this shell is not an agent-teams session with remote servers (AT_CRED_SESSION missing)
```

B 和组长的会话里根本没有 ssh_config 和会话秘密；控制台那边，`session.open` 不带 `ssh_servers`，`ssh.cert_request` 会回 `no_servers` / `not_granted`，网关每个 channel 还会再查（服务端测试 `TestGatewayRefusesUngrantedSessions`、`TestSSHServersWiring`，§4）。

### 3.4 证书换会话 / 换 open_gen（R4）

测试用节点令牌直接连网关 `/nodes/ssh`（一个小的 WebSocket 转发脚本当 ProxyCommand），身份仍用 dev-1 的 agent（证书是签给 dev-1 这个会话的），只改请求头里声明的会话和 `open_gen`：

```
直连网关：dev-1 证书 + dev-1 会话头 + open_gen=1（对照组）→ exit 0 · out: ok
直连网关：dev-1 证书 + ops-1 会话头 → exit 255 · err: gateway refused: 404
直连网关：dev-1 证书 + lead-1 会话头 → exit 255 · err: gateway refused: 404
直连网关：dev-1 证书 + open_gen=2 → exit 255 · err: gateway refused: 404
```

升级阶段就拒（会话不是这个节点的活会话、或 `open_gen` 不是当前值）。即使头对上了，证书扩展里的 `session@` / `gen@` / `node@` 还要和头一致（`TestGatewayCertificateChecks/other_session's_certificate`）。升级阶段的 404 不写 `at_ssh_audit`，只写服务端日志。

### 3.5 没有每会话秘密的进程（R5）

```
dev-1 环境但 AT_CRED_SECRET 错误 $ ssh pwbox → exit 255 · err: agent-teams ssh: unknown session (the session ended or has no remote servers)
dev-1 环境去掉 AT_CRED_* $ ssh pwbox → exit 255 · err: agent-teams ssh: this shell is not an agent-teams session with remote servers (AT_CRED_SESSION missing)
同账号进程直接连 agent.sock $ ssh-add -L → exit 0 · out: ssh-ed25519-cert-v01@openssh.com AAAAIHNz…
```

- `ssh.sock`（ssh-proxy 和守护进程之间）核对每会话秘密，错了或没有都拒。
- `cred.sock`（git 凭证助手）同样核对：错的秘密、换成别的会话 id 都领不到令牌（§3.6）。
- **`agent.sock` 不核对秘密**，只靠目录 0700 / socket 0600：同一系统账号的进程能列出证书、请它签名。这是已知的残余风险（§5 第 1 条）；就算借到签名，还要过 `ssh.sock` 的秘密和节点令牌才连得上网关。

### 3.6 秘密不进 env、文件、git 配置（R1、R2、R6）

会话 env（在会话 env 里真跑 `env`，搜目标密码、私钥口令、私钥正文中间一行、节点令牌，以及特征串 `BEGIN OPENSSH PRIVATE KEY`、`-cert-v01@openssh.com`、`ghs_`、`ghp_`、`GH_TOKEN=`、`GITHUB_TOKEN=`、`SSH_AUTH_SOCK=`）：

```
dev-1 $ env：14 个变量；命中秘密=[]；命中特征=[]；AT_* 变量=AT_CRED_SESSION,AT_CRED_SECRET,AT_SSH_CONFIG
ops-1 $ env：11 个变量；命中秘密=[]；命中特征=[]；AT_* 变量=
lead-1 $ env：11 个变量；命中秘密=[]；命中特征=[]；AT_* 变量=
```

会话目录和文件内容（`cat`）：

```
dev-1 会话目录 …/node/ssh/s_01m7waw7…：known_hosts, ssh_config（另有 agent.sock）
known_hosts：agent-teams-gateway ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAeMrVhv…   ← 网关的公钥
平台 bin/ssh：#!/bin/sh ⏎ if [ -n "$AT_SSH_CONFIG" ] && [ -f "$AT_SSH_CONFIG" ]; then ⏎ exec /usr/bin/ssh -F "$AT_SSH_CONFIG" "$@" ⏎ fi ⏎ exec /usr/bin/ssh "$@"
节点数据目录 20 个文件里搜秘密：无
```

`ssh_config` 每台一段 `Host <名> / User <名> / HostKeyAlias agent-teams-gateway / ProxyCommand … ssh-proxy --socket <ssh.sock> --server <名>`，公共段是 `IdentityAgent <agent.sock>`、`UserKnownHostsFile <会话目录>/known_hosts`、`StrictHostKeyChecking yes`、`PasswordAuthentication no`、`ForwardAgent no`、`ControlMaster no` 等，**没有任何秘密**（会话秘密由 ssh-proxy 从继承的环境里读）。

git 凭证（`node/test/redline.test.ts`，真 `git credential fill` 经凭证助手，假控制台回一个 `ghs_REDLINE_…` 令牌）断言：

- `git credential fill` 拿到 `password=ghs_REDLINE_…`，控制台收到的 `cred.request` 带 `session_id`、`open_gen`、`host`、`path`。
- 会话 env 里没有这个令牌，也没有 `GH_TOKEN`、`GITHUB_TOKEN`、`GH_ENTERPRISE_TOKEN`、`GITHUB_ENTERPRISE_TOKEN`；在会话 env 里跑 `env` 也没有。
- `git config --list --show-origin` 里只有 `credential.https://github.com.helper=!… git-credential --socket … --session s_red --secret …`，没有令牌。
- 节点所有目录（数据、状态、平台、run、ssh、tmp）里搜不到令牌。
- 换错的秘密、换别的会话 id：领不到。
- 会话关闭后用旧 env 再领：领不到，控制台也没再收到 `cred.request`；磁盘上仍然搜不到。

`gh`：令牌只放进那一次 gh 子进程的 `GH_TOKEN` / `GITHUB_TOKEN`，会话 env 不变、`GH_CONFIG_DIR` 里没有 `hosts.yml`、继承来的 `GH_TOKEN` 先被删掉（`node/test/gh.test.ts` 的 “runs the real gh with a per-session token only in the child's environment”，t2）；本机没有装真的 gh，所以 gh 这条用的是假 gh 二进制。

### 3.7 授权撤销与会话结束（R3、R7）

```
dev-1 正在 ssh pwbox 'sleep 60' 时撤销 pwbox 授权：0.0 秒后断开，exit 255 · err: agent-teams: 会话证书已吊销（grant_removed），连接已断开
撤销后 dev-1 $ ssh pwbox → exit 255 · err: ssh: Could not resolve hostname pwbox      ← ssh.servers_update 已去掉这台
（keybox 不受影响：ssh keybox 'echo still' → still）

归档讨论串：200
归档时 dev-1 正连着 keybox（sleep 60）：0.0 秒后断开，exit 255 · err: agent-teams: 会话证书已吊销（session_closed），连接已断开
归档讨论串后：会话目录存在=false，agent.sock 存在=false
会话结束后用旧环境 $ ssh pwbox → exit 255 · err: ssh: Could not resolve hostname pwbox
会话结束后连 agent.sock：connect ENOENT …/agent.sock
```

“0.0 秒”是从 `PUT` / 归档请求返回开始计时：连接在请求返回之前就已经断了（吊销在事务提交后立即广播）。会话结束后私钥随 agent 一起消失，抓到的证书没有私钥签名、而且已吊销，网关拒绝已吊销证书见 `TestGatewayCertificateChecks/revoked`、`TestGatewayRevocation`。

### 3.8 审计（E5）

`GET /api/projects/{pid}/server-audit`（去掉成对的 `connect` / `disconnect closed`）：

```
dev-1(dev) keybox exec "echo still" exit=0 2ms
dev-1(dev) pwbox exec "sleep 60; echo late" exit=- 1118ms          ← 被吊销打断：有时长、没有退出码
dev-1(dev) pwbox exec "echo ok" exit=0 1ms                          ← §3.4 的对照组
dev-1(dev) pwbox auth_fail "" reason=user_must_be_server_name       ← ssh root@pwbox
dev-1(dev) pwbox exec "exit 7" exit=7 1ms
dev-1(dev) keybox exec "rsync --server -logDtpre.iLsfxCIvu . …/tree/" exit=0 26ms
dev-1(dev) keybox subsystem "sftp" exit=0 3ms                        ← scp（两次）
dev-1(dev) keybox exec "echo ok" exit=0 2ms
dev-1(dev) pwbox …（同上一组）
dev-1(dev) pwbox disconnect reason=会话证书已吊销（grant_removed）
归档后：dev-1 keybox disconnect reason=会话证书已吊销（session_closed）
```

没有任何 ops-1、lead-1 的行：它们的请求在节点上（没有 ssh_config / 会话秘密）或网关升级阶段（404）就被拒了。用户自己的测试连接记在服务器的访问记录里（`kind=test`、`employee=user`），不在项目访问记录里。

## 4. 服务端测试（随 CI）

```
$ go test -count=1 -v -run 'TestGateway(RefusesUngrantedSessions|CertificateChecks|Revocation|HostKeyPinning|CertRateLimit|KillDoesNotWaitForAStuckClient)' ./internal/sshgw/
--- PASS: TestGatewayRefusesUngrantedSessions
--- PASS: TestGatewayCertificateChecks   （子用例：plain_key、user@_form、foreign_CA、expired、critical_option、principal_missing、other_session's_certificate、unknown_serial、revoked）
--- PASS: TestGatewayRevocation
--- PASS: TestGatewayKillDoesNotWaitForAStuckClient
--- PASS: TestGatewayHostKeyPinning
--- PASS: TestGatewayCertRateLimit
$ go test -count=1 -v -run TestSSHServersWiring ./internal/engine/      --- PASS（组长 session.open 不带服务器；成员带；证书响应里没有服务器密码）
$ go test -count=1 -v -run 'TestProjectServerGrants|TestProjectServerRoles' ./internal/store/   --- PASS
```

## 5. 残余风险（如实记录）

1. **同一系统账号的进程在会话存活期内可以借用会话身份**。节点隔离级别 `none` 时，员工会话、守护进程和宿主机上同账号的其他进程是同一个 uid：
   - 能连会话的 `agent.sock`（实测 `ssh-add -L` 列出了证书，也能请它签名）；
   - 能读会话进程的环境变量，拿到 `AT_CRED_SESSION` / `AT_CRED_SECRET`，再连 `ssh.sock` / `cred.sock` 冒充这个会话连服务器、领 GitHub 令牌。这一步本次没有实际演示（开发环境的命令钩子不允许读别的进程的环境），但这是同 uid 下的已知能力，`remote-servers.md` §8.2 早已写明。
   - 限度：凭证本身（密码、私钥、口令、App 私钥）拿不走；只在会话存活期内、只在本节点上有效；每次连接都过网关的证书、会话、`open_gen`、岗位授权校验并审计；会话结束（或授权撤销）立即作废。
   - 根除要等 `agent_user` 隔离（`architecture.md` §10.3、`remote-servers.md` §8.4）。用户在 Q11 选了第一期接受。
2. `gh` 运行期间，它的子进程环境里有那一次的令牌，同账号进程能读到（t2 已记）。
3. **PAT 连接没有收紧**：PAT 项目的员工会话仍直接拿 PAT（节点缓存 30 分钟），PAT 长期有效、权限是整个账号（`remote-servers.md` §10.3 第 1 条、Q8，待以后决定）。
4. **第一期没有首次连接批准**：被授权岗位的会话里，提示注入能做的事等于目标上部署用户的权限；建议部署用户最小权限。
5. 网关升级阶段（HTTP 404）的拒绝只写服务端日志，不进访问记录；被连接断开打断的 exec 只有时长、没有退出码（网页显示时长）。
6. 连接数上限按副本计数，多副本时实际上限是“单副本上限 × 副本数”。
7. 节点令牌由守护进程保存在它的配置里（不是每会话秘密，本次在数据目录里没搜到，是因为测试直接把令牌传给了 Daemon）；节点机器被攻破时，攻击者在会话存活期内能做 AI 能做的事（`remote-servers.md` §14）。

## 6. 没有覆盖到的

- `sandbox` 级别节点：开发节点没有 bubblewrap，没法实测；这类 Claude 会话第一期不注入 SSH（`remote-servers.md` §8.3）。
- Codex 会话：端到端只跑了 Claude 会话（Codex 的注入路径有节点单元测试 `codex.test.ts`）。
- 多副本控制台：只起了一个副本（吊销广播走的是不配 Valkey 时的进程内实现）。
- 真实的 GitHub：git 凭证这条用假控制台回令牌，没有连 GitHub。

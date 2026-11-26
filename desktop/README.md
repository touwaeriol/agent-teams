# Agent Teams 桌面端（Mac / Windows）

一个 Electron 壳，窗口里直接加载线上站点 <https://agent.prophey.ai>。网页、接口和资源都来自同一个站点，所以 cookie、CSRF、SSE、语音 WebSocket 的 Origin 校验、交付物 iframe 都跟在浏览器里一样，**web/ 和 server/ 不需要为桌面端做任何改动**。网页更新后，用户下次打开（或重新加载）就是新版，只有壳本身的改动才需要发新安装包。

不上 Mac App Store，从官网和 GitHub Releases 下载。

## 目录

```
desktop/
  src/
    main.ts          主进程：窗口、导航、权限、深链接、菜单、离线页、自动更新的接线
    preload.ts       沙盒 preload，只暴露 window.agentTeamsDesktop = { version, platform }
    config.ts        加载哪个站点（AGENT_TEAMS_URL）、User-Agent
    navigation.ts    链接留在 App 里 / 交给系统浏览器 / 拒绝
    deeplink.ts      agentteams://<路径> → 站点上的同一路径
    permissions.ts   放行哪些权限
    menu.ts          应用菜单模板、开发者工具快捷键
    windowState.ts   窗口大小和位置（纯逻辑），windowStore.ts 负责读写文件
    offline.ts       什么样的加载失败显示离线页
    updater.ts       electron-updater
    *.test.ts        vitest 单元测试（纯逻辑模块）
  static/            离线提示页（offline.html + offline-page.js），构建时复制进 dist/
  build/             electron-builder 资源：icon.png、entitlements.mac.plist
  scripts/           make-icon.mjs（生成图标）、copy-static.mjs
  ci/desktop-release.yml   GitHub Actions workflow，需要手动复制到 .github/workflows/
  electron-builder.config.cjs   打包、签名、公证、发布配置
```

## 本地开发

需要 Node 22 以上。

```bash
cd desktop
npm ci
npm run typecheck      # tsc
npm test               # vitest
npm run build          # tsc → dist/，并复制 static/
npm start              # build 后用 electron 打开 https://agent.prophey.ai
```

连本地前端调试：先在 web/ 里 `npm run dev`（或 `npm run dev:mock`），然后

```bash
AGENT_TEAMS_URL=http://127.0.0.1:5173 npm start
```

`AGENT_TEAMS_URL` 只接受 http/https 地址，写错了就回落到线上站点。开发模式（没打包）下“显示”菜单里有“开发者工具”；打包后的版本也能用快捷键打开：Mac 上 `Cmd+Alt+I`，Windows 上 `Ctrl+Shift+I`。

## 行为说明

| 方面 | 做法 |
| --- | --- |
| 窗口 | 记住大小、位置和是否最大化（保存在 userData/window-state.json；显示器拔掉后位置作废，只保留大小）。只允许一个实例，再次启动会聚焦已有窗口。Mac 关窗口不退出，点 Dock 图标重新打开；Windows 关掉所有窗口就退出。 |
| 安全 | contextIsolation、sandbox 打开，nodeIntegration 关闭，禁用 `<webview>`。网页拿不到任何 Node 能力，只能读 `window.agentTeamsDesktop.version` / `.platform`。 |
| 链接 | 同源（与加载的站点同协议、同域名、同端口）的跳转留在 App 里；`window.open` 打开同源地址（比如讨论串菜单里的“在新窗口中打开”）会在 App 里开新窗口。其他 http/https 链接一律交给系统浏览器打开；其他协议（file:、javascript:、mailto: 等）直接拒绝。 |
| 权限 | 只对加载的站点放行：麦克风（只要音频，摄像头拒绝）、通知、剪贴板写入。其他权限（定位、摄像头、剪贴板读取、MIDI、USB 等）和其他来源（比如交付物 iframe）一律拒绝。 |
| 深链接 | 注册 `agentteams://` 协议。`agentteams://projects/<pid>/threads/12` 打开站点上的 `/projects/<pid>/threads/12`，其他路径同理（`/sessions/new`、`/projects/<pid>/deliverables` 等，见 web/src/router.ts）。只接受站点上的路径：带协议的（`agentteams://https://…`）、带反斜杠或控制字符的、超过 2048 字符的都会被忽略。Mac 走 open-url 事件，Windows 走第二个实例的命令行参数。 |
| 菜单 | Mac：应用菜单、文件、编辑（没有它 Cmd+C/V 不能用）、显示（重新加载、缩放、全屏）、窗口、帮助。Windows：同样的菜单，菜单栏默认隐藏，按 Alt 显示。 |
| 离线 | 主页面加载失败（断网、DNS、证书……）时显示本地中文提示页，带“重试”按钮；网络恢复（online 事件）时自动重新加载原来的地址。 |
| 下载 | 用 Electron 默认行为：附件、共享文件的下载会弹出系统保存对话框。 |
| User-Agent | 原来的 UA 后面追加 `AgentTeamsDesktop/<版本号>`，网页以后可以据此识别桌面端。 |
| 通知 | Windows 上设了 AppUserModelId（`ai.prophey.agentteams`），否则系统通知弹不出来。 |

## 图标

`build/icon.png`（1024×1024）由 `web/public/favicon.svg` 生成：`npm run icon`（scripts/make-icon.mjs，用 @resvg/resvg-js 渲染，四周留约 10% 边距，背景透明）。electron-builder 打包时从它生成 Mac 的 .icns 和 Windows 的 .ico。favicon 改了就重跑一次并提交 PNG。

## 打包

```bash
npm run dist          # 当前系统的安装包（Mac：dmg + zip，x64 和 arm64；Windows：NSIS 安装包 x64）
npm run dist:dir      # 只出解包后的目录，不做安装包，检查配置用
```

产物在 `desktop/release/`。

- Mac 的 dmg 只能在 macOS 上打。
- Windows 的 NSIS 安装包在 Linux 上需要 wine；`npx electron-builder --config electron-builder.config.cjs --win dir` 不需要 wine，可以用来检查配置。
- 要 Windows on ARM 的包：在 electron-builder.config.cjs 的 `win.target` 里加上 `"arm64"`（安装包会同时带两种架构，体积翻倍）。
- `linux` 段只是为了能在 Linux 上 `--linux dir` 检查配置，不发布。

## 签名和公证

签名配置已经写在 `electron-builder.config.cjs` 里，**由环境变量开关**：有证书就签名，没有就出未签名的包，不会报错。

### Mac（Developer ID + 公证）

| 环境变量 | 内容 |
| --- | --- |
| `CSC_LINK` | “Developer ID Application” 证书导出的 .p12（文件路径、https 地址或 base64） |
| `CSC_KEY_PASSWORD` | .p12 的密码 |
| `APPLE_ID` | 开发者账号的 Apple ID |
| `APPLE_APP_SPECIFIC_PASSWORD` | 在 appleid.apple.com 生成的 App 专用密码 |
| `APPLE_TEAM_ID` | 10 位团队 ID |

- 有 `CSC_LINK` 时：用 Developer ID 签名，打开 hardened runtime，entitlements 用 `build/entitlements.mac.plist`（JIT、`com.apple.security.device.audio-input` 麦克风）。Info.plist 里有 `NSMicrophoneUsageDescription`。
- 三个 `APPLE_*` 都有时：签名后自动提交公证（notarytool）并装订。只设了其中一部分会报错。
- 没有 `CSC_LINK` 时：做 ad-hoc 签名（arm64 的 Mac 没有签名的程序根本启动不了），不开 hardened runtime，不公证。

### Windows

二选一：

- **.pfx 证书**（OV/EV）：`WIN_CSC_LINK`（.pfx 路径或 base64）、`WIN_CSC_KEY_PASSWORD`。
- **Azure Artifact Signing**（原 Trusted Signing）：`AZURE_SIGN_ENDPOINT`、`AZURE_SIGN_ACCOUNT`、`AZURE_SIGN_PROFILE` 三个都有时启用，另外需要服务主体的 `AZURE_TENANT_ID`、`AZURE_CLIENT_ID`、`AZURE_CLIENT_SECRET`。

都没有时出未签名的安装包（日志里是 “no code signing certificate configured, signing is skipped”）。

## 没签名时用户会看到什么、怎么绕过

**Mac**（未签名、未公证，只有 ad-hoc 签名）

- 双击打开会提示“无法验证开发者”或“‘Agent Teams’已损坏，无法打开”。
- 绕过办法（任选一种）：
  - 在“应用程序”里右键（或按住 Control 点）Agent Teams →“打开”→ 再点“打开”。macOS 15 以后这一招不再有效，要去“系统设置 → 隐私与安全性”，在下方找到被拦下的 Agent Teams，点“仍要打开”，输入密码。
  - 终端执行 `xattr -dr com.apple.quarantine "/Applications/Agent Teams.app"`。
- **没签名的 Mac 版不能自动更新**（Squirrel.Mac 要求签名），只能手动下载新版覆盖。

**Windows**（未签名）

- SmartScreen 弹出“Windows 已保护你的电脑”。点“更多信息”→“仍要运行”即可安装。
- 浏览器（Edge、Chrome）下载时可能提示“不常下载”，选“保留”。
- 未签名的 Windows 版可以自动更新（electron-updater 只在已签名时校验发布者一致）。

签名之后：Mac 公证过的包直接打开；Windows 用 OV 证书或 Artifact Signing 签名后 SmartScreen 仍可能提示一段时间，下载量积累出信誉后消失。

## 发布

发布走 GitHub Actions，workflow 在 `ci/desktop-release.yml`。**平台令牌不能改 .github/workflows，需要仓库管理员手动复制一次**：

```bash
cp desktop/ci/desktop-release.yml .github/workflows/desktop-release.yml
```

它做的事：

- 改了 desktop/ 的每次 push：Linux 上跑 typecheck 和测试，再在 `macos-latest` 和 `windows-latest`（GitHub 自带的 runner）上打包，产物作为 workflow artifacts 上传（Actions → 那次运行 → Artifacts）。
- 推送 `desktop-v<版本号>` 的 tag：同样打包，然后建 GitHub Release，上传 dmg、zip、exe、blockmap 和 `latest.yml` / `latest-mac.yml`。

发一个新版本：

```bash
cd desktop
npm version 0.2.0 --no-git-tag-version   # 改 package.json 和 package-lock.json 里的版本号
git commit -am "desktop: 0.2.0"
git tag desktop-v0.2.0 && git push && git push origin desktop-v0.2.0
```

tag 必须和 package.json 的版本号一致（workflow 会检查）。签名用的 secrets 列在 workflow 文件头部的注释里，都是可选的。

## 自动更新

- 用 electron-updater，更新源是 GitHub Releases（touwaeriol/agent-teams），配置在 electron-builder.config.cjs 的 `publish`，打包时写进 App 里的 app-update.yml。
- 只在打包后的正式版本里检查：启动时检查一次，有新版就在后台下载，退出 App 时安装，并弹系统通知。菜单里也有“检查更新…”。开发模式（`npm start`）不检查；环境变量 `AGENT_TEAMS_NO_UPDATE=1` 也可以关掉。
- electron-updater 取的是仓库的 **Latest release**。这个仓库目前只有桌面端在发 Release；以后如果别的东西也要发 Release，要么标成 pre-release / 不设为 latest，要么把桌面端的发布挪到单独的公开仓库（改 `publish` 的 `repo`）。
- 没签名的 Mac 版不能自动更新，见上文。检查失败只写日志，不影响使用。

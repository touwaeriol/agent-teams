# 办公室布局 JSON

办公室（设计稿 `Office` 画板）完全由一个 JSON 文件描述：房间、地板、墙、家具、数据驱动的小部件（任务板、机柜、合并队列 / CI 面板）、讨论串工作台的模板与排布、成员的工位区、走路的路网、员工状态到位置的规则、人物像素图和岗位形象。**改 JSON 就能重画办公室**，不改代码、不重新部署。

- Schema：`office-layout.schema.json`（JSON Schema draft 2020-12，`/protocol/schemas/office/` 是它的正式位置，本目录这份与之相同）。
- 示例：`office.example.json`（= `/office/default.json`），共享成员（`shared-members.md` §9）的办公室：开发区上方一排讨论串工作台（每张只坐组长）、下方成员工位区（按岗位分排）、任务板（项目队列卡片）、决策室、项目统筹办公室、休息区、等候凳、机房的机柜 / 合并队列 / CI。场景尺寸 1180 × 892；其余家具和房间的坐标取自设计稿。
- 共享成员之前的布局（成员坐在讨论串工作组里，没有 `staffArea`）照样能用：成员按旧规则坐进讨论串的 pod，见 §4.4。

## 1. 坐标与单位

- 场景坐标单位是 CSS px，原点在场景左上角。`canvas.width/height` 是场景尺寸；渲染器按容器宽度整体缩放（只按整数倍或 0.5 倍缩放以保持像素清晰），放不下时容器滚动（同设计稿）。
- `rect` 一律写 `[x, y, w, h]`，点写 `[x, y]`。
- **人物位置 = 人物精灵的左上角**（与设计稿一致：设计稿里 `lead-12` 在 `[76, 96]`）。座位 `seat`、锚点 `anchors.*.points`、路网节点都用这个约定，所以“站到某点”就是把精灵左上角放到那点。`characters.person.anchor` 是脚底点（相对精灵左上角），只用于前后遮挡排序（按 `y + anchor.y`）。
- 讨论串工作组（pod）内部的坐标是**相对 pod 左上角**的；实例化时加上 pod 的位置。
- `canvas.pixel` / `characters.person.pixel`：一个“美术像素”等于多少 CSS px（设计稿是 4）。

## 2. 颜色

能写颜色的地方接受：

| 写法 | 含义 |
| --- | --- |
| `#RGB` `#RRGGBB` `#RRGGBBAA` `rgb()` `rgba()` | 字面颜色 |
| `$name` | `palette` 里的命名颜色 |
| `@slot` | 运行时槽位：精灵的 `slots`（如桌子的 `@screen`），或人物形象槽位（`@hair` `@skin` `@shirt` `@accent` `@pants` `@color` `@tag`） |
| `{threadColor}` | 模板变量：所在讨论串的颜色（`threadPods.threadColors` 按讨论串编号轮换） |
| `shade(<颜色>, k)` | k ∈ [-1, 1]，负数变暗、正数变亮（HSL 亮度按比例调整） |

## 3. 顶层结构

```
format, id, name, extends?
canvas      场景尺寸、像素倍数、背景、外框、页面底色
fonts       label（房间名、名牌）、bubble（气泡）、googleFonts（需要加载的字体）
palette     命名颜色
floors      地板样式：plain | planks（横竖两组木纹线）| grid | checker | image
sprites     家具和小部件的精灵：shapes（矩形/椭圆组合）| pixels（字符网格）| image（/office/assets 下的图片或图集）
characters  人物像素图（必须有 person）：parts、frames、animations、overlays
looks       服装、配饰、色板、发色、按岗位的默认形象
rooms       房间：矩形、地板、墙（实线/虚线）、名牌样式、kind
furniture   静态家具：精灵 + 位置 + 所属房间
widgets     数据驱动的小部件：taskBoard、nodeRacks、pipelinePanel
threadPods  讨论串工作台：区域、模板（组长座位、参与者头像条、访客位、局部路网）、排布规则、入座规则、结束时淡出
staffArea   成员工位区（可选）：区域、按岗位分排、每排桌数、溢出方式、行走道
members     成员怎么摆（可选）：visitPods = 干活时去讨论串工作台的访客座（默认 false，坐自己工位）
anchors     命名的位置组：任务板前、休息区、等候凳、决策室、统筹桌……
nav         全局路网：节点 + 无向边 + 步行速度
states      员工状态 → 去哪、什么动作、桌面屏幕颜色、气泡；以及“领任务”这类动画序列
labels      房间名、名牌、计时器的文字样式
bubbles     气泡：字体、最长字数、按种类（plain/ask/error/done）的颜色
focus       聚焦单个讨论串时其他内容的透明度
legend      侧栏图例
```

`extends`（可选）：继承另一个布局，只写差异。合并规则：对象按键深合并；`rooms`、`furniture`、`widgets` 按 `id` 合并（同 id 的条目整体覆盖，新 id 追加；不支持按 id 删除，要删就重写整个数组）；其他数组整体替换。

## 4. 各部分

### 4.1 房间、地板、家具

```json
{ "id": "machine", "label": "机房", "kind": "machine", "rect": [736, 240, 428, 504],
  "floor": "serverChecker", "wall": { "color": "#23262D", "width": 4 },
  "labelStyle": { "bg": "#23262D", "fg": "#E6E3DC" } }
```

- 先画地板（`floors[floor]`）再画墙（边框画在 rect 内侧，`dashed` 为虚线，如等候凳），名牌画在左上角 `labels.room.offset` 处。
- `kind` 只用于语义（例如“点击休息区显示空闲的人”），渲染上一视同仁。
- `doors`：墙上的开口 `{"side": "n|s|e|w", "offset": <门中心到房间左边/上边的距离>, "width": 24}`，画墙时在这里留缺口（`draw: false` 则只用于校验不留缺口）。人只能经门进出房间：路网的边、锚点到接入节点的连线穿过某房间的墙时，必须落在一个门的宽度内（§6.3 校验）。示例里每个房间一到两个门，房间之间是 16 px 宽的走廊（`corridorNE`、`corridorJ`、`hallBottom`…），没有人会穿墙或穿过别的讨论串工作组。
- 家具 `{"id", "sprite", "at", "room", "z"}`；`z` 缺省为 0，同 z 按 y 排序。家具精灵里的 `@slot` 颜色可以在家具上用 `slots` 固定。

### 4.2 精灵

- `shapes`：一组矩形/椭圆，带填充、描边、圆角，坐标相对精灵左上角。设计稿里所有家具都是这种（桌子 = 桌面 + 显示器 + 屏幕 + 椅子）。
- `pixels`：字符网格 + `colors` 映射（`.` 透明），按 `pixel` 放大。
- `image`：`/office/assets/…` 下的 PNG（或 https 地址），`frames` > 1 时为横向图集，按 `fps` 播放。素材目录和布局文件一起放在 `/office`（部署时挂到 `/srv/office`，由控制台以 `/office/assets/*` 静态提供）。
- `slots`：精灵里 `@slot` 颜色的默认值。桌子的 `@screen` 由坐在那里的员工状态决定（见 4.8 的 `screen`）。

### 4.3 小部件（数据绑定）

| type | 绑定的数据 | 字段 |
| --- | --- | --- |
| `taskBoard` | 任务池可领数量（聚焦时只算该讨论串）；项目队列 `board.queue` | `sprite`、`text.value` 模板变量 `{claimable}`；可选 `cards`：把队列里待领的任务画成卡片（`at` 板内位置、`size`、`gap`、`columns`、`max`，卡片用讨论串颜色，等依赖的带锁，返工预留的写“→dev-1”，放不下时最后一张变成 `+N`；`text.value` 变量 `{taskId} {threadNo} {role} {reservedFor}`，默认 `{taskId}`）；点击打开任务池 |
| `nodeRacks` | 该项目用到的节点：名字、在线、`max_active_sessions`、活跃/停放/排队会话数 | 按 `grid`（列数、步长、最多几台）重复 `sprite`；每台按 `slot` 重复槽位灯（最多 `slot.max` 个，超过的合并到最后一盏），每盏灯按状态取 `slotColors`（active/parked/queued/empty/offline，可指定哪颗灯闪）；`label` 模板变量 `{name} {active} {capacity}`；超过 `maxItems` 时显示 `overflowLabel`（`{rest}`） |
| `pipelinePanel` | 合并队列条目、最近一次 CI 的 checks | `sections`：`mergeQueue`（`row` 模板 `{threadNo} {employee} {taskId}`，状态文案与颜色）、`ci`（每个 check 一盏灯，按结论取色，`blink` 列表里的状态闪烁） |

小部件的类型是固定的一小组（新增类型需要改渲染器代码）；位置、外观、文案、颜色全部在 JSON 里。

### 4.4 讨论串工作台（threadPods）

共享成员以后（`shared-members.md` §9），每个进行中的讨论串一张工作台，**只有组长的座位是固定的**；成员坐自己的工位（§4.5），工作台上用讨论串颜色的小头像列出正在给它干活的人。

```
threadPods
├─ area          工作台可以放的区域（开发区内）
├─ templates     一个或多个模板：size、frame（虚线框，聚焦时变成讨论串颜色）、plate（门牌）、
│                desks（桌子精灵、pod 内位置、座位、是否给组长、计时器位置）、overflowSeats、
│                participants（参与者头像条：at、size、step、max，超出显示 +N）、
│                visit（访客站位，place `pod`；缺省为第一个出口节点）、
│                nav（pod 内路网 + 座位接到哪个过道节点 + 出口接到哪个全局节点）
├─ layout        mode=grid、gap、columns、use（按可见讨论串数挑模板）、overflow、order、show
├─ seating       组长坐哪张桌、成员入座顺序、桌子属于哪一排
├─ threadColors  讨论串颜色轮换
└─ fadeMs        讨论串结束后工作台淡出的时长（默认 3000；组长走到门口比这久时，工作台等组长出门再撤）
```

**实例化算法**（前端 `office/compile.ts`，纯函数，输入布局 + 讨论串列表，输出场景）：

1. 可见讨论串 = 状态在 `layout.show` 里的讨论串；按 `layout.order`（`no` 升序最稳定，不会因状态变化跳位置）；`pinFocused` 时被聚焦的放第一个。
2. 挑模板：`layout.use` 里第一个 `maxThreads ≥ 可见数` 的条目；都不满足就用最后一个，并按 `overflow` 处理：`paginate` = 每页放满后分页（工作区右上角出现“1/3 ‹ ›”），`scale` = 模板整体缩小直到放下（不低于 0.5）。
3. 排布：在 `area.rect` 里按网格放 pod：列数 = `columns`（`auto` = 能放下几列放几列），间距 `gap`；从左到右、从上到下。
4. 入座：组长坐 `seating.leadDesk`（或 `for: "lead"` 的桌子）。有 `staffArea` 时成员不坐工作台（`members.visitPods: true` 时例外：正在给这个讨论串干活的成员坐访客座）；**没有 `staffArea` 的旧布局**照旧：讨论串的成员按 `seating.order`（`roleSort` = 岗位模板里的顺序，`instance` = 实例编号）依次坐剩下的桌子；桌子不够时站到 `overflowSeats`；还不够就叠在最后一个 overflow 点旁边（错开 6 px）。
5. 路网：pod 的局部节点加前缀 `pod<no>.`、坐标加 pod 位置，并入全局路网；每个座位接到 `seatLinks[rowOf[deskId]]` 指定的过道节点；`exits` 把 pod 出口连到全局节点。

**分页时其他页的人**（`layout.offPage`）：`hide` = 不画，只在翻页器上显示人数；`anchorsWithBadge`（默认）= 不画在座位上，但去了共享锚点（休息区、等候凳、决策室、机房、任务板）的照样画，名牌旁加一个讨论串颜色的 `#<no>` 小徽章；其他页有人举手时翻页器上亮一个橙点。统筹不属于任何 pod，始终画出。

**参与者**：成员（不含组长）里当前状态带 `task_id`、且所在讨论串（`thread_no`）是这张工作台的，按名字排在 `participants` 头像条上（讨论串颜色的小方块 + 头部像素）。

**讨论串结束**：网页发现一个讨论串从可见列表里消失（结束、归档、删除），把它和它的组长再保留 `fadeMs`、并且直到组长的 `threadEnded` 序列把他藏起来或播完（最多再等 30 秒）：工作台在动态层按时间淡出，组长播 `threadEnded` 序列（§4.9）。淡出期间讨论串又回来（恢复、重新打开）：组长停下序列走回座位；再结束时重新计时。新出现的讨论串播 `threadOpened`（统筹走过去“放一张新工作台”）。翻页、聚焦、热更新都不算新出现。

示例里：一排最多 4 张 164 × 132 的工作台（组长小桌 + 放参与者头像的工作台），更多则分页。

### 4.5 成员工位区（staffArea）

```
staffArea
├─ room, rect    工位区的范围
├─ desk          一个工位：sprite（`@screen` 跟状态规则，`@thread` 取成员正在干活的讨论串颜色）、
│                at / seat（相对单元格）、timer、tag（`#18 · t5` 小牌的中上点，相对桌子）
├─ cell          一个工位单元的 [宽, 高]（一排高一个单元）
├─ perRow        每排几张；auto = 宽度放得下几张
├─ group         role（默认）：每个岗位另起一排；放不下时自动退成 none（按岗位顺序连着排）
├─ order         排序：roleSort（岗位模板顺序）、instance、name
├─ overflow      连着排也放不下：scale（整体缩小，不低于 0.5）| stack（多出来的人站在最后一张桌旁，错开 6 px）
└─ nav           entry（工位区接入的全局节点）、spineX（纵向走道的 x；每排在座位线上有一个节点 `staff.row<n>`）
```

- 每个项目成员（`tier = member`，即编制）一张**固定工位**：先按 `order` 排序再按上面的规则分排，所以成员换讨论串、空闲、离开都不挪桌子；管理岗（统筹、组长）不占工位。
- `states.rules` 里 `place: "seat"` 对成员就是自己的工位，对组长是工作台的座位。
- 成员有任务时，工位上显示讨论串颜色的 `#18 · t5`（`desk.tag`），显示器边框是讨论串颜色（`@thread`）；不在工位上（去机房、决策室、任务板）时名牌旁带讨论串颜色的 `#18`。
- 路网：`staff.row0` 接 `nav.entry`，每排接上一排；座位到本排节点视为直线可达。

示例：`rect = [40, 212, 672, 352]`，单元 84 × 88，每排 8 张、最多 4 排（开发、测试、审查……各一排），`entry = devWest`、`spineX = 28`。

### 4.6 锚点（anchors）

命名的位置组，供状态规则用 `place: "anchor:<名字>"` 引用：

```json
"bench": { "points": [[510,488],[550,488],[590,488],[630,488],[670,488]], "pose": "sit", "nav": "benchDoor", "overflow": "standBehind" }
```

- 多人去同一个锚点组时按先来先占分配空位，离开即释放；满了按 `overflow`：`jitter`（在最后几个点附近随机错开）、`standBehind`（往下一排站，y + 24）、`stack`（同一点错开 6 px）。
- `nav`：这个锚点组接入全局路网的节点；每个点到该节点视为直线可达。

### 4.7 人物与形象

- `characters.person`：`parts` 是字符网格片段（每行宽度 = `width`），`frames` 把片段从上到下拼成一帧，`animations` 是帧序列 + 帧率（`bob` = 上下浮动像素，用于“读代码”“思考”的轻微晃动），`overlays` 是叠加的像素点（`raiseHand` 举手、`smoke` 出错冒烟），可按 `blinkFps` 闪烁。示例的像素图直接取自设计稿的 BODY/LEGS 网格，另加了坐姿和打字两帧。
- 颜色键：`h` 头发、`s` 皮肤、`e` 眼睛、`S` 眼镜位（默认同皮肤）、`c` 上衣、`a` 上衣点缀、`p` 裤子、`k` 鞋。它们映射到形象槽位 `@hair @skin @shirt @accent @pants`。
- **形象解析**（与 Team 页预览、办公室共用 `office/looks.ts`）：
  1. 取员工的 `look`（岗位模板 `at_roles.look`，见 `data-model.md`）；缺的字段按 `looks.defaults[role]` → `looks.defaults[tier]` → `looks.defaults["*"]` 补齐。
  2. `outfit`（tee / hoodie / suit / coat）给出 `shirt`、`accent`、`pants` 的取值（可引用 `@color`、`shade()`），还可以用 `patch` 改某几行像素（卫衣的帽兜）。
  3. `accessory`（none / glasses / headset / cap）可改颜色键（眼镜把 `S` 变黑）、改行（帽子）或叠加像素点（耳机）。
  4. 同岗位的多个实例衣着一致；`variation.hair = byInstance` 时发色按实例编号在 `looks.hair` 里轮换（look 里显式写了 `hair` 则固定），`shirtShade` 让第 2、3 个实例的上衣略深/略浅，便于区分；名牌底色 `@tag` = `@color`。
  5. Team 页的色板和“随机一个”使用 `looks.colors`、`looks.outfits`、`looks.accessories` 的键和 `label`。所以新增一种服装 = 在 JSON 里加一个 outfit，Team 页自动出现这个选项（后端只校验 outfit/accessory 的值在当前全局布局里存在）。
- 渲染：形象 + 帧组合的结果缓存成 `ImageBitmap`（键 = 形象 hash + 帧名），每人每帧只是一次 `drawImage`。

### 4.8 路网（nav）与走路

- 全局节点 + 无向边；pod 的局部路网和锚点在编译时并入。边是直线段，所以要绕墙就加中间节点（走廊节点、门内外各一个节点），不要指望寻路自己绕开障碍；家具不是障碍，墙是（只能从门过）。
- 员工从当前位置走到目标：当前位置 → 最近的接入节点 → A*（边权 = 欧氏距离）→ 目标的接入节点 → 目标点。沿折线以 `walkSpeed` px/s 匀速移动，播放 `states.walkAnim` 动画，按移动方向水平翻转精灵。
- 同时走路的人数上限 `maxConcurrentWalks`，超过的直接瞬移（避免几十人同时状态变化时满屏乱跑）；页面在后台标签页时一律瞬移。
- 回放时同样走路，倍速播放时步行速度同比例加快。

### 4.9 状态规则（states）

员工状态由控制台的状态机产生（`architecture.md` §11.3，事件 `status`），取值：`typing reading running thinking walking_to_board claiming returning raising_hand idle waiting queued offline broken`，另有 `substate`（running 的 test / git_commit / git_push / ci / install）、`bubble`、`bubble_kind`、`task_id`、`task_kind`（规则里写作 `taskKind`）、`tier`、`role`。这些字段都在每条状态事件和 `EmployeeStatusView` 里（`at_status_events.task_kind/tier/role`），实时和回放都不需要回查任务表。

`states.rules` 按顺序匹配，**第一条成立的规则生效**；最后一条应是兜底的 `{"match": {}}`。

| 字段 | 含义 |
| --- | --- |
| `match.state` / `substate` / `tier` / `role` / `taskKind` | 都写了就都要满足；不写表示不限 |
| `place` | `seat`（自己的座位：成员 = 工位区的固定工位，组长 = 工作台座位；统筹没有座位，规则里给它指定锚点）、`current`（原地不动，例如举手；换场景时按它原本要去的地方重新算）、`pod`（所在讨论串工作台的访客位）、`anchor:<名字>` |
| `anim` | 人物动画名；`keep` = 保持当前动画 |
| `overlay` | 叠加层（举手、冒烟） |
| `screen` | 设置所坐桌子的 `@screen` 颜色（打字绿、读代码紫、跑命令橙…，同设计稿） |
| `timer` | 在桌上显示本任务已用时间 |
| `bubble` / `bubbleKind` | 平台和 `set_status` 都没给气泡文字时的默认文字；气泡样式 |
| `opacity` / `grayscale` | 离线时变淡变灰 |

**优先级**：位置和动作只由状态规则决定；气泡文字优先级 `set_status`（未过期）> 平台生成（“跑 pytest”“请求权限：pnpm add”）> 规则里的默认 `bubble`。`bubble_kind` 由控制台给（审批/提问 = `ask`，出错 = `error`），规则里的 `bubbleKind` 只在控制台没给时生效。

**动画序列**（`states.sequences`）：一次状态变化触发一段固定的小剧情。示例 `claim`：收到 `claiming` 且 `cause` 以 `task.claimed` 开头时，人物走到 `anchor:boardClaim` → 停 1.5 s（气泡“领取 t8 计划与子任务展示”，`{taskId}` `{taskTitle}` 由前端从任务缓存填入）→ 走回座位；序列结束后再应用该员工**当前最新**的状态（序列播放期间到达的状态先排队，只保留最后一个）。

场景事件也能触发序列：`trigger.event = "threadOpened"`（新工作台出现，默认由统筹播放）或 `"threadEnded"`（讨论串结束，默认由它的组长播放），`trigger.tier` 可改播放者。步骤里 `goto: "pod"` 走到那张工作台的访客位，`hide: true` 之后这个人不再画出：`threadEnded` 序列里是组长走出办公室，直到他的工作台回来（讨论串恢复）才再画出；状态触发的序列里只藏到这段序列结束（被新状态打断、回放跳转也会让他重新出现），不会把人永久藏掉，气泡模板多了 `{threadNo} {threadTitle}`。示例：

- `openThread`：统筹走到新工作台（“给 #15 放一张工作台”）→ 停 1.5 s（“#15 落地页改版 开工”）→ 序列结束后按规则回办公室。
- `leaveThread`：组长起身（“#12 收工”）→ 走到 `anchor:exit`（开发区东北角的走廊口）→ 消失；工作台在 `fadeMs` 内淡出。

示例规则覆盖的场景（对应设计稿）：

- 统筹总在自己办公室的桌后；离线变灰、有问题时举手。
- 举手（审批 / 提问）：原地举手，气泡橙色（`dev-2`“请求权限：pnpm add”、`lead-13`“问你：key 还是登录？”）。
- 成员有任务时坐自己的工位（开发一排、测试一排……），工位上 `#18 · t5`；讨论串工作台上是组长和正在给它干活的人的小头像。
- 正在做决策类任务的人去决策室（`architect`“决策：卡片布局 A / B”）。
- 看 CI 的人去机房看屏幕。
- 打字 / 读代码 / 跑命令 / 思考：坐在工位，屏幕变色，桌上计时器。
- 领任务：走到任务板 → 领取 → 回工位（`dev-3`）。
- 组长没事时也坐在自己桌后；成员等依赖时去等候凳（`qa-2`“等任务放出”），空闲去休息区（`review-1`“空闲”）。

## 5. 渲染器如何使用布局

前端目录 `web/src/office/`：

```
load.ts       拉布局（ETag 缓存）→ JSON Schema 校验（ajv，与后端同一份 Schema）→ 解析 extends / palette / shade()
compile.ts    纯函数：布局 + 可见讨论串 + 员工 → Scene（房间、家具、小部件实例、pod 实例、工位、座位表、合并后的路网）
staff.ts      工位区：按岗位分排、溢出（scale / stack）、每排走道节点
leaving.ts    刚结束的讨论串和它的组长保留到淡出结束、组长出门（每个讨论串一个计时器，恢复即撤）
looks.ts      形象解析与像素图合成（ImageBitmap 缓存），Team 页预览也用它
nav.ts        A* 与路径插值
director.ts   状态 → 规则匹配 → 目标位置/动画/屏幕色/气泡；序列播放；锚点占位分配
render.ts     Canvas 2D：静态层（地板、墙、家具）离屏绘制一次；动态层每帧画桌面屏幕、灯、人物
overlay.tsx   DOM 叠加层（Vue JSX）：房间名、门牌、名牌、气泡、计时器、工位上的 `#18 · t5`、任务板卡片、可点区域（人物、门牌、任务板、机柜）
Office.tsx    渲染组件（Vue JSX）；数据在 pages/Office.vue：GET /api/projects/{pid}/office 首屏快照 + 项目 SSE（topics=status,thread,task,merge,ci,node,layout）
replay.ts     回放：GET …/office/replay 的快照 + 事件，按时间轴驱动 director（1× / 4× / 16×、拖动进度条）
```

- 两层画布：静态层只在布局或可见讨论串变化时重画；动态层用 `requestAnimationFrame`，只在有动画时运行（全员静止时停帧，省电）。
- 文字一律走 DOM 叠加层（中文清晰、可选中、读屏可读）；叠加层的坐标与画布同一套变换。
- 交互：点人物 → 弹出员工卡片（`GET /api/employees/{id}`：当前任务、最近动态、会话状态）；点门牌 → 聚焦该讨论串（其他内容透明度 `focus.dimOpacity`）；点任务板 / 机柜 / 合并队列 → 打开对应面板。
- 可访问性：叠加层给每个人物一个按钮（`aria-label = "dev-2，举手：请求权限 pnpm add"`）；“减少动态效果”系统设置开启时不走路、不闪烁。
- 手机：场景按宽度缩放到 0.5 倍以下时改为列表视图（统筹；每个讨论串的组长和给它干活的成员；空闲成员），不画场景。
- 顶栏（办公室页）：`编制 N 人 · 干活 x · 空闲 y · 讨论串 z 个进行中`。编制 = 项目成员数（统筹和组长是管理岗，不算），干活 = 状态带任务的成员，讨论串 = 运行 / 等待 / 待验收 / 暂停中的讨论串（排队的不算）。

## 6. 热更新

用户的要求是“只改 JSON 文件就能重画办公室”。两种来源都支持热更新：

### 6.1 文件（默认）

- 布局文件放在仓库 `/office/*.json`，部署时挂进控制台容器 `/srv/office`（`OFFICE_LAYOUT_DIR`）。文件名去掉 `.json` 就是布局 id（`default.json` → `default`）。
- 控制台用 fsnotify 监听目录（去抖 300 ms）；文件变化 → 读取 → **Schema 校验 + 语义校验**（见 6.3）→ 通过则替换内存里的版本（`version` = 内容 sha256 前 12 位），并向所有正在使用该布局的项目 SSE 频道发 `event: layout {layout_id, version}`；失败则保留旧版本，错误写日志并显示在 Admin → 办公室布局。
- 前端收到 `layout` 事件 → 重新 `GET /api/office/layouts/{id}`（带 `If-None-Match`）→ 重新编译场景 → 下一帧生效。人物保持当前状态，位置按新布局重新计算后**瞬移**过去（不走路），气泡保留。整个过程不刷新页面，目标 2 秒内完成。
- 开发时：`web` 的 Vite 开发服务器把 `/office/*.json` 作为模块导入，Vite HMR 推送变更，效果同上，不需要后端。

### 6.2 数据库（可选，管理员在页面上改）

- Admin → 办公室布局：JSON 编辑器（CodeMirror 6 + 用同一份 Schema 做补全和实时报错）+ 右侧实时预览（用编辑中的 JSON 直接编译渲染，不落库）。
- 保存 = `PUT /api/admin/office/layouts/{id}`，校验通过存 `at_office_layouts` 并广播 `layout` 事件。数据库里的同 id 布局覆盖文件布局；删除数据库布局即回到文件版本。
- 项目设置可以选择用哪个布局（`at_projects.office_layout`），默认用 `at_settings.office_default`，再缺省用 `default`。

### 6.3 校验

后端（Go，`santhosh-tekuri/jsonschema/v6`）和前端（ajv）用同一份 Schema。Schema 之外的语义检查（Go `internal/office/validate.go`，前端编辑器也跑一遍同样的 TS 实现）：

- 所有引用都存在：`floor`、`sprite`、家具/小部件的 `room`、模板的桌子精灵、`anchors.*.nav` 和 pod `exits` 指向的全局节点、`staffArea` 的 `room` / `desk.sprite` / `nav.entry`、规则里的 `anchor:<名字>`、`anim`、`overlay`、序列里的 `goto`。
- 像素网格每行宽度等于 `characters.person.width`；outfit/accessory 的 `patch` 行宽一致。
- 路网连通：所有锚点和 pod 出口在同一个连通分量里（否则有人走不到）。
- 不穿墙：对每条全局边、每个 pod 模板按“最多可见讨论串数”实例化后的每条 pod 内边和出口边、工位区坐满时每排的走道边、每个锚点点位到它 `nav` 节点的连线，求与各房间墙（rect 四条边）的交点；交点必须落在该边某个门的 `[offset − width/2, offset + width/2]` 区间内。没有 `doors` 的房间跳过并给警告。报错时给出边的两端和穿过的房间。
- `doors` 的 `offset` 在墙长范围内，门不重叠。
- 房间、pod 区域、工位区、家具不超出场景；`threadPods.area` 至少放得下一个最小模板。
- 向后兼容：`staffArea`、`members`、`participants`、`visit`、`fadeMs`、`cards`、序列的 `event` / `hide` 都是可选的；没有 `staffArea` 的旧布局照常通过校验、照旧把成员摆进 pod（Go 端只做 Schema + 结构校验，同样接受）。
- `states.rules` 最后一条是兜底；每个状态名至少被一条规则覆盖（兜底算）。
- `looks.outfits` 包含 `tee hoodie suit coat`、`looks.accessories` 包含 `none glasses headset cap`（岗位模板里已有的值必须仍然有效）；如果新布局去掉了某个正在使用的服装，校验报错并列出受影响的岗位。
- 文件大小 ≤ 512 KB；`furniture` ≤ 1000 件。

### 6.4 版本

- `format` 是格式版本（当前 1）。渲染器遇到不认识的 `format` 时显示“布局格式需要更新的前端”，并回退到内置的最小布局。
- 新增可选字段不升 `format`；Schema 里所有对象都 `additionalProperties: false`，所以拼错字段名会在保存/加载时立刻报错（热更新失败、旧版本继续生效），不会静默忽略。
- 自由备注写在顶层 `meta` 里。

## 7. 示例对照设计稿

| 设计稿元素 | `office.example.json` |
| --- | --- |
| 开发区（木地板，`#8A6A45` 墙） | `rooms[id=dev]`、`floors.devPlanks` |
| #12 / #13 两个虚线工作组、门牌“#12 员工通信与审批 · 7/13” | `threadPods.templates.full`（`plate.value = "#{no} {title} · {mainDone}/{mainTotal}"`），两个讨论串时用 full 模板 |
| 每组 4 张桌（显示器屏幕变色、计时器） | `templates.full.desks` d1–d4，`sprites.desk` 的 `@screen`，规则里的 `screen`/`timer` |
| 任务板“任务板 可领 3” | `widgets[id=board]` |
| 决策室（会议桌） | `rooms[id=decision]`、`furniture[id=decisionTable]`、`anchors.decisionSeats` |
| 项目统筹办公室（桌子、盆栽） | `rooms[id=coordinator]`、`coordDesk`、`coordPlant`、`anchors.coordinatorDesk` |
| 休息区（沙发、茶几、盆栽） | `rooms[id=lounge]`、`sofa`、`coffee`、`loungePlant`、`anchors.lounge` |
| 等候凳“等候凳 · 等任务放出”（虚线框） | `rooms[id=bench]`（`wall.style = dashed`）、`waitBench`、`anchors.bench` |
| 机房（深色棋盘地板）、机柜 rc-erio 3/4、cvp 4/4 及槽位灯 | `rooms[id=machine]`、`floors.serverChecker`、`widgets[id=racks]` |
| 合并队列 + CI（verify / integration 灯） | `widgets[id=pipeline]` |
| 人物像素图、举手、走路 | `characters.person`（BODY/LEGS 网格来自设计稿）、`overlays.raiseHand`、`animations.walk` |
| 岗位形象（统筹紫色卫衣耳机、组长橙色西装、开发蓝色 T 恤、测试白大褂眼镜、审查深色西装眼镜） | `looks.defaults` |
| 气泡（普通白底、提问橙底） | `bubbles.kinds.plain`、`bubbles.kinds.ask` |
| 聚焦单个讨论串时其他变淡 | `focus.dimOpacity = 0.3` |

# 平台通用组件

页面只用这里和 `../ui` 的组件拼装，不直接写卡片、徽章、弹窗的样式。预览：本地 `npm run dev` 后打开 `/ui`（只在开发模式里有，线上构建不包含）。

| 组件 | 用途 |
| --- | --- |
| `Page` / `PageHeader` / `SplitLayout` | 页面骨架：居中容器、标题说明和右侧操作；主列 + 侧栏 |
| `Panel` | 所有块的底板；可带标题、图标、说明、右侧操作，`tone` 高亮或危险 |
| `Field` / `FieldGroup` / `Disclosure` | 表单字段（自动绑定 label）、字段分组、折叠的高级设置 |
| `FormDialog` / `InfoDialog` | 表单弹窗（提交按钮带进度）、只读信息弹窗 |
| `ConfirmAction` / `DangerZone` | 不可撤销操作前的确认；危险区一行 |
| `StatusBadge` / `Chip` / `TASK_STATUS` / `PLAN_STATUS` / `MEMBER_TYPES` / `nodeStatus` | 状态徽章和小标签；业务状态到颜色、文字的映射（任务、计划步骤、成员类型）集中在 `status.ts` |
| `StatCard` / `StatGrid` / `KeyValueList` | 统计数字、键值列表 |
| `DataTable` / `PickList` | 可横向滚动的表格；可单选的滚动列表 |
| `Callout` / `EmptyState` / `Loading` / `Spinner` | 提示条、空状态、加载中 |
| `CopyBlock` | 需要复制的命令或令牌 |

接口调用用 `lib/mutations.ts` 的 `useAction`：出错弹提示、成功后刷新指定查询并可显示成功消息。

员工提问和权限审批的卡片在 `../QuestionCard.tsx`（由 `Panel`、`StatusBadge`、`CopyBlock` 拼成）；相关的纯逻辑（子任务排序、时间线、审批答案）在 `lib/comms.ts`，团队表单的成员类型切换在 `lib/team.ts`，测试用 `cd web && npm test`（Node 自带测试，无需额外依赖）。

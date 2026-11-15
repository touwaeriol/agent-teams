# 平台通用组件

页面只用这里和 `../ui` 的组件拼装，不直接写卡片、徽章、弹窗的样式。v2 页面专用的积木（状态药丸、勾选标记、输入框、等你处理的卡片、讨论串列表）在 `../app`。

| 组件 | 用途 |
| --- | --- |
| `Page` / `PageHeader` / `SplitLayout` | 页面骨架：居中容器、标题说明和右侧操作；主列 + 侧栏 |
| `Panel` | 所有块的底板；可带标题、图标、说明、右侧操作，`tone` 高亮或危险 |
| `Field` / `FieldGroup` / `Disclosure` | 表单字段（自动绑定 label）、字段分组、折叠的高级设置 |
| `FormDialog` / `InfoDialog` | 表单弹窗（提交按钮带进度）、只读信息弹窗 |
| `ConfirmAction` / `DangerZone` | 不可撤销操作前的确认；危险区一行 |
| `StatusBadge` / `Chip` | 状态徽章和小标签（v2 的业务状态颜色见 `lib/present.ts`） |
| `StatCard` / `StatGrid` / `KeyValueList` | 统计数字、键值列表 |
| `DataTable` / `PickList` | 可横向滚动的表格；可单选的滚动列表 |
| `Callout` / `EmptyState` / `Loading` / `Spinner` | 提示条、空状态、加载中 |
| `CopyBlock` | 需要复制的命令或令牌 |

接口调用用 `api/mutations.ts` 的 `useAction`：出错弹提示、成功后刷新指定查询并可显示成功消息。

员工提问、权限审批和 PR 验收的卡片在 `../app/cards.tsx`；状态到颜色和文字的映射在 `lib/present.ts`。测试：`cd web && npm test`（vitest）。

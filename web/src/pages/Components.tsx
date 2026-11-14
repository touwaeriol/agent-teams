import { Activity, Github, Plus, TriangleAlert } from "lucide-react";
import { useState } from "react";
import {
  Callout,
  Chip,
  ConfirmAction,
  CopyBlock,
  DangerZone,
  DataTable,
  Disclosure,
  EmptyState,
  Field,
  FieldGroup,
  FormDialog,
  KeyValueList,
  Page,
  Panel,
  PickList,
  Spinner,
  StatCard,
  StatGrid,
  StatusBadge,
  TASK_STATUS,
} from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { TagInput } from "@/components/ui/tag-input";
import { Textarea } from "@/components/ui/textarea";

/** Living catalogue of the platform's components, for design review. */
export function ComponentsPage() {
  const [dialog, setDialog] = useState(false);
  const [tags, setTags] = useState(["Bash(git *)", "Bash(pnpm *)"]);
  const [picked, setPicked] = useState("octo/app");
  return (
    <Page title="组件库" description="平台页面都由这些通用组件拼成：components/common（业务通用块）和 components/ui（基础控件，shadcn/ui + Radix）。改样式改这里，所有页面一起变。">
      <Section name="按钮 Button">
        <div className="flex flex-wrap items-center gap-2">
          <Button>主要操作</Button>
          <Button variant="outline">次要操作</Button>
          <Button variant="secondary">弱操作</Button>
          <Button variant="ghost">幽灵</Button>
          <Button variant="destructive">危险操作</Button>
          <Button variant="link">链接</Button>
          <Button size="sm">小号</Button>
          <Button disabled>
            <Spinner /> 处理中
          </Button>
        </div>
      </Section>

      <Section name="状态 StatusBadge · Chip">
        <div className="flex flex-wrap items-center gap-2">
          {Object.entries(TASK_STATUS).map(([k, s]) => (
            <StatusBadge key={k} tone={s.tone} label={s.label} spinning={k === "running"} />
          ))}
          <StatusBadge tone="ok" dot label="在线" />
          <StatusBadge tone="bad" dot label="离线" />
          <StatusBadge tone="primary" label="负责人" />
          <Chip tone="ok">lead @rc-erio</Chip>
          <Chip tone="bad">reviewer @cvp</Chip>
        </div>
      </Section>

      <Section name="统计 StatCard · 键值 KeyValueList">
        <StatGrid>
          <StatCard label="节点" value="2（在线 2）" />
          <StatCard label="运行中的会话 / 总槽位" value="1 / 8" />
          <StatCard label="Claude Code 版本" value="2.1.322" />
        </StatGrid>
        <Panel padding="sm" title="GitHub · octocat" icon={<Github />} action={<StatusBadge tone="ok" label="正常" />}>
          <KeyValueList items={[{ label: "GitHub 账号", value: "octocat" }, { label: "添加时间", value: "3 分钟前" }]} />
        </Panel>
      </Section>

      <Section name="面板 Panel · 提示 Callout · 空状态 EmptyState">
        <div className="grid gap-4 lg:grid-cols-2">
          <Panel title="任务 · 1 / 2" icon={<Activity />} action={<Button size="sm" variant="ghost">查看全部</Button>} description="面板带标题、图标、右侧操作和说明。">
            <Callout tone="info">普通提示</Callout>
            <Callout tone="warn" icon={<TriangleAlert />}>警告：节点离线时任务会排队</Callout>
            <Callout tone="bad">错误：GitHub 拒绝了这个令牌</Callout>
          </Panel>
          <EmptyState icon={<Github />} title="还没有 Git 连接" description="空状态说明下一步做什么。" action={<Button>添加连接</Button>} />
        </div>
      </Section>

      <Section name="表单 Field · FieldGroup · Disclosure · TagInput · PickList">
        <div className="grid gap-4 lg:grid-cols-2">
          <Panel>
            <Field label="项目名称" hint="字段下方的说明文字。">
              <Input placeholder="例如：官网改版" />
            </Field>
            <Field label="项目上下文">
              <Textarea rows={3} placeholder="支持 Markdown" />
            </Field>
            <Field label="权限模式">
              <Select defaultValue="acceptEdits">
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="acceptEdits">自动接受改文件</SelectItem>
                  <SelectItem value="plan">只读规划</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field label="允许的工具">
              <TagInput values={tags} onChange={setTags} placeholder="回车添加" />
            </Field>
            <label className="flex items-center gap-3">
              <Switch defaultChecked /> 开关
            </label>
          </Panel>
          <Panel>
            <FieldGroup legend="代码仓库（GitHub）">
              <PickList
                items={["octo/app", "octo/site", "octo/docs"]}
                itemKey={(r) => r}
                selected={picked}
                onPick={setPicked}
                render={(r) => <span className="font-mono text-[13px]">{r}</span>}
              />
            </FieldGroup>
            <Disclosure summary="权限与高级设置（折叠区）">
              <Field label="单个任务最多轮数">
                <Input type="number" placeholder="不限" />
              </Field>
            </Disclosure>
            <CopyBlock text="curl -fsSL https://agent.prophey.ai/install-node.sh | AGENT_TEAMS_NODE_TOKEN=… bash -s -- --console https://agent.prophey.ai --name build-1" />
          </Panel>
        </div>
      </Section>

      <Section name="表格 DataTable · 选项卡 Tabs">
        <Tabs defaultValue="a">
          <TabsList>
            <TabsTrigger value="a">表单</TabsTrigger>
            <TabsTrigger value="b">YAML</TabsTrigger>
          </TabsList>
          <TabsContent value="a">
            <DataTable
              rows={[
                { id: "1", name: "rc-erio", ver: "2.1.322", slots: "1 / 4" },
                { id: "2", name: "cvp", ver: "2.1.322", slots: "0 / 4" },
              ]}
              rowKey={(r) => r.id}
              columns={[
                { header: "节点", cell: (r) => <span className="font-mono">{r.name}</span> },
                { header: "版本", cell: (r) => r.ver },
                { header: "槽位", cell: (r) => r.slots },
              ]}
            />
          </TabsContent>
          <TabsContent value="b">
            <Textarea rows={4} className="font-mono text-xs" defaultValue={"leader: lead\nemployees: []"} />
          </TabsContent>
        </Tabs>
      </Section>

      <Section name="弹窗 FormDialog · 确认 ConfirmAction · DangerZone">
        <div className="flex flex-wrap items-center gap-2">
          <Button onClick={() => setDialog(true)}>
            <Plus /> 打开表单弹窗
          </Button>
          <ConfirmAction title="删除这个连接？" description="确认框写清后果。" onConfirm={() => undefined} />
          <ConfirmAction iconOnly="删除" title="只有图标的删除按钮" onConfirm={() => undefined} />
        </div>
        <DangerZone title="删除项目" description="危险区：说明后果，右侧放需要确认的操作。">
          <ConfirmAction title="删除项目？" onConfirm={() => undefined} />
        </DangerZone>
        <FormDialog open={dialog} onOpenChange={setDialog} title="添加节点" description="表单弹窗：标题、说明、字段、一个提交按钮。" submitLabel="生成安装命令" onSubmit={() => setDialog(false)}>
          <Field label="节点名">
            <Input autoFocus className="font-mono" />
          </Field>
        </FormDialog>
      </Section>
    </Page>
  );
}

function Section({ name, children }: { name: string; children: React.ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <h2 className="font-mono text-xs uppercase tracking-wider text-muted-foreground">{name}</h2>
      {children}
    </section>
  );
}

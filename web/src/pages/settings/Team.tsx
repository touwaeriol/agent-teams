// Team board: role templates (coordinator → lead → members) with the look
// editor (palette / outfit / accessory / random), skip-permissions, details
// dialog and a YAML view.
import { Check, ChevronDown, MoreHorizontal } from "lucide-react";
import { useEffect, useState } from "react";
import { roles as roleActions } from "@/api/actions";
import { errorText, useAction } from "@/api/mutations";
import { keys, useNodes, usePlugins, useRolePresets, useRoles, useRolesYaml, useSkills } from "@/api/queries";
import type { Look, NodeView, PluginView, RoleView } from "@/api/views";
import { Avatar, useOfficeLayout } from "@/components/app/Avatar";
import { Callout, Field, FormDialog, Loading, Spinner } from "@/components/common";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { TagInput } from "@/components/ui/tag-input";
import { Textarea } from "@/components/ui/textarea";
import { NavButton } from "@/components/shell/Shell";
import type { OfficeLayout } from "@/office";
import { SkillLibrary } from "@/components/app/SkillLibrary";
import { PluginLibrary } from "@/components/app/PluginLibrary";
import { missingRefs, pinPlugin, refFor, resolvedVersion, runtimeNote, splitLibrary, togglePlugin, type PluginRef } from "@/lib/plugins";
import { canHaveSkills, countText, fromPreset, kindText, LEVEL, newRole, previewTags, roleName, roleProblems, ruleText, skipHint, sortRoles, toggleSkill } from "@/lib/roles";
import { cn } from "@/lib/utils";

export function TeamPage() {
  const roles = useRoles();
  const [yaml, setYaml] = useState(false);
  const [draft, setDraft] = useState<RoleView[] | null>(null);
  useEffect(() => {
    if (roles.data) setDraft(sortRoles(roles.data.roles ?? []));
  }, [roles.data]);
  const save = useAction(() => roleActions.save(draft!.map((r, i) => ({ ...r, sort: i }))), { invalidate: [keys.roles], success: "岗位模板已保存" });
  const layout = useOfficeLayout();
  if (!draft) return <Loading className="h-full" />;
  const dirty = JSON.stringify(draft) !== JSON.stringify(sortRoles(roles.data?.roles ?? []));
  const problems = roleProblems(draft);
  const setRole = (i: number, patch: Partial<RoleView>) => setDraft(draft.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const add = () => setDraft([...draft, newRole(draft, layout.looks.colors[draft.length % layout.looks.colors.length] ?? "#4C7BD0")]);
  return (
    <main className="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
      <div className="mx-auto flex max-w-[1040px] flex-col gap-5">
        <header className="flex flex-wrap items-end justify-between gap-3">
          <div className="flex items-start gap-1">
            <NavButton />
            <div className="flex flex-col gap-1.5">
              <h1 className="text-2xl font-semibold">团队岗位</h1>
              <p className="max-w-[640px] text-[13px] leading-[22px] text-muted-foreground">每个项目有一个项目统筹；每开一个讨论串，就按下面的岗位拉一组组长和成员。这里是模板，具体实例在讨论串里。</p>
            </div>
          </div>
          <div className="flex gap-2">
            <Button variant="ghost" className="h-[38px]" onClick={() => setYaml(!yaml)}>
              {yaml ? "卡片视图" : "YAML"}
            </Button>
            {!yaml && (
              <>
                <PresetMenu onPick={(p) => setDraft([...draft, fromPreset(draft, p)])} />
                <Button variant="outline" className="h-[38px]" onClick={add}>
                  添加岗位
                </Button>
                <Button className="h-[38px]" disabled={!dirty || problems.length > 0 || save.isPending} onClick={() => save.mutate()}>
                  {save.isPending && <Spinner />} 保存
                </Button>
              </>
            )}
          </div>
        </header>
        <section className="flex flex-wrap items-center gap-2 rounded-xl border bg-panel px-3.5 py-3 text-[13px]">
          <span className="font-semibold">层级</span>
          <span className="rounded-full px-2.5 py-1" style={{ background: LEVEL.coordinator.bg, color: LEVEL.coordinator.fg }}>项目统筹 · 每项目 1 个</span>
          <span className="text-[#8A877F]">→</span>
          <span className="rounded-full px-2.5 py-1" style={{ background: LEVEL.lead.bg, color: LEVEL.lead.fg }}>组长 · 每讨论串 1 个</span>
          <span className="text-[#8A877F]">→</span>
          <span className="rounded-full px-2.5 py-1" style={{ background: LEVEL.member.bg, color: LEVEL.member.fg }}>成员 · 每讨论串按岗位数量</span>
        </section>
        {(roles.data?.warnings ?? []).map((w) => (
          <Callout key={`${w.role}-${w.message}`} tone="warn">
            {w.role ? `${w.role}：` : ""}
            {w.message}
          </Callout>
        ))}
        {dirty && problems.length > 0 && <Callout tone="warn">{problems.join("；")}</Callout>}
        {yaml ? (
          <YamlEditor />
        ) : (
          <div className="grid gap-3.5 lg:grid-cols-2">
            {draft.map((r, i) => (
              <RoleCard key={r.id || `new-${i}`} role={r} layout={layout} onChange={(p) => setRole(i, p)} onRemove={() => setDraft(draft.filter((_, j) => j !== i))} />
            ))}
          </div>
        )}
        {!yaml && <SkillLibrary />}
        {!yaml && <PluginLibrary />}
      </div>
    </main>
  );
}

function RoleCard({ role, layout, onChange, onRemove }: { role: RoleView; layout: OfficeLayout; onChange: (p: Partial<RoleView>) => void; onRemove: () => void }) {
  const level = LEVEL[role.tier];
  const [editing, setEditing] = useState(false);
  const skip = role.skip_permissions;
  return (
    <article className={cn("flex flex-col gap-3.5 rounded-[14px] border bg-panel p-[18px]", skip && "border-[#EBC3C3]")}>
      <div className="flex items-start justify-between gap-2.5">
        <div className="flex flex-col gap-1">
          <span className="flex items-center gap-2">
            <span className="text-base font-semibold">{roleName(role)}</span>
            <span className="rounded-full px-2 py-0.5 text-[11px]" style={{ background: level.bg, color: level.fg }}>
              {level.label}
            </span>
          </span>
          <span className="text-[13px] leading-[21px] text-[#4A4843]">{role.description || "还没有职责说明。"}</span>
        </div>
        <span className="flex items-center gap-1">
          <span className="whitespace-nowrap text-xs text-muted-foreground">{countText(role)}</span>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button type="button" aria-label={`${roleName(role)} 的菜单`} className="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-raised">
                <MoreHorizontal className="size-4" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={() => setEditing(true)}>编辑详情</DropdownMenuItem>
              {role.tier === "member" && (
                <DropdownMenuItem className="text-destructive" onSelect={onRemove}>
                  删除岗位
                </DropdownMenuItem>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        </span>
      </div>
      <div className="grid grid-cols-2 gap-2.5 text-[13px]">
        <span className="flex flex-col gap-1">
          <span className="text-xs text-muted-foreground">类型</span>
          <Select value={role.runtime} disabled={role.tier !== "member"} onValueChange={(v) => onChange({ runtime: v as RoleView["runtime"] })}>
            <SelectTrigger className="h-9 text-[13px]">
              <SelectValue>{kindText(role)}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="claude">Claude Code</SelectItem>
              <SelectItem value="codex">Codex</SelectItem>
            </SelectContent>
          </Select>
        </span>
        <span className="flex flex-col gap-1">
          <span className="text-xs text-muted-foreground">可用节点</span>
          <NodePicker value={role.node_ids ?? []} onChange={(ids) => onChange({ node_ids: ids })} />
        </span>
      </div>
      <span className="rounded-lg bg-background px-2.5 py-2 text-xs leading-[19px] text-[#4A4843]">{ruleText(role)}</span>
      <LookEditor role={role} layout={layout} onChange={(look) => onChange({ look })} />
      {canHaveSkills(role) && <SkillPicker value={role.skills ?? []} onChange={(skills) => onChange({ skills })} />}
      {canHaveSkills(role) && <PluginPicker role={role} onChange={(plugins) => onChange({ plugins })} />}
      {role.tier === "member" && (
        <label className="flex cursor-pointer items-center justify-between gap-3 border-t border-[#F1EFEA] pt-2.5">
          <span className="flex flex-col gap-0.5">
            <span className={cn("font-medium", skip && "text-destructive")}>跳过权限确认</span>
            <span className="text-xs text-muted-foreground">{skipHint(role)}</span>
          </span>
          <Switch checked={skip} onCheckedChange={(v) => onChange({ skip_permissions: v })} aria-label="跳过权限确认" className="data-[state=checked]:bg-destructive" />
        </label>
      )}
      <RoleDialog open={editing} onOpenChange={setEditing} role={role} onSave={(p) => (onChange(p), setEditing(false))} />
    </article>
  );
}

function PresetMenu({ onPick }: { onPick: (r: RoleView) => void }) {
  const presets = useRolePresets();
  const list = presets.data?.roles ?? [];
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" className="h-[38px]" disabled={list.length === 0}>
          从预设添加 <ChevronDown />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="max-w-[320px]">
        {list.map((p) => (
          <DropdownMenuItem key={p.role} onSelect={() => onPick(p)} className="flex flex-col items-start gap-0.5">
            <span className="font-medium">{roleName(p)}</span>
            <span className="text-xs text-muted-foreground">{p.description}</span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** Skills this role loads (built-in design/docs and uploaded ones). */
function SkillPicker({ value, onChange }: { value: string[]; onChange: (skills: string[]) => void }) {
  const skills = useSkills();
  const all = skills.data?.skills ?? [];
  const missing = value.filter((name) => !all.some((s) => s.name === name));
  return (
    <div className="flex flex-wrap items-center gap-1.5 border-t border-[#F1EFEA] pt-2.5 text-[13px]">
      <span className="mr-1 text-xs text-muted-foreground">技能</span>
      {all.map((s) => (
        <span key={s.name} title={s.description}>
          <Chip on={value.includes(s.name)} onClick={() => onChange(toggleSkill(value, s.name))}>
            {value.includes(s.name) && <Check className="mr-0.5 inline size-3" />}
            {s.name}
          </Chip>
        </span>
      ))}
      {missing.map((name) => (
        <Chip key={name} on onClick={() => onChange(toggleSkill(value, name))}>
          {name}（已删除）
        </Chip>
      ))}
      {all.length === 0 && <span className="text-xs text-muted-foreground">技能库是空的</span>}
    </div>
  );
}

/** Plugins this role loads: tick a plugin (latest version), then optionally pin a version. */
function PluginPicker({ role, onChange }: { role: RoleView; onChange: (plugins: string[]) => void }) {
  const plugins = usePlugins();
  const all = plugins.data?.plugins ?? [];
  const { mine, builtin } = splitLibrary(all);
  const value = role.plugins ?? [];
  const note = value.length > 0 ? runtimeNote(role) : "";
  return (
    <div className="flex flex-col gap-1.5 border-t border-[#F1EFEA] pt-2.5 text-[13px]" aria-label={`${roleName(role)} 的插件`}>
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="mr-1 text-xs text-muted-foreground">插件</span>
        {[...mine, ...builtin].map((p) => (
          <PluginChip key={p.id} plugin={p} entry={refFor(value, p.name)} onToggle={() => onChange(togglePlugin(value, p.name))} onPin={(v) => onChange(pinPlugin(value, p.name, v))} />
        ))}
        {missingRefs(value, all).map((r) => (
          <Chip key={r.name} on onClick={() => onChange(togglePlugin(value, r.name))}>
            {r.name}（已删除）
          </Chip>
        ))}
        {all.length === 0 && <span className="text-xs text-muted-foreground">插件库是空的</span>}
      </div>
      {note && <span className="text-xs leading-[18px] text-muted-foreground">{note}</span>}
    </div>
  );
}

function PluginChip({ plugin, entry, onToggle, onPin }: { plugin: PluginView; entry?: PluginRef; onToggle: () => void; onPin: (version: number) => void }) {
  const on = Boolean(entry);
  const gone = entry && resolvedVersion(plugin, entry) === undefined;
  const label = `${plugin.name}${plugin.builtin ? " · 内置" : ""}${plugin.enabled ? "" : " · 已停用"}`;
  return (
    <span className="inline-flex items-center gap-1" title={plugin.versions?.[0]?.description || undefined}>
      <Chip on={on} onClick={onToggle}>
        {on && <Check className="mr-0.5 inline size-3" />}
        {label}
      </Chip>
      {entry && (
        <Select value={String(entry.version)} onValueChange={(v) => onPin(Number(v))}>
          <SelectTrigger className={cn("h-7 w-auto gap-1 px-2 text-xs", gone && "border-destructive text-destructive")} aria-label={`${plugin.name} 的版本`}>
            <SelectValue>{entry.version ? `v${entry.version}${gone ? "（已清理）" : ""}` : `最新 v${plugin.latest}`}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="0">最新（v{plugin.latest}）</SelectItem>
            {(plugin.versions ?? []).map((v) => (
              <SelectItem key={v.version} value={String(v.version)}>
                v{v.version}
                {v.manifest_version ? ` · ${v.manifest_version}` : ""}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
    </span>
  );
}

function NodePicker({ value, onChange }: { value: string[]; onChange: (ids: string[]) => void }) {
  const nodes = useNodes();
  const list: NodeView[] = nodes.data ?? [];
  const names = list.filter((n) => value.includes(n.id)).map((n) => n.name);
  const missing = value.length - names.length;
  const text = value.length === 0 ? "任意节点" : [...names, ...(missing > 0 ? [`${missing} 个已删除`] : [])].join("、");
  const toggle = (id: string) => onChange(value.includes(id) ? value.filter((x) => x !== id) : [...value, id]);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button type="button" className="flex h-9 items-center justify-between gap-2 rounded-lg border border-input bg-well px-2.5 text-left">
          <span className="truncate">{text}</span>
          <ChevronDown className="size-4 shrink-0 text-muted-foreground" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        {list.length === 0 && <div className="px-2 py-1.5 text-xs text-muted-foreground">还没有节点</div>}
        {list.map((n) => (
          <DropdownMenuItem key={n.id} onSelect={(e) => (e.preventDefault(), toggle(n.id))}>
            <span className="flex size-4 items-center justify-center">{value.includes(n.id) && <Check />}</span>
            <span className="font-mono">{n.name}</span>
            <span className="ml-auto text-xs text-muted-foreground">{n.online ? "在线" : "离线"}</span>
          </DropdownMenuItem>
        ))}
        {value.length > 0 && <DropdownMenuItem onSelect={() => onChange([])}>不限节点</DropdownMenuItem>}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function Chip({ on, onClick, children }: { on: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button type="button" aria-pressed={on} onClick={onClick} className={cn("h-6 rounded-md border px-2 text-xs", on ? "border-foreground bg-foreground/5" : "border-input bg-white")}>
      {children}
    </button>
  );
}

function LookEditor({ role, layout, onChange }: { role: RoleView; layout: OfficeLayout; onChange: (look: Look) => void }) {
  const look = role.look;
  const [tag, tag2] = previewTags(role);
  const set = (patch: Partial<Look>) => onChange({ ...look, ...patch });
  const random = useAction(() => roleActions.randomLook(role.role), { onSuccess: (l) => onChange(l) });
  return (
    <div className="flex items-center gap-3.5 rounded-[10px] border border-[#EFEDE8] px-3 py-2.5">
      <div className="flex h-[84px] w-16 shrink-0 items-end justify-center rounded-[10px] bg-[#F7F2EA] pb-1">
        <Avatar layout={layout} look={look} role={role.role} tier={role.tier === "coordinator" ? "coordinator" : role.tier} label={tag} scale={4} />
      </div>
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <span className="flex flex-wrap items-center gap-1.5">
          <span className="w-[30px] text-xs text-muted-foreground">颜色</span>
          {layout.looks.colors.map((hex) => (
            <button
              key={hex}
              type="button"
              aria-label={`颜色 ${hex}`}
              aria-pressed={look.color === hex}
              onClick={() => set({ color: hex })}
              className="size-5 rounded-full border-2 p-0"
              style={{ background: hex, borderColor: look.color === hex ? "#1F1E1C" : "#FFFFFF" }}
            />
          ))}
        </span>
        <span className="flex flex-wrap items-center gap-1.5">
          <span className="w-[30px] text-xs text-muted-foreground">衣着</span>
          {Object.entries(layout.looks.outfits).map(([key, piece]) => (
            <Chip key={key} on={look.outfit === key} onClick={() => set({ outfit: key })}>
              {piece.label}
            </Chip>
          ))}
        </span>
        <span className="flex flex-wrap items-center gap-1.5">
          <span className="w-[30px] text-xs text-muted-foreground">配饰</span>
          {Object.entries(layout.looks.accessories).map(([key, piece]) => (
            <Chip key={key} on={look.accessory === key} onClick={() => set({ accessory: key })}>
              {piece.label}
            </Chip>
          ))}
          <button type="button" onClick={() => random.mutate()} disabled={random.isPending} className="ml-auto h-6 rounded-md border border-dashed border-[#C9C5BC] bg-white px-2 text-xs">
            随机一个
          </button>
        </span>
        <span className="text-[11px] text-[#8A877F]">
          同岗位的成员衣着一致，发色和编号自动区分（{tag}、{tag2}…）
        </span>
      </div>
    </div>
  );
}

const GATES = [
  { value: "none", label: "普通岗位" },
  { value: "review", label: "审查关（合并前审查）" },
  { value: "decision", label: "决策关（方案取舍）" },
];

function RoleDialog({ open, onOpenChange, role, onSave }: { open: boolean; onOpenChange: (o: boolean) => void; role: RoleView; onSave: (p: Partial<RoleView>) => void }) {
  const [d, setD] = useState(role);
  useEffect(() => setD(role), [role, open]);
  const set = (p: Partial<RoleView>) => setD({ ...d, ...p });
  const member = d.tier === "member";
  return (
    <FormDialog open={open} onOpenChange={onOpenChange} title={`编辑 ${roleName(role)}`} submitLabel="确定" onSubmit={() => onSave(d)} className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="名称">
          <Input value={d.title} onChange={(e) => set({ title: e.target.value })} />
        </Field>
        <Field label="岗位键" hint="小写字母、数字和 -，成员编号用它（dev-1）">
          <Input value={d.role} disabled={!member} onChange={(e) => set({ role: e.target.value.trim() })} className="font-mono" />
        </Field>
        {member && (
          <Field label="每个讨论串的数量">
            <Input type="number" min={1} max={8} value={d.count} onChange={(e) => set({ count: Number(e.target.value) })} />
          </Field>
        )}
        <Field label="模型" hint="留空用节点默认模型">
          <Input value={d.model} onChange={(e) => set({ model: e.target.value.trim() })} className="font-mono" />
        </Field>
        <Field label="思考强度（effort）">
          <Input value={d.effort} onChange={(e) => set({ effort: e.target.value.trim() })} placeholder="例如 high" />
        </Field>
        <Field label="最多回合数">
          <Input type="number" min={1} value={d.max_turns ?? ""} onChange={(e) => set({ max_turns: e.target.value ? Number(e.target.value) : null })} />
        </Field>
      </div>
      <Field label="职责说明">
        <Textarea rows={2} value={d.description} onChange={(e) => set({ description: e.target.value })} />
      </Field>
      <Field label="指令" hint="开工时写进系统提示">
        <Textarea rows={4} value={d.instructions} onChange={(e) => set({ instructions: e.target.value })} />
      </Field>
      {member && (
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="把关">
            <Select value={d.gate || "none"} onValueChange={(v) => set({ gate: v === "none" ? "" : v })}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {GATES.map((g) => (
                  <SelectItem key={g.value} value={g.value}>
                    {g.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          {d.runtime === "codex" && (
            <Field label="Codex 沙箱">
              <Select value={d.sandbox || "workspace-write"} onValueChange={(v) => set({ sandbox: v })}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="read-only">read-only</SelectItem>
                  <SelectItem value="workspace-write">workspace-write</SelectItem>
                  <SelectItem value="danger-full-access">danger-full-access</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          )}
          <Field label="可领的任务类型" hint="回车添加，例如 dev、test">
            <TagInput values={d.task_kinds ?? []} onChange={(v) => set({ task_kinds: v })} />
          </Field>
          <Field label="允许的工具">
            <TagInput values={d.allowed_tools ?? []} onChange={(v) => set({ allowed_tools: v })} placeholder="Bash(git *)" />
          </Field>
          <Field label="禁用的工具">
            <TagInput values={d.disallowed_tools ?? []} onChange={(v) => set({ disallowed_tools: v })} />
          </Field>
          <label className="flex items-center justify-between gap-3 self-end rounded-lg border px-3 py-2 text-[13px]">
            只读（不改代码）
            <Switch checked={d.read_only ?? (d.gate === "review" || d.gate === "decision")} onCheckedChange={(v) => set({ read_only: v })} />
          </label>
          <label className="flex items-center justify-between gap-3 self-end rounded-lg border px-3 py-2 text-[13px]">
            可以给别人派活
            <Switch checked={d.can_delegate} onCheckedChange={(v) => set({ can_delegate: v })} />
          </label>
        </div>
      )}
    </FormDialog>
  );
}

function YamlEditor() {
  const yaml = useRolesYaml(true);
  const [text, setText] = useState("");
  useEffect(() => {
    if (yaml.data) setText(yaml.data.yaml);
  }, [yaml.data]);
  const save = useAction(() => roleActions.saveYaml(text), { invalidate: [keys.roles, keys.rolesYaml], success: "已保存" });
  if (yaml.isLoading) return <Loading />;
  return (
    <section className="flex flex-col gap-3 rounded-[14px] border bg-panel p-5">
      <p className="text-[13px] text-muted-foreground">岗位模板的 YAML（docs/threads.md 的格式，旧格式会自动转换）。</p>
      {yaml.error && <Callout tone="bad">{errorText(yaml.error)}</Callout>}
      <Textarea rows={22} value={text} onChange={(e) => setText(e.target.value)} className="font-mono text-xs leading-5" spellCheck={false} />
      <div className="flex justify-end">
        <Button disabled={save.isPending || text === yaml.data?.yaml} onClick={() => save.mutate()}>
          {save.isPending && <Spinner />} 保存 YAML
        </Button>
      </div>
    </section>
  );
}

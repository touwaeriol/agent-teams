// Plugin library: the Team page section (my plugins + the built-ins every user may tick) and
// the admin page section that manages built-in plugins and built-in skills. A plugin is a
// Claude Code plugin package uploaded as a zip; uploading the same name again adds a version.
import { FolderUp, Upload } from "lucide-react";
import { useRef } from "react";
import { admin as adminActions, plugins as pluginActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useAdminPlugins, usePlugins, useSkills } from "@/api/queries";
import type { PluginView, SkillFile, SkillView } from "@/api/views";
import { ConfirmAction, Loading } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { toBase64, pickedSkillFiles } from "@/components/app/SkillLibrary";
import { formatBytes } from "@/lib/artifacts";
import { contentsText, splitLibrary } from "@/lib/plugins";
import { timeAgo } from "@/lib/utils";

const HELP =
  "插件是 Claude Code 插件包：根目录 .claude-plugin/plugin.json，加上 skills/、commands/、agents/、hooks/hooks.json 或 .mcp.json。打包成 zip 上传（最多 200 个文件、合计 1 MB），同名再次上传会成为新版本。插件的工具调用照常需要审批；hooks 会在节点上直接运行命令。";

async function zipOf(f: File): Promise<string> {
  return toBase64(new Uint8Array(await f.arrayBuffer()));
}

function ZipButton({ label, pending, onZip }: { label: string; pending: boolean; onZip: (zip: string) => void }) {
  const input = useRef<HTMLInputElement>(null);
  const pick = async (f?: File) => f && onZip(await zipOf(f));
  return (
    <>
      <input ref={input} type="file" accept=".zip,application/zip" hidden onChange={(e) => (void pick(e.target.files?.[0]), (e.target.value = ""))} />
      <Button variant="outline" size="sm" disabled={pending} onClick={() => input.current?.click()}>
        <Upload /> {label}
      </Button>
    </>
  );
}

/** Team page: my plugins and the enabled built-ins. */
export function PluginLibrary() {
  const list = usePlugins();
  const upload = useAction((zip: string) => pluginActions.upload(zip), { invalidate: [keys.plugins], success: "插件已上传" });
  const { mine, builtin } = splitLibrary(list.data?.plugins ?? []);
  return (
    <section className="flex flex-col gap-3 rounded-[14px] border bg-panel p-[18px]" aria-label="插件库">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <h2 className="text-base font-semibold">插件库</h2>
          <p className="max-w-[620px] text-[13px] leading-[21px] text-muted-foreground">{HELP} 你上传的插件只有你能看到和使用。</p>
        </div>
        <ZipButton label="上传插件 zip" pending={upload.isPending} onZip={(zip) => upload.mutate(zip)} />
      </div>
      {list.isLoading && <Loading />}
      <PluginGroup title="我的插件" empty="还没有上传插件">
        {mine.map((p) => (
          <PluginRow key={p.id} plugin={p} actions={<OwnActions plugin={p} />} />
        ))}
      </PluginGroup>
      <PluginGroup title="内置插件" empty="管理员还没有发布内置插件">
        {builtin.map((p) => (
          <PluginRow key={p.id} plugin={p} />
        ))}
      </PluginGroup>
    </section>
  );
}

function PluginGroup({ title, empty, children }: { title: string; empty: string; children: React.ReactNode[] }) {
  return (
    <div className="flex flex-col gap-2">
      <h3 className="text-[13px] font-semibold text-muted-foreground">{title}</h3>
      {children.length === 0 ? <p className="text-xs text-muted-foreground">{empty}</p> : <div className="grid gap-2 sm:grid-cols-2">{children}</div>}
    </div>
  );
}

export function PluginRow({ plugin, actions }: { plugin: PluginView; actions?: React.ReactNode }) {
  const latest = plugin.versions?.[0];
  return (
    <div className="flex items-start justify-between gap-2 rounded-[10px] border px-3 py-2.5" data-plugin={plugin.name}>
      <span className="flex min-w-0 flex-col gap-0.5">
        <span className="flex flex-wrap items-center gap-2 text-[13px] font-medium">
          <span className="font-mono">{plugin.name}</span>
          <span className="rounded-full bg-raised px-2 py-0.5 text-[11px] font-normal text-muted-foreground">
            v{plugin.latest}
            {latest?.manifest_version ? ` · ${latest.manifest_version}` : ""}
          </span>
          {plugin.builtin && <span className="rounded-full bg-raised px-2 py-0.5 text-[11px] font-normal text-muted-foreground">内置</span>}
          {!plugin.enabled && <span className="rounded-full bg-[#F6E3E3] px-2 py-0.5 text-[11px] font-normal text-destructive">已停用</span>}
        </span>
        {latest?.description && <span className="line-clamp-2 text-xs leading-[18px] text-muted-foreground">{latest.description}</span>}
        <span className="text-xs text-muted-foreground">
          {contentsText(latest?.contents)}
          {latest && ` · ${formatBytes(latest.size)} · ${timeAgo(latest.created_at)}上传`}
        </span>
      </span>
      {actions}
    </div>
  );
}

function OwnActions({ plugin }: { plugin: PluginView }) {
  const invalidate = [keys.plugins, keys.roles];
  const toggle = useAction((on: boolean) => pluginActions.setEnabled(plugin.id, on), { invalidate });
  const remove = useAction(() => pluginActions.remove(plugin.id), { invalidate, success: "已删除" });
  return <RowActions plugin={plugin} onToggle={(on) => toggle.mutate(on)} onRemove={() => remove.mutate()} />;
}

function RowActions({ plugin, onToggle, onRemove }: { plugin: PluginView; onToggle: (on: boolean) => void; onRemove: () => void }) {
  return (
    <span className="flex shrink-0 items-center gap-1.5">
      <Switch checked={plugin.enabled} onCheckedChange={onToggle} aria-label={`${plugin.enabled ? "停用" : "启用"}插件 ${plugin.name}`} />
      <ConfirmAction iconOnly={`删除插件 ${plugin.name}`} title={`删除插件 ${plugin.name}？`} description="所有版本都会删除，用到它的岗位会去掉这个插件，已经开工的员工下次开会话时生效。" onConfirm={onRemove} />
    </span>
  );
}

/** Admin page: built-in plugins (disabled ones included) and admin-created built-in skills. */
export function BuiltinLibrary() {
  const list = useAdminPlugins();
  const upload = useAction((zip: string) => adminActions.uploadPlugin(zip), { invalidate: [keys.adminPlugins, keys.plugins], success: "内置插件已上传" });
  return (
    <section className="flex flex-col gap-3 rounded-[14px] border bg-panel p-[18px]" aria-label="内置插件和技能">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <h2 className="text-base font-semibold">内置插件</h2>
          <p className="max-w-[620px] text-[13px] leading-[21px] text-muted-foreground">所有用户都能在岗位上勾选内置插件，它会运行在用户自己的节点上。停用后用户看不到它，已勾选的岗位也不再加载。</p>
        </div>
        <ZipButton label="上传内置插件" pending={upload.isPending} onZip={(zip) => upload.mutate(zip)} />
      </div>
      {list.isLoading && <Loading />}
      <PluginGroup title="插件" empty="还没有内置插件">
        {(list.data?.plugins ?? []).map((p) => (
          <PluginRow key={p.id} plugin={p} actions={<BuiltinActions plugin={p} />} />
        ))}
      </PluginGroup>
      <BuiltinSkills />
    </section>
  );
}

function BuiltinActions({ plugin }: { plugin: PluginView }) {
  const invalidate = [keys.adminPlugins, keys.plugins, keys.roles];
  const toggle = useAction((on: boolean) => adminActions.setPluginEnabled(plugin.id, on), { invalidate });
  const remove = useAction(() => adminActions.removePlugin(plugin.id), { invalidate, success: "已删除" });
  return <RowActions plugin={plugin} onToggle={(on) => toggle.mutate(on)} onRemove={() => remove.mutate()} />;
}

async function readFolder(list: FileList): Promise<SkillFile[]> {
  const entries = await Promise.all([...list].map(async (f) => ({ path: f.webkitRelativePath || f.name, text: await f.text() })));
  return pickedSkillFiles(entries);
}

function BuiltinSkills() {
  const skills = useSkills();
  const dirInput = useRef<HTMLInputElement>(null);
  const invalidate = [keys.skills];
  const upload = useAction((body: { zip?: string; files?: SkillFile[] }) => adminActions.createSkill(body), { invalidate, success: "内置技能已上传" });
  const builtins = (skills.data?.skills ?? []).filter((s) => s.builtin);
  const folderProps = { webkitdirectory: "", directory: "" } as Record<string, string>;
  return (
    <div className="flex flex-col gap-2 border-t border-[#F1EFEA] pt-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-[13px] font-semibold text-muted-foreground">内置技能</h3>
        <span className="flex gap-2">
          <ZipButton label="上传技能 zip" pending={upload.isPending} onZip={(zip) => upload.mutate({ zip })} />
          <input ref={dirInput} type="file" multiple hidden {...folderProps} onChange={(e) => (e.target.files?.length && void readFolder(e.target.files).then((files) => upload.mutate({ files })), (e.target.value = ""))} />
          <Button variant="outline" size="sm" disabled={upload.isPending} onClick={() => dirInput.current?.click()}>
            <FolderUp /> 选择文件夹
          </Button>
        </span>
      </div>
      <div className="grid gap-2 sm:grid-cols-2">
        {builtins.map((s) => (
          <BuiltinSkillRow key={s.id || s.name} skill={s} />
        ))}
      </div>
    </div>
  );
}

function BuiltinSkillRow({ skill }: { skill: SkillView }) {
  const remove = useAction(() => adminActions.removeSkill(skill.id), { invalidate: [keys.skills, keys.roles], success: "已删除" });
  return (
    <div className="flex items-start justify-between gap-2 rounded-[10px] border px-3 py-2.5">
      <span className="flex min-w-0 flex-col gap-0.5">
        <span className="flex items-center gap-2 text-[13px] font-medium">
          <span className="font-mono">{skill.name}</span>
          <span className="rounded-full bg-raised px-2 py-0.5 text-[11px] font-normal text-muted-foreground">{skill.id ? "管理员发布" : "随平台发布"}</span>
        </span>
        <span className="line-clamp-2 text-xs leading-[18px] text-muted-foreground">{skill.description}</span>
      </span>
      {skill.id && <ConfirmAction iconOnly={`删除内置技能 ${skill.name}`} title={`删除内置技能 ${skill.name}？`} description="所有用户的岗位都会去掉这个技能。" onConfirm={() => remove.mutate()} />}
    </div>
  );
}

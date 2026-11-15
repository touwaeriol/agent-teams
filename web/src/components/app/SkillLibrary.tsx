// Skill library on the Team page: built-in skills plus the user's uploads (a zip, or a
// picked folder of text files with SKILL.md). Roles tick the skills they load in RoleCard.
import { FolderUp, Upload } from "lucide-react";
import { useRef } from "react";
import { skills as skillActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useSkills } from "@/api/queries";
import type { SkillFile, SkillView } from "@/api/views";
import { ConfirmAction, Loading } from "@/components/common";
import { Button } from "@/components/ui/button";
import { formatBytes } from "@/lib/artifacts";

/** Text files of a picked folder: hidden files and folders (.git, .DS_Store) are left out. */
export function pickedSkillFiles(entries: { path: string; text: string }[]): SkillFile[] {
  return entries
    .filter((e) => !e.path.split("/").some((part) => part.startsWith(".") || part === "__MACOSX"))
    .map((e) => ({ path: e.path, content: e.text }));
}

/** Base64 of bytes (zip upload), in chunks so large files do not overflow the call stack. */
export function toBase64(bytes: Uint8Array): string {
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}

async function readFolder(list: FileList): Promise<SkillFile[]> {
  const entries = await Promise.all([...list].map(async (f) => ({ path: f.webkitRelativePath || f.name, text: await f.text() })));
  return pickedSkillFiles(entries);
}

export function SkillLibrary() {
  const list = useSkills();
  const zipInput = useRef<HTMLInputElement>(null);
  const dirInput = useRef<HTMLInputElement>(null);
  const invalidate = [keys.skills, keys.roles];
  const upload = useAction((body: { zip?: string; files?: SkillFile[] }) => skillActions.create(body), { invalidate, success: "技能已上传" });
  const onZip = async (f?: File) => f && upload.mutate({ zip: toBase64(new Uint8Array(await f.arrayBuffer())) });
  const onDir = async (l: FileList | null) => l && l.length > 0 && upload.mutate({ files: await readFolder(l) });
  const folderProps = { webkitdirectory: "", directory: "" } as Record<string, string>;
  return (
    <section className="flex flex-col gap-3 rounded-[14px] border bg-panel p-[18px]">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <h2 className="text-base font-semibold">技能库</h2>
          <p className="max-w-[620px] text-[13px] leading-[21px] text-muted-foreground">
            技能是一份 SKILL.md 说明（可以附带参考文件），员工开工时按岗位加载。上传 zip 或选择文件夹，SKILL.md 开头需要 name 和 description；只收文本文件，单个技能最多 512 KB。
          </p>
        </div>
        <div className="flex gap-2">
          <input ref={zipInput} type="file" accept=".zip,application/zip" hidden onChange={(e) => (void onZip(e.target.files?.[0]), (e.target.value = ""))} />
          <input ref={dirInput} type="file" multiple hidden {...folderProps} onChange={(e) => (void onDir(e.target.files), (e.target.value = ""))} />
          <Button variant="outline" size="sm" disabled={upload.isPending} onClick={() => zipInput.current?.click()}>
            <Upload /> 上传 zip
          </Button>
          <Button variant="outline" size="sm" disabled={upload.isPending} onClick={() => dirInput.current?.click()}>
            <FolderUp /> 选择文件夹
          </Button>
        </div>
      </div>
      {list.isLoading && <Loading />}
      <div className="grid gap-2 sm:grid-cols-2">
        {(list.data?.skills ?? []).map((s) => (
          <SkillRow key={s.id || s.name} skill={s} />
        ))}
      </div>
    </section>
  );
}

function SkillRow({ skill }: { skill: SkillView }) {
  const remove = useAction(() => skillActions.remove(skill.id), { invalidate: [keys.skills, keys.roles], success: "已删除" });
  return (
    <div className="flex items-start justify-between gap-2 rounded-[10px] border px-3 py-2.5">
      <span className="flex min-w-0 flex-col gap-0.5">
        <span className="flex items-center gap-2 text-[13px] font-medium">
          <span className="font-mono">{skill.name}</span>
          <span className="rounded-full bg-raised px-2 py-0.5 text-[11px] font-normal text-muted-foreground">{skill.builtin ? "内置" : formatBytes(skill.size)}</span>
        </span>
        <span className="line-clamp-2 text-xs leading-[18px] text-muted-foreground">{skill.description}</span>
      </span>
      {!skill.builtin && (
        <ConfirmAction iconOnly={`删除技能 ${skill.name}`} title={`删除技能 ${skill.name}？`} description="用到它的岗位会去掉这个技能，已经开工的员工下次开会话时生效。" onConfirm={() => remove.mutate()} />
      )}
    </div>
  );
}

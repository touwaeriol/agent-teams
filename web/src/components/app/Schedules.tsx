// 定时任务 dialog: messages the project coordinator gets on a schedule.
import { Pencil, Play, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { schedules as scheduleActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useSchedules } from "@/api/queries";
import type { ScheduleView } from "@/api/views";
import { Field, IconButton, Loading } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { formatDateTime } from "@/lib/library";
import { browserTimeZone, CRON_PRESETS, CUSTOM, describeCron, looksLikeCron, presetOf } from "@/lib/schedules";

type Draft = { id?: string; name: string; cron: string; message: string };

const EMPTY: Draft = { name: "", cron: CRON_PRESETS[0].cron, message: "" };

export function SchedulesDialog({ pid, open, onOpenChange }: { pid: string; open: boolean; onOpenChange: (open: boolean) => void }) {
  const list = useSchedules(pid, open);
  const [draft, setDraft] = useState<Draft | null>(null);
  const items = list.data ?? [];
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) setDraft(null);
        onOpenChange(o);
      }}
    >
      <DialogContent onOpenAutoFocus={(e) => e.preventDefault()} className="max-w-xl gap-5 rounded-[14px] outline-none">
        <DialogHeader>
          <DialogTitle className="text-base">定时任务</DialogTitle>
          <DialogDescription>到点后把消息发给项目统筹，由它开讨论串去做。按你的时区（{browserTimeZone()}）计算。</DialogDescription>
        </DialogHeader>
        {draft ? (
          <ScheduleForm pid={pid} draft={draft} onChange={setDraft} onDone={() => setDraft(null)} />
        ) : (
          <div className="flex flex-col gap-2">
            {list.isLoading && <Loading />}
            {list.data && items.length === 0 && <p className="rounded-xl border border-dashed p-6 text-center text-[13px] text-muted-foreground">还没有定时任务。例如每天 9:00 让统筹汇报各讨论串的进展。</p>}
            {items.map((s) => (
              <ScheduleRow key={s.id} pid={pid} item={s} onEdit={() => setDraft({ id: s.id, name: s.name, cron: s.cron, message: s.message })} />
            ))}
            <Button variant="outline" className="mt-1 self-start" onClick={() => setDraft(EMPTY)}>
              <Plus /> 新建定时任务
            </Button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function ScheduleRow({ pid, item, onEdit }: { pid: string; item: ScheduleView; onEdit: () => void }) {
  const invalidate = [keys.schedules(pid)];
  const toggle = useAction((enabled: boolean) => scheduleActions.update(item.id, { enabled }), { invalidate });
  const run = useAction(() => scheduleActions.run(item.id), { invalidate, success: "已发给统筹" });
  const remove = useAction(() => scheduleActions.remove(item.id), { invalidate, success: "已删除" });
  return (
    <div className="flex items-start gap-3 rounded-xl border bg-panel px-3.5 py-3">
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="flex items-center gap-2">
          <span className="truncate font-medium">{item.name}</span>
          <span className="shrink-0 rounded-md bg-raised px-1.5 py-px text-xs text-muted-foreground">{describeCron(item.cron)}</span>
        </span>
        <span className="line-clamp-2 text-[13px] text-muted-foreground">{item.message}</span>
        <span className="text-xs text-muted-foreground">
          {item.enabled && item.next_run_at ? `下次 ${formatDateTime(item.next_run_at)}` : "已停用"}
          {item.last_run_at ? ` · 上次 ${formatDateTime(item.last_run_at)}` : ""}
        </span>
        {item.last_error && <span className="text-xs text-destructive">上次没发出去：{item.last_error}</span>}
      </span>
      <span className="flex shrink-0 items-center gap-0.5">
        <IconButton label="现在运行一次" onClick={() => run.mutate()} disabled={run.isPending}>
          <Play strokeWidth={1.8} />
        </IconButton>
        <IconButton label="编辑" onClick={onEdit}>
          <Pencil strokeWidth={1.8} />
        </IconButton>
        <IconButton label="删除" onClick={() => remove.mutate()}>
          <Trash2 strokeWidth={1.8} />
        </IconButton>
        <Switch checked={item.enabled} onCheckedChange={(v) => toggle.mutate(v)} aria-label={`启用 ${item.name}`} className="ml-1.5" />
      </span>
    </div>
  );
}

function ScheduleForm({ pid, draft, onChange, onDone }: { pid: string; draft: Draft; onChange: (d: Draft) => void; onDone: () => void }) {
  const [preset, setPreset] = useState(presetOf(draft.cron));
  const body = { name: draft.name.trim(), cron: draft.cron.trim(), message: draft.message.trim(), timezone: browserTimeZone() };
  const save = useAction(() => (draft.id ? scheduleActions.update(draft.id, body) : scheduleActions.create(pid, { ...body, enabled: true })), {
    invalidate: [keys.schedules(pid)],
    success: "已保存",
    onSuccess: onDone,
  });
  const valid = body.name && body.message && looksLikeCron(body.cron);
  const choose = (id: string) => {
    setPreset(id);
    const p = CRON_PRESETS.find((x) => x.id === id);
    if (p) onChange({ ...draft, cron: p.cron });
  };
  return (
    <form
      className="flex flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (valid) save.mutate();
      }}
    >
      <Field label="名称">
        <Input value={draft.name} onChange={(e) => onChange({ ...draft, name: e.target.value })} placeholder="例如：每日进展" autoFocus maxLength={100} />
      </Field>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="什么时候">
          <Select value={preset} onValueChange={choose}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {CRON_PRESETS.map((p) => (
                <SelectItem key={p.id} value={p.id}>
                  {p.label}
                </SelectItem>
              ))}
              <SelectItem value={CUSTOM}>自定义（cron）</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        <Field label="cron 表达式" hint="分 时 日 月 周">
          <Input
            value={draft.cron}
            onChange={(e) => {
              onChange({ ...draft, cron: e.target.value });
              setPreset(presetOf(e.target.value));
            }}
            className="font-mono"
            aria-invalid={!looksLikeCron(draft.cron)}
          />
        </Field>
      </div>
      <Field label="发给统筹的消息">
        <Textarea rows={4} value={draft.message} onChange={(e) => onChange({ ...draft, message: e.target.value })} placeholder="例如：汇总昨天各讨论串的进展，有卡住的告诉我" maxLength={20000} />
      </Field>
      <span className="flex justify-end gap-2">
        <Button type="button" variant="outline" onClick={onDone}>
          取消
        </Button>
        <Button type="submit" disabled={!valid || save.isPending}>
          保存
        </Button>
      </span>
    </form>
  );
}

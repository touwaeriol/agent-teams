<script setup lang="ts">
/**
 * PR acceptance: 验收通过并合并 / 提修改意见 (reason goes to the lead).
 * Accepting is asynchronous: the server answers at once with pr_state "accepting" and merges in the
 * background; live thread events bring "merged" or pr_state back to "open" with merge_error. The card
 * switches to 合并中 the moment the button is pressed (optimistic cache update, rolled back if the
 * request itself fails) and stays locked while pr_state is "accepting".
 */
import { useMutation, useQueryClient } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import { threads as threadActions } from "@/api/actions";
import { patchThread } from "@/api/events";
import { errorText, useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import Spinner from "@/components/common/Spinner.vue";
import { INK, threadBases, threadPRs } from "@/lib/present";
import { safeHref } from "@/lib/safeurl";
import { toast } from "@/lib/toast";
import CardActions from "./CardActions.vue";
import CardButton from "./CardButton.vue";
import CardFrame from "./CardFrame.vue";
import ReasonBox from "./ReasonBox.vue";
import type { CardVariant } from "./variant";

const props = withDefaults(defineProps<{ thread: ThreadView; variant?: CardVariant; files?: number }>(), { variant: "soft", files: undefined });
const client = useQueryClient();
const rejecting = ref(false);
const invalidate = () => [keys.threads(props.thread.project_id), keys.thread(props.thread.id), keys.waiting(props.thread.project_id)];
const refresh = () => invalidate().forEach((queryKey) => client.invalidateQueries({ queryKey }));
const patch = (fn: (t: ThreadView) => ThreadView) => patchThread(client, props.thread.project_id, props.thread.id, fn);

type Before = Pick<ThreadView, "pr_state" | "merge_error">;
// Set in the click handler itself: the mutation's isPending only reaches the component on a later
// tick, and the DOM's disabled attribute after the next render, so a double click would send twice.
const accepting = ref(false);
const accept = useMutation<ThreadView, Error, void, Before>({
  mutationFn: () => threadActions.accept(props.thread.id),
  onMutate: async () => {
    const before: Before = { pr_state: props.thread.pr_state, merge_error: props.thread.merge_error ?? null };
    // A refetch already on its way would land the old state over the optimistic one.
    await Promise.all(invalidate().slice(0, 2).map((queryKey) => client.cancelQueries({ queryKey })));
    patch((t) => ({ ...t, pr_state: "accepting", merge_error: null }));
    return before;
  },
  onSuccess: (view) => {
    if (view && typeof view === "object") patch((t) => ({ ...t, ...view }));
    toast.success("已开始合并，完成后讨论串会自动更新");
  },
  onError: (e, _vars, before) => {
    // Only undo our own change: a live event may already have moved the thread on.
    if (before) patch((t) => (t.pr_state === "accepting" ? { ...t, ...before } : t));
    toast.error(errorText(e));
  },
  onSettled: () => {
    accepting.value = false;
    refresh();
  },
});
function startAccept() {
  if (accepting.value || merging.value) return;
  accepting.value = true;
  accept.mutate();
}

const sending = ref(false);
const reject = useAction((reason: string) => threadActions.reject(props.thread.id, reason), {
  invalidate,
  success: "修改意见已发给组长",
  onSuccess: () => (rejecting.value = false),
});
function sendReject(reason: string) {
  if (!reason || sending.value || merging.value) return;
  sending.value = true;
  reject.mutate(reason, { onSettled: () => (sending.value = false) });
}

/** 合并中: pressed here (request in flight) or reported by the server (pr_state accepting). */
const merging = computed(() => accepting.value || props.thread.pr_state === "accepting");
const mergeError = computed(() => (!merging.value && props.thread.merge_error) || "");
const prs = computed(() => threadPRs(props.thread));
const subject = computed(() => (prs.value.length > 1 ? `${prs.value.length} 个 PR` : props.thread.pr_number ? `PR #${props.thread.pr_number}` : "讨论串"));
const code = computed(() => `${props.thread.branch} → ${threadBases(props.thread) || "基准分支"}${props.files ? ` · 改了 ${props.files} 个文件` : ""}`);
const label = computed(() => (merging.value ? "合并中" : mergeError.value ? "合并失败" : "等你验收"));
</script>

<template>
  <CardFrame :variant="variant" :code="code" :label="label">
    <template #head>
      <span v-if="merging" class="inline-flex items-center gap-1.5">
        {{ subject }} <span class="inline-flex items-center gap-1" :style="{ color: INK.merge }"><Spinner class="size-3" />合并中…</span>
      </span>
      <span v-else-if="mergeError">{{ subject }} <span class="text-destructive">合并失败</span> · <span class="text-[#B4530B]">等你处理</span></span>
      <span v-else>{{ subject }} 等你验收 · <span class="text-[#B4530B]">等你处理</span></span>
    </template>
    <p v-if="thread.summary" class="text-xs leading-5 text-[#4A4843]">{{ thread.summary }}</p>
    <p v-if="merging" class="text-xs leading-5 text-muted-foreground">平台正在检查并合并，可以先去做别的，完成后这里会自动更新。</p>
    <p v-else-if="mergeError" role="alert" class="rounded-lg border border-[#EBC3C3] bg-white px-2.5 py-2 text-xs leading-5 text-destructive">合并没有成功：{{ mergeError }}</p>
    <ReasonBox
      v-if="rejecting && !merging"
      placeholder="哪里要改？会发给组长"
      confirm="发给组长"
      required
      :busy="sending || reject.isPending.value"
      @cancel="rejecting = false"
      @submit="sendReject"
    />
    <CardActions v-else :variant="variant" :count="2">
      <CardButton kind="primary" :variant="variant" :busy="merging" @click="startAccept">{{ merging ? "合并中…" : mergeError ? "重试合并" : "验收通过并合并" }}</CardButton>
      <CardButton kind="outline" :variant="variant" :disabled="merging" @click="rejecting = true">提修改意见</CardButton>
      <template v-if="variant !== 'mobile'">
        <a
          v-for="pr in prs"
          :key="pr.url ?? pr.number"
          :href="safeHref(pr.url)"
          target="_blank"
          rel="noreferrer"
          class="inline-flex h-8 items-center px-1 text-[13px] text-muted-foreground hover:text-foreground"
        >
          {{ prs.length > 1 ? `${pr.repo} #${pr.number} ↗` : "在 GitHub 上看 ↗" }}
        </a>
      </template>
    </CardActions>
  </CardFrame>
</template>

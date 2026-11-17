<script setup lang="ts">
/**
 * PR acceptance: 验收通过并合并 / 提修改意见 (reason goes to the lead).
 * Accepting is asynchronous: the server answers at once with pr_state "accepting" and merges in the
 * background; live thread events bring "merged" or pr_state back to "open" with merge_error. The card
 * switches to 合并中 the moment the button is pressed (optimistic cache update, rolled back if the
 * request itself fails) and the button stays locked while pr_state is "accepting". 提修改意见 stays
 * open then: rejecting withdraws an accept that waits on long checks.
 */
import { useMutation, useQueryClient } from "@tanstack/vue-query";
import { computed, ref, watch } from "vue";
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
    await client.cancelQueries({ queryKey: keys.threads(props.thread.project_id) });
    await client.cancelQueries({ queryKey: keys.thread(props.thread.id) });
    patch((t) => ({ ...t, pr_state: "accepting", merge_error: null }));
    return before;
  },
  onSuccess: (view) => {
    // A live event that came first (merged, or failed back to open) wins over this older answer.
    if (view && typeof view === "object") patch((t) => (t.pr_state === "accepting" ? { ...t, ...view } : t));
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
// The api client has no request timeout: when live events end the merge (open with merge_error, or
// merged) the lock goes too, so a hung request cannot keep the card locked.
watch(
  () => props.thread.pr_state,
  (now, was) => {
    if (was === "accepting" && now !== "accepting") accepting.value = false;
  },
);
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
  if (!reason || sending.value || accepting.value) return;
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
        {{ subject }} <span class="inline-flex items-center gap-1" :style="{ color: INK.merge }"><Spinner class="size-3" decorative />合并中…</span>
      </span>
      <span v-else-if="mergeError">{{ subject }} <span class="text-destructive">合并失败</span> · <span class="text-[#B4530B]">等你处理</span></span>
      <span v-else>{{ subject }} 等你验收 · <span class="text-[#B4530B]">等你处理</span></span>
    </template>
    <p v-if="thread.summary" class="text-xs leading-5 text-[#4A4843]">{{ thread.summary }}</p>
    <p v-if="merging" class="text-xs leading-5 text-muted-foreground">平台正在检查并合并，可以先去做别的，完成后这里会自动更新。</p>
    <p v-else-if="mergeError" role="alert" class="rounded-lg border border-[#EBC3C3] bg-white px-2.5 py-2 text-xs leading-5 text-destructive">{{ mergeError }}</p>
    <ReasonBox
      v-if="rejecting"
      placeholder="哪里要改？会发给组长"
      confirm="发给组长"
      required
      :busy="sending || reject.isPending.value"
      @cancel="rejecting = false"
      @submit="sendReject"
    />
    <CardActions v-else :variant="variant" :count="2">
      <CardButton kind="primary" :variant="variant" :busy="merging" @click="startAccept">{{ merging ? "合并中…" : mergeError ? "重试合并" : "验收通过并合并" }}</CardButton>
      <CardButton kind="outline" :variant="variant" :disabled="accepting" @click="rejecting = true">提修改意见</CardButton>
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

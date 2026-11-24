<script setup lang="ts">
/**
 * 上下文 of a session's 会话信息: the total bar (SessionContext) with its breakdown, a 刷新 button
 * and when it was last counted. Only a runtime that reports a breakdown (Claude) shows it: for
 * Codex and Antigravity the whole block stays hidden, total included (user decision, #53).
 *
 * Updates: after every turn the server stores the summary and sends the project event context
 * (events.ts invalidates the query). 刷新 asks for the precise count (POST …/context/refresh, 202
 * {refresh_id}); the answer is a context event with the same refresh_id — with error when it
 * failed, without updated_at when it was older than what is stored — and ends the spinner either way.
 */
import { RefreshCw } from "lucide-vue-next";
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useQueryClient } from "@tanstack/vue-query";
import { conversations } from "@/api/actions";
import { ApiError } from "@/api/client";
import type { ContextEvent } from "@/api/events";
import { useContextDetail } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import { toast } from "@/lib/toast";
import { cn, timeAgo } from "@/lib/utils";
import SessionContext from "./SessionContext.vue";
import SessionContextDetail from "./SessionContextDetail.vue";
import { updatedText } from "./contextDetail";

/** How long 刷新 waits for its answer before giving up (the node itself waits 30 s for the count). */
const REFRESH_TIMEOUT_MS = 45_000;

const props = defineProps<{ thread: Pick<ThreadView, "id" | "session"> }>();
const cid = computed(() => props.thread.id);
/** A runtime known to have no breakdown is not even asked. */
const maybe = computed(() => {
  const rt = props.thread.session?.runtime;
  return !rt || rt === "claude";
});
const q = useContextDetail(cid, () => maybe.value);
const detail = computed(() => (maybe.value && q.data.value?.supported ? q.data.value : null));
/** The bar's numbers: the session's own (thread events) or, before them, the breakdown's. */
const session = computed(() => {
  const s = props.thread.session;
  const d = detail.value;
  if (s?.context_used != null && s.context_max) return s;
  return d && d.max_tokens > 0 ? { context_used: d.total_tokens, context_max: d.max_tokens } : s;
});
const hasRows = computed(() => (detail.value?.categories ?? []).length > 0);

// can_refresh is a snapshot: a session that parks or wakes changes it.
watch(
  () => props.thread.session?.state,
  (now, before) => {
    if (before !== undefined && now !== before) void q.refetch();
  },
);

const qc = useQueryClient();
const pending = ref<string | null>(null);
let timer: ReturnType<typeof setTimeout> | undefined;
function settle() {
  pending.value = null;
  if (timer) clearTimeout(timer);
  timer = undefined;
}
// The answer to 刷新 is the context event with its refresh_id (events.ts keeps the latest one per conversation).
const unsub = ref<(() => void) | null>(null);
onMounted(() => {
  unsub.value = qc.getQueryCache().subscribe((e) => {
    if (e.query.queryKey[0] !== "c" || e.query.queryKey[1] !== cid.value || e.query.queryKey[2] !== "context-event") return;
    const ev = e.query.state.data as ContextEvent | undefined;
    if (!ev || !pending.value || ev.refresh_id !== pending.value) return;
    settle();
    if (ev.error) toast.error(`刷新失败：${ev.error}`);
  });
});
onBeforeUnmount(() => {
  unsub.value?.();
  settle();
});

async function refresh() {
  if (pending.value) return;
  pending.value = "…"; // locked at once (a double click sends one request)
  try {
    const r = await conversations.refreshContext(cid.value);
    pending.value = r.refresh_id;
    timer = setTimeout(() => {
      if (!pending.value) return;
      settle();
      toast.error("刷新超时，稍后再试");
      void q.refetch();
    }, REFRESH_TIMEOUT_MS);
  } catch (e) {
    settle();
    toast.error(e instanceof ApiError ? e.message : "刷新失败");
    void q.refetch(); // 409: the session parked, the node went away…
  }
}

const now = ref(Date.now());
let tick: ReturnType<typeof setInterval> | undefined;
onMounted(() => (tick = setInterval(() => (now.value = Date.now()), 30_000)));
onBeforeUnmount(() => tick && clearInterval(tick));
const updated = computed(() => {
  void now.value;
  return detail.value ? updatedText(detail.value, timeAgo) : null;
});
const refreshTitle = computed(() =>
  detail.value?.can_refresh ? "立刻重新统计（精确值，调用 token 计数接口，不跑模型）" : "会话没有在运行（已停放或节点离线），发一条消息后再刷新",
);
defineExpose({ refresh, pending });
</script>

<template>
  <SessionContext v-if="detail" :session="session">
    <template #actions>
      <button
        type="button"
        :class="cn('flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs text-muted-foreground hover:bg-raised hover:text-foreground disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:bg-transparent')"
        :disabled="!detail.can_refresh || pending !== null"
        :title="refreshTitle"
        data-testid="context-refresh"
        @click="refresh"
      >
        <RefreshCw :class="cn('size-3', pending !== null && 'animate-spin')" />
        {{ pending !== null ? "刷新中…" : "刷新" }}
      </button>
    </template>
    <SessionContextDetail v-if="hasRows" :detail="detail" />
    <p class="text-[11.5px] leading-5 text-muted-foreground" data-testid="context-updated">
      <template v-if="updated">{{ updated }}。</template>每轮结束自动更新，“刷新”取精确值；延迟加载的工具用到时才载入，不计入已使用。
    </p>
  </SessionContext>
</template>

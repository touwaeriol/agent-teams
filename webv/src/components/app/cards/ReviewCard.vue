<script setup lang="ts">
/** PR acceptance: 验收通过并合并 / 提修改意见 (reason goes to the lead). */
import { computed, ref } from "vue";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import { threadBases, threadPRs } from "@/lib/present";
import { safeHref } from "@/lib/safeurl";
import CardActions from "./CardActions.vue";
import CardButton from "./CardButton.vue";
import CardFrame from "./CardFrame.vue";
import ReasonBox from "./ReasonBox.vue";
import type { CardVariant } from "./variant";

const props = withDefaults(defineProps<{ thread: ThreadView; variant?: CardVariant; files?: number }>(), { variant: "soft", files: undefined });
const rejecting = ref(false);
const invalidate = () => [keys.threads(props.thread.project_id), keys.thread(props.thread.id), keys.waiting(props.thread.project_id)];
const accept = useAction(() => threadActions.accept(props.thread.id), { invalidate, success: "已验收，平台会在检查通过后合并" });
const reject = useAction((reason: string) => threadActions.reject(props.thread.id, reason), { invalidate, success: "修改意见已发给组长" });
const prs = computed(() => threadPRs(props.thread));
const subject = computed(() => (prs.value.length > 1 ? `${prs.value.length} 个 PR` : props.thread.pr_number ? `PR #${props.thread.pr_number}` : "讨论串"));
const code = computed(() => `${props.thread.branch} → ${threadBases(props.thread) || "基准分支"}${props.files ? ` · 改了 ${props.files} 个文件` : ""}`);
</script>

<template>
  <CardFrame :variant="variant" :code="code" label="等你验收">
    <template #head>
      <span>{{ subject }} 等你验收 · <span class="text-[#B4530B]">等你处理</span></span>
    </template>
    <p v-if="thread.summary" class="text-xs leading-5 text-[#4A4843]">{{ thread.summary }}</p>
    <ReasonBox v-if="rejecting" placeholder="哪里要改？会发给组长" confirm="发给组长" :busy="reject.isPending.value" @cancel="rejecting = false" @submit="(r) => r && reject.mutate(r)" />
    <CardActions v-else :variant="variant" :count="2">
      <CardButton kind="primary" :variant="variant" :busy="accept.isPending.value" @click="accept.mutate()">验收通过并合并</CardButton>
      <CardButton kind="outline" :variant="variant" @click="rejecting = true">提修改意见</CardButton>
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

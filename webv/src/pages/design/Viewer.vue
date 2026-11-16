<script setup lang="ts">
// A design or any file published with publish_file, shown full size, with a version switcher,
// comments, "新窗口打开" and "通过，按这个做" (tells the lead to go ahead).
import { ArrowLeft, ExternalLink } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import { RouterLink } from "vue-router";
import { conversations } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useDeliverableFiles } from "@/api/queries";
import type { DeliverableView, ThreadView } from "@/api/views";
import { isHosted } from "@/lib/artifacts";
import { deliverableKind } from "@/lib/present";
import { safeHref } from "@/lib/safeurl";
import Comments from "./Comments.vue";
import HostedContent from "./HostedContent.vue";
import LinkFrame from "./LinkFrame.vue";
import VersionPicker from "./VersionPicker.vue";
import { approvalText, viewerSubtitle } from "./viewer";

const props = defineProps<{ pid: string; item: DeliverableView; thread?: ThreadView }>();
const hosted = computed(() => isHosted(props.item));
const version = ref(0);
watch(() => [props.item.id, props.item.detail.version], () => (version.value = 0));
const files = useDeliverableFiles(() => props.item.id, version, hosted);
const author = computed(() => String(props.item.detail.author ?? props.item.published_by ?? ""));
const shown = computed<number | undefined>(() => files.data.value?.version ?? (props.item.detail.version as number | undefined));
const label = computed(() => deliverableKind(props.item.kind).label);
const approve = useAction(() => conversations.send(props.thread?.id ?? "", approvalText(label.value, props.item.title, shown.value)), {
  invalidate: () => [keys.messages(props.thread?.id ?? "")],
  success: "已告诉组长按这个做",
});
const sub = computed(() => viewerSubtitle(props.thread, author.value, props.item.updated_at));
const external = computed(() => (hosted.value ? files.data.value?.entry_url : safeHref(props.item.url)));
</script>

<template>
  <div class="flex min-h-full flex-col bg-[#ECEAE5] dark:bg-background">
    <header class="flex min-h-[52px] flex-wrap items-center gap-x-4 gap-y-2.5 border-b bg-panel px-4 py-2">
      <RouterLink :to="`/projects/${pid}/deliverables`" aria-label="返回交付物" class="flex size-9 items-center justify-center rounded-lg hover:bg-raised">
        <ArrowLeft class="size-[18px]" :stroke-width="1.8" />
      </RouterLink>
      <span class="flex min-w-0 flex-[1_1_260px] flex-col">
        <span class="truncate font-semibold">{{ item.title }} · {{ label }}</span>
        <span class="truncate text-xs text-muted-foreground">{{ sub }}</span>
      </span>
      <span class="flex flex-wrap items-center gap-1.5">
        <VersionPicker v-if="hosted && files.data.value" :files="files.data.value" @pick="(v) => (version = v)" />
        <el-button tag="a" size="small" :href="external" target="_blank" rel="noreferrer noopener" class="!ml-0">新窗口打开 <ExternalLink /></el-button>
        <el-button type="primary" size="small" class="!ml-0" :disabled="!thread || approve.isPending.value" @click="approve.mutate()">通过，按这个做</el-button>
      </span>
    </header>
    <div class="flex flex-1 flex-wrap">
      <section aria-label="内容" class="flex min-w-0 flex-[999_1_640px] p-4 sm:px-12 sm:py-10">
        <HostedContent v-if="hosted" :title="item.title" :files="files.data.value" :loading="files.isLoading.value" :failed="files.isError.value" />
        <LinkFrame v-else :item="item" />
      </section>
      <Comments :item="item" :thread="thread" :author="author" :version="shown" />
    </div>
  </div>
</template>

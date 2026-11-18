<script setup lang="ts">
/**
 * 成员: the thread's participants (its lead and the project members that work or worked for it)
 * with role, runtime, node and state, “在 #12 忙” for a member now working in another thread;
 * stop while working here; the 全部放行 switch.
 */
import { computed } from "vue";
import { useQuestions, useRoles } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import { workingHere } from "@/lib/live";
import { openForUser } from "@/lib/present";
import { usesAsk } from "@/lib/roles";
import AutoApproveToggle from "./AutoApproveToggle.vue";
import MemberRow from "./MemberRow.vue";
import { useNow } from "./useNow";

const props = defineProps<{ thread: ThreadView }>();
const questions = useQuestions(() => props.thread.id);
const roles = useRoles();
const open = computed(() => openForUser(questions.data.value));
const now = useNow(() => props.thread.members.some(workingHere));
const askRoles = computed(() => usesAsk(roles.data.value?.roles ?? []));
const finished = computed(() => ["done", "failed", "archived"].includes(props.thread.status));
</script>

<template>
  <div class="flex flex-col gap-2">
    <AutoApproveToggle v-if="!finished && (thread.auto_approve || askRoles)" :thread="thread" />
    <MemberRow v-for="m in thread.members" :key="m.id" :thread="thread" :member="m" :asking="open.some((q) => q.asker === m.name)" :now="now" />
  </div>
</template>

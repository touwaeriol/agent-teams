<script setup lang="ts">
import { Settings } from "lucide-vue-next";
import { computed } from "vue";
import { RouterLink } from "vue-router";
import { useNodes } from "@/api/queries";
import type { UserView } from "@/api/views";
import Dot from "@/components/app/Dot.vue";
import Initial from "@/components/app/Initial.vue";
import { summarizeNodes } from "./nodes";

const props = defineProps<{ me: UserView }>();
const emit = defineEmits<{ navigate: [] }>();
const nodes = useNodes();
const s = computed(() => summarizeNodes(nodes.data.value ?? []));
const tone = computed(() => {
  const { total, online } = s.value;
  if (total === 0) return "#B7B3AA";
  if (online === total) return "#2E7D4F";
  return online > 0 ? "#C2570C" : "#B42318";
});
const name = computed(() => props.me.display_name || props.me.email.split("@")[0]);
</script>

<template>
  <div class="border-t p-3">
    <RouterLink to="/settings/nodes" class="flex items-center gap-2 px-2.5 pb-2.5 pt-1 text-xs text-muted-foreground hover:text-foreground" @click="emit('navigate')">
      <Dot :color="tone" />
      {{ s.total === 0 ? "还没有节点，去添加" : `节点 ${s.online} / ${s.total} 在线 · 槽位 ${s.active} / ${s.slots}` }}
    </RouterLink>
    <div class="flex items-center gap-2.5 px-1">
      <Initial :name="name" bg="#F4E3D7" fg="#A24E1E" :size="32" round />
      <span class="flex min-w-0 flex-1 flex-col">
        <span class="truncate text-[13px]">{{ name }}</span>
        <span class="text-[11px] text-muted-foreground">{{ me.role === "admin" ? "管理员" : "成员" }}</span>
      </span>
      <RouterLink to="/settings/team" aria-label="设置" class="flex size-8 items-center justify-center rounded-lg text-[#4A4843] hover:bg-accent" @click="emit('navigate')">
        <Settings class="size-4" :stroke-width="1.8" />
      </RouterLink>
    </div>
  </div>
</template>

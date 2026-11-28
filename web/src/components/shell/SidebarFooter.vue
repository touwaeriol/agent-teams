<script setup lang="ts">
import { $t } from "@/i18n";
import { Settings } from "lucide-vue-next";
import { computed } from "vue";
import { RouterLink } from "vue-router";
import { useNodes } from "@/api/queries";
import type { UserView } from "@/api/views";
import Dot from "@/components/app/Dot.vue";
import Initial from "@/components/app/Initial.vue";
import { runningText, summarizeNodes } from "./nodes";

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
      {{ s.total === 0 ? $t("还没有节点，去添加") : $t("节点 {online} / {total} 在线 · {running}", { online: s.online, total: s.total, running: runningText(s) }) }}
    </RouterLink>
    <div class="flex items-center gap-2.5 px-1">
      <Initial :name="name" bg="#F4E3D7" fg="#A24E1E" :size="32" round />
      <span class="flex min-w-0 flex-1 flex-col">
        <span class="truncate text-[13px]">{{ name }}</span>
        <span class="text-[11px] text-muted-foreground">{{ me.role === "admin" ? $t("管理员") : $t("成员") }}</span>
      </span>
      <RouterLink to="/settings/team" :aria-label="$t('设置')" class="flex size-8 items-center justify-center rounded-lg text-[#4A4843] hover:bg-accent" @click="emit('navigate')">
        <Settings class="size-4" :stroke-width="1.8" />
      </RouterLink>
    </div>
  </div>
</template>

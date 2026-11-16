<script setup lang="ts">
import { computed } from "vue";
import { RouterLink } from "vue-router";
import { useNodes } from "@/api/queries";
import Dot from "@/components/app/Dot.vue";
import Loading from "@/components/common/Loading.vue";
import { nodeSlots } from "@/components/shell/nodes";
import Heading from "./Heading.vue";
import Row from "./Row.vue";

const nodes = useNodes();
const list = computed(() => nodes.data.value ?? []);
</script>

<template>
  <div class="flex flex-col">
    <Heading>
      节点
      <template #action><RouterLink to="/settings/nodes" class="text-[13px] text-muted-foreground hover:text-foreground">管理节点</RouterLink></template>
    </Heading>
    <p class="-mt-1 pb-1 text-[13px] text-muted-foreground">员工在这些节点上运行，按岗位模板选择节点。</p>
    <Loading v-if="nodes.isLoading.value" />
    <p v-if="nodes.data.value && list.length === 0" class="text-[13px] text-muted-foreground">还没有节点。</p>
    <Row v-for="n in list" :key="n.id" :desc="`隔离：${n.isolation}`">
      <template #title>
        <span class="flex items-center gap-2"><Dot :color="n.online ? '#2E7D4F' : '#B7B3AA'" />{{ n.name }}</span>
      </template>
      <span class="text-[13px] tabular-nums text-muted-foreground">{{ n.online ? `会话 ${n.active_sessions} / ${nodeSlots(n)}` : "离线" }}</span>
    </Row>
  </div>
</template>

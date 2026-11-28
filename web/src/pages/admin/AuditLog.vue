<script setup lang="ts">
import { $t } from "@/i18n";
import { ref } from "vue";
import { useAudit } from "@/api/queries";
import Card from "@/components/app/Card.vue";
import CardTitle from "@/components/app/CardTitle.vue";
import { auditTime } from "./admin";

const open = ref(true);
const audit = useAudit();
</script>

<template>
  <Card class="gap-3">
    <CardTitle>
      {{ $t("审计日志") }}
      <template #action>
        <button type="button" class="text-[13px] text-muted-foreground hover:text-foreground" @click="open = !open">{{ open ? $t("收起") : $t("展开") }}</button>
      </template>
    </CardTitle>
    <template v-if="open">
      <div v-for="e in audit.data.value?.items ?? []" :key="e.id" class="flex flex-wrap gap-x-3 gap-y-0.5 border-t border-[#F1EFEA] pt-2 text-[13px] dark:border-border">
        <span class="font-mono text-xs text-muted-foreground">{{ auditTime(e.created_at) }}</span>
        <span>{{ e.actor }}</span>
        <span class="font-mono">{{ e.action }}</span>
        <span class="text-muted-foreground">{{ e.target_type }} {{ e.target_id }}</span>
      </div>
    </template>
  </Card>
</template>

<script setup lang="ts">
import { $t } from "@/i18n";
/** Skills this role loads (built-in design/docs and uploaded ones). `v-model` is the names. */
import { Check } from "lucide-vue-next";
import { computed } from "vue";
import { useSkills } from "@/api/queries";
import { toggleSkill } from "@/lib/roles";
import ToggleChip from "./ToggleChip.vue";

const value = defineModel<string[]>({ required: true });
const skills = useSkills();
const all = computed(() => skills.data.value?.skills ?? []);
const missing = computed(() => value.value.filter((name) => !all.value.some((s) => s.name === name)));
const toggle = (name: string) => (value.value = toggleSkill(value.value, name));
</script>

<template>
  <div class="flex flex-wrap items-center gap-1.5 border-t border-[#F1EFEA] pt-2.5 text-[13px]">
    <span class="mr-1 text-xs text-muted-foreground">{{ $t("技能") }}</span>
    <span v-for="s in all" :key="s.name" :title="s.description">
      <ToggleChip :on="value.includes(s.name)" @click="toggle(s.name)"><Check v-if="value.includes(s.name)" class="mr-0.5 inline size-3" />{{ s.name }}</ToggleChip>
    </span>
    <ToggleChip v-for="name in missing" :key="name" on @click="toggle(name)">{{ $t("{name}（已删除）", { name }) }}</ToggleChip>
    <span v-if="all.length === 0" class="text-xs text-muted-foreground">{{ $t("技能库是空的") }}</span>
  </div>
</template>

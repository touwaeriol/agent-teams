<script setup lang="ts">
// 内置技能 on the Admin page: shipped with the platform, or published by an admin (deletable).
import { $t } from "@/i18n";
import { computed } from "vue";
import { admin as adminActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useSkills } from "@/api/queries";
import type { SkillFile } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import FolderButton from "./FolderButton.vue";
import SkillCard from "./SkillCard.vue";
import ZipButton from "./ZipButton.vue";

const skills = useSkills();
const builtins = computed(() => (skills.data.value?.skills ?? []).filter((s) => s.builtin));
const upload = useAction((body: { zip?: string; files?: SkillFile[] }) => adminActions.createSkill(body), { invalidate: [keys.skills], success: $t("内置技能已上传") });
const remove = useAction((id: string) => adminActions.removeSkill(id), { invalidate: [keys.skills, keys.roles], success: $t("已删除") });
</script>

<template>
  <div class="flex flex-col gap-2 border-t border-[#F1EFEA] pt-3 dark:border-border">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h3 class="text-[13px] font-semibold text-muted-foreground">{{ $t("内置技能") }}</h3>
      <span class="flex gap-2">
        <ZipButton :label="$t('上传技能 zip')" :pending="upload.isPending.value" @zip="(zip: string) => upload.mutate({ zip })" />
        <FolderButton :pending="upload.isPending.value" @files="(files: SkillFile[]) => upload.mutate({ files })" />
      </span>
    </div>
    <div class="grid gap-2 sm:grid-cols-2">
      <SkillCard v-for="s in builtins" :key="s.id || s.name" :name="s.name" :badge="s.id ? $t('管理员发布') : $t('随平台发布')" :description="s.description">
        <ConfirmAction v-if="s.id" :icon-only="$t('删除内置技能 {name}', { name: s.name })" :title="$t('删除内置技能 {name}？', { name: s.name })" :description="$t('所有用户的岗位都会去掉这个技能。')" @confirm="remove.mutate(s.id)" />
      </SkillCard>
    </div>
  </div>
</template>

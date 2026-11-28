<script setup lang="ts">
// 技能库 on the Team page: built-in skills plus the user's uploads (a zip, or a picked folder of
// text files with SKILL.md). Roles tick the skills they load in RoleCard.
import { $t } from "@/i18n";
import { computed } from "vue";
import { skills as skillActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useSkills } from "@/api/queries";
import type { SkillFile } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import Loading from "@/components/common/Loading.vue";
import { formatBytes } from "@/lib/bytes";
import FolderButton from "./FolderButton.vue";
import LibrarySection from "./LibrarySection.vue";
import SkillCard from "./SkillCard.vue";
import ZipButton from "./ZipButton.vue";

const HELP = $t("技能是一份 SKILL.md 说明（可以附带参考文件），员工开工时按岗位加载。上传 zip 或选择文件夹，SKILL.md 开头需要 name 和 description；只收文本文件，单个技能最多 512 KB。");
const list = useSkills();
const skills = computed(() => list.data.value?.skills ?? []);
const invalidate = [keys.skills, keys.roles];
const upload = useAction((body: { zip?: string; files?: SkillFile[] }) => skillActions.create(body), { invalidate, success: $t("技能已上传") });
const remove = useAction((id: string) => skillActions.remove(id), { invalidate, success: $t("已删除") });
</script>

<template>
  <LibrarySection :title="$t('技能库')" :help="HELP">
    <template #actions>
      <ZipButton :label="$t('上传 zip')" :pending="upload.isPending.value" @zip="(zip: string) => upload.mutate({ zip })" />
      <FolderButton :pending="upload.isPending.value" @files="(files: SkillFile[]) => upload.mutate({ files })" />
    </template>
    <Loading v-if="list.isLoading.value" />
    <div class="grid gap-2 sm:grid-cols-2">
      <SkillCard v-for="s in skills" :key="s.id || s.name" :name="s.name" :badge="s.builtin ? $t('内置') : formatBytes(s.size)" :description="s.description">
        <ConfirmAction
          v-if="!s.builtin"
          :icon-only="$t('删除技能 {name}', { name: s.name })"
          :title="$t('删除技能 {name}？', { name: s.name })"
          :description="$t('用到它的岗位会去掉这个技能，已经开工的员工下次开会话时生效。')"
          @confirm="remove.mutate(s.id)"
        />
      </SkillCard>
    </div>
  </LibrarySection>
</template>

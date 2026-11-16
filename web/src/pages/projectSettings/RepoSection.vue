<script setup lang="ts">
/** 代码仓库: edits a draft of the list; 保存 appears once it differs from the project. */
import { computed, ref, watch } from "vue";
import type { ProjectView } from "@/api/views";
import RepoList from "@/components/app/repo/RepoList.vue";
import { reposPayload, type RepoRef } from "@/components/app/repo/repo";
import Heading from "./Heading.vue";
import { repoRefs } from "./logic";
import { useUpdate } from "./useUpdate";

const props = defineProps<{ project: ProjectView }>();
const saved = computed(() => JSON.stringify(repoRefs(props.project)));
const draft = ref<RepoRef[]>(JSON.parse(saved.value) as RepoRef[]);
watch(saved, (s) => (draft.value = JSON.parse(s) as RepoRef[]));
const update = useUpdate(() => props.project.id, "仓库设置已保存");
const dirty = computed(() => JSON.stringify(draft.value) !== saved.value);
</script>

<template>
  <div class="flex flex-col gap-2">
    <Heading>
      代码仓库
      <template #action>
        <el-button v-if="dirty" size="small" type="primary" :disabled="update.isPending.value" @click="update.mutate({ repos: reposPayload(draft) })">保存</el-button>
      </template>
    </Heading>
    <RepoList v-model="draft" :current-project="project.id" />
  </div>
</template>

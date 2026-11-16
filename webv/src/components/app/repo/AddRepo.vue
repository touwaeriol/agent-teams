<script setup lang="ts">
// The repo picker: which GitHub connection (when there are several) and a searchable list of its
// repositories ("已被 X 使用" / private lock); a pasted URL or typed owner/repo works too.
import { Lock } from "lucide-vue-next";
import { computed, ref } from "vue";
import { errorText } from "@/api/mutations";
import { useRepos } from "@/api/queries";
import type { GitConnectionView } from "@/api/views";
import Combobox from "@/components/common/Combobox.vue";
import Field from "@/components/common/Field.vue";
import { addRepo, connectionLabel, matchesRepo, repoOptions, type RepoRef } from "./repo";

const props = defineProps<{ connections: GitConnectionView[]; currentProject?: string }>();
const value = defineModel<RepoRef[]>({ required: true });
const connectionId = ref(props.connections[0]?.id ?? "");
const repos = useRepos(connectionId);
const owner = computed(() => props.connections.find((c) => c.id === connectionId.value)?.login ?? "");
const options = computed(() => repoOptions(repos.data.value?.items ?? [], value.value, props.currentProject));
const hint = computed(() => (repos.error.value ? errorText(repos.error.value) : `来自你连接的 GitHub（${owner.value}），输入即可搜索，也能粘贴链接；选中就加入列表。`));
const add = (repo: string) => (value.value = addRepo(value.value, { repo, git_connection_id: connectionId.value }));
</script>

<template>
  <div class="grid gap-3 sm:grid-cols-[2fr_3fr]">
    <Field v-if="connections.length > 1" v-slot="{ id }" label="GitHub 连接">
      <el-select :id="id" v-model="connectionId" placeholder="选择连接">
        <el-option v-for="c in connections" :key="c.id" :value="c.id" :label="connectionLabel(c)" />
      </el-select>
    </Field>
    <Field v-slot="{ id }" :label="value.length ? '再加一个仓库' : '添加仓库'" :hint="hint" :class="connections.length > 1 ? undefined : 'sm:col-span-2'">
      <!-- a fresh, empty picker after each added repository -->
      <Combobox
        :id="id"
        :key="value.length"
        :options="options"
        :filter="matchesRepo"
        :loading="repos.isLoading.value"
        placeholder="搜索仓库"
        empty="没有匹配的仓库"
        allow-custom
        class="at-combobox--mono"
        @pick="add"
      >
        <template #option="{ option }"><span class="font-mono text-[13px]">{{ option.value }}</span></template>
        <template #hint="{ option }">
          <template v-if="option.usedBy">{{ option.usedBy }}</template>
          <Lock v-else-if="option.private" class="size-3.5" aria-label="私有" />
        </template>
      </Combobox>
    </Field>
  </div>
</template>

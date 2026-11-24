<script setup lang="ts">
// 新建 (a session), like Claude Code's new session: a clean page with one line in the middle and the
// box at the bottom. In the box: what to do and, folded into a chip, the repositories (each from a
// branch) and the work branch's name. Under it one row (NewSessionOptions): node (自动选择 or one of
// the user's nodes), role (a template only) and model, with room for more controls at its end.
// Enter sends (Shift+Enter: new line). The choices are remembered for the next session.
import { ChevronDown, FolderGit2 } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { sessions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useNodes, useRoles } from "@/api/queries";
import SendButton from "@/components/app/SendButton.vue";
import NewSessionOptions from "@/components/app/session/NewSessionOptions.vue";
import NewSessionControls from "@/components/app/session/controls/NewSessionControls.vue";
import SessionRepos from "@/components/app/session/SessionRepos.vue";
import { DEFAULTS_KEY, parseDefaults, reposLabel, sessionReposPayload, usableNode, type SessionRepo } from "@/components/app/session/session";
import Field from "@/components/common/Field.vue";
import { MODEL_FORMAT_HINT, validModel, type Runtime } from "@/lib/models";
import { runtimeSpec } from "@/lib/runtimes";
import { readLocal, writeLocal } from "@/lib/persist";
import { toast } from "@/lib/toast";

const router = useRouter();
const roles = useRoles();
const nodes = useNodes();
/** "auto" = picked by the platform (el-select shows "" as nothing picked). */
const AUTO = "auto";
const saved = parseDefaults(readLocal(DEFAULTS_KEY));
const message = ref("");
const repos = ref<SessionRepo[]>(saved?.repos ?? []);
const role = ref(saved?.role ?? "");
const model = ref("");
const node = ref(saved?.node || AUTO);
const branch = ref("");
/** Permission mode and effort ("" = the session's default; NewSessionControls). */
const mode = ref("");
const effort = ref("");
const reposOpen = ref(false);

const members = computed(() => (roles.data.value?.roles ?? []).filter((r) => r.tier === "member"));
watch(
  members,
  (list) => {
    if (list.length && !list.some((r) => r.role === role.value)) role.value = list[0].role;
  },
  { immediate: true },
);
const picked = computed(() => members.value.find((r) => r.role === role.value));
const runtime = computed<Runtime>(() => runtimeSpec(picked.value?.runtime).id);
const nodeList = computed(() => nodes.data.value ?? []);
const ready = computed(() => message.value.trim() !== "" && repos.value.length > 0 && role.value !== "");

const create = useAction(
  () =>
    sessions.create({
      message: message.value.trim(),
      repos: sessionReposPayload(repos.value),
      role: role.value,
      model: model.value.trim() || undefined,
      permission_mode: (mode.value || undefined) as never,
      effort: effort.value || undefined,
      node_id: node.value === AUTO ? undefined : node.value,
      branch: branch.value.trim() || undefined,
    }),
  {
    invalidate: [keys.projects],
    onSuccess: (s) => {
      writeLocal(DEFAULTS_KEY, JSON.stringify({ repos: repos.value, node: node.value, role: role.value }));
      void router.push(`/sessions/${s.project.id}`);
    },
  },
);
function submit() {
  if (!message.value.trim() || create.isPending.value) return;
  if (repos.value.length === 0) {
    reposOpen.value = true;
    return void toast.error("先选要在哪些仓库里干活");
  }
  if (!role.value) return void toast.error("团队里还没有成员岗位，先在 设置 → 团队 里添加一个");
  if (!validModel(model.value)) return void toast.error(MODEL_FORMAT_HINT);
  create.mutate();
}
/** Enter sends; Shift+Enter and Enter while an input method is composing (Chinese) do not. */
function onKey(ev: Event | KeyboardEvent) {
  const e = ev as KeyboardEvent;
  if (e.key !== "Enter" || e.shiftKey || e.isComposing) return;
  e.preventDefault();
  submit();
}
// A remembered (or picked) node that was deleted or is offline falls back to 自动选择.
watch(
  nodeList,
  (list) => {
    if (nodes.data.value) node.value = usableNode(node.value, list, AUTO);
  },
  { immediate: true },
);
const reposText = computed(() => (branch.value.trim() ? `${reposLabel(repos.value)} · ${branch.value.trim()}` : reposLabel(repos.value)));
</script>

<template>
  <div class="flex h-full min-h-0 flex-col bg-panel">
    <div class="flex flex-1 items-center justify-center px-6 text-center">
      <p class="text-[15px] text-muted-foreground" data-testid="new-session-hint">要做点什么？员工会在你选的仓库里直接动手。</p>
    </div>
    <form class="mx-auto w-full max-w-[780px] shrink-0 px-4 pb-6" data-testid="new-session" @submit.prevent="submit">
      <div class="flex flex-col rounded-2xl border bg-background px-3 pb-2 pt-3 shadow-sm focus-within:border-foreground/30">
        <el-input
          v-model="message"
          type="textarea"
          :autosize="{ minRows: 2, maxRows: 10 }"
          resize="none"
          autofocus
          placeholder="描述任务或提出问题"
          aria-label="要做什么"
          class="at-bare-textarea px-1"
          @keydown="onKey"
        />
        <div class="mt-1 flex items-center gap-2">
          <button
            type="button"
            class="inline-flex h-7 min-w-0 max-w-[60%] items-center gap-1.5 rounded-md px-2 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
            data-testid="repos-chip"
            :title="`仓库：${reposText}`"
            @click="reposOpen = true"
          >
            <FolderGit2 class="size-3.5 shrink-0" />
            <span class="truncate">{{ reposText }}</span>
            <ChevronDown class="size-3 shrink-0" />
          </button>
          <SendButton label="开始会话" class="ml-auto" :busy="create.isPending.value" :disabled="!ready" />
        </div>
      </div>
      <NewSessionOptions v-model:node="node" v-model:role="role" v-model:model="model" :nodes="nodeList" :roles="members" :runtime="runtime" :auto="AUTO" class="mt-1.5 px-1">
        <NewSessionControls v-model:mode="mode" v-model:effort="effort" :role="role" :model="model" />
      </NewSessionOptions>
    </form>
    <el-dialog v-model="reposOpen" title="仓库" width="600px" class="at-dialog w-[calc(100%-2rem)] max-w-[600px]" append-to-body>
      <div class="flex flex-col gap-4">
        <p class="text-[13px] leading-5 text-muted-foreground">每个仓库是工作目录下的一个子文件夹，员工从选的分支切出自己的会话分支（session/…），改完推上去、开 PR。会话里随时可以增删。</p>
        <SessionRepos v-model="repos" />
        <Field v-slot="{ id }" label="工作分支名（可选）" hint="不填就取标题里的英文，标题全是中文时用仓库名">
          <el-input :id="id" v-model="branch" maxlength="60" placeholder="例如：fix-login-redirect" />
        </Field>
      </div>
      <template #footer><el-button type="primary" @click="reposOpen = false">完成</el-button></template>
    </el-dialog>
  </div>
</template>

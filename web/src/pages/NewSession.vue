<script setup lang="ts">
// 新建 (a session), like Claude Code's new session: an empty page with one box at the bottom for
// what to do; under it, chips for where (repositories, each from a branch) and who (role, model)
// on which node. Enter sends (Shift+Enter: new line). The chips remember the last choice.
import { Bot, ChevronDown, FolderGit2, Server } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { sessions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useNodes, useRoles } from "@/api/queries";
import ModelPicker from "@/components/app/models/ModelPicker.vue";
import SendButton from "@/components/app/SendButton.vue";
import SessionRepos from "@/components/app/session/SessionRepos.vue";
import { DEFAULTS_KEY, parseDefaults, reposLabel, sessionReposPayload, type SessionRepo } from "@/components/app/session/session";
import Field from "@/components/common/Field.vue";
import { MODEL_FORMAT_HINT, validModel, type Runtime } from "@/lib/models";
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
const reposOpen = ref(false);
const whoOpen = ref(false);

const members = computed(() => (roles.data.value?.roles ?? []).filter((r) => r.tier === "member"));
watch(
  members,
  (list) => {
    if (list.length && !list.some((r) => r.role === role.value)) role.value = list[0].role;
  },
  { immediate: true },
);
const picked = computed(() => members.value.find((r) => r.role === role.value));
const runtime = computed<Runtime>(() => (picked.value?.runtime === "codex" ? "codex" : "claude"));
const nodeList = computed(() => nodes.data.value ?? []);
const nodeLabel = computed(() => (node.value === AUTO ? "自动分配节点" : (nodeList.value.find((n) => n.id === node.value)?.name ?? "自动分配节点")));
const whoLabel = computed(() => `${picked.value?.title || picked.value?.role || "岗位"}${model.value.trim() ? ` · ${model.value.trim()}` : ""}`);
const ready = computed(() => message.value.trim() !== "" && repos.value.length > 0 && role.value !== "");

const create = useAction(
  () =>
    sessions.create({
      message: message.value.trim(),
      repos: sessionReposPayload(repos.value),
      role: role.value,
      model: model.value.trim() || undefined,
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
const chip = "inline-flex h-7 max-w-[260px] items-center gap-1.5 rounded-md px-2 text-xs text-muted-foreground hover:bg-accent hover:text-foreground";
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <header class="flex h-12 shrink-0 items-center px-5 text-[13px] text-muted-foreground">新会话</header>
    <div class="flex flex-1 items-center justify-center px-6 text-center text-[13px] leading-6 text-muted-foreground">
      <p class="max-w-md">选好仓库，说要做什么。仓库会并排拉到同一个文件夹里，一个员工直接在里面干活，你直接和它对话。</p>
    </div>
    <form class="mx-auto w-full max-w-[780px] shrink-0 px-4 pb-5" data-testid="new-session" @submit.prevent="submit">
      <div class="flex items-end gap-2 rounded-2xl border bg-background px-4 py-3 shadow-sm focus-within:border-foreground/30">
        <el-input
          v-model="message"
          type="textarea"
          :autosize="{ minRows: 1, maxRows: 10 }"
          resize="none"
          autofocus
          placeholder="描述任务或提出问题"
          aria-label="要做什么"
          class="at-bare-textarea flex-1"
          @keydown="onKey"
        />
        <SendButton label="开始会话" :busy="create.isPending.value" :disabled="!ready" />
      </div>
      <div class="mt-1.5 flex flex-wrap items-center gap-1 px-1">
        <button type="button" :class="chip" data-testid="repos-chip" @click="reposOpen = true">
          <FolderGit2 class="size-3.5 shrink-0" />
          <span class="truncate">{{ reposLabel(repos) }}</span>
          <ChevronDown class="size-3 shrink-0" />
        </button>
        <el-dropdown trigger="click" placement="top-start" @command="(v: string) => (node = v)">
          <button type="button" :class="chip" data-testid="node-chip">
            <Server class="size-3.5 shrink-0" />
            <span class="truncate">{{ nodeLabel }}</span>
            <ChevronDown class="size-3 shrink-0" />
          </button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item :command="AUTO">自动分配（剩余用量最多的在线节点）</el-dropdown-item>
              <el-dropdown-item v-for="n in nodeList" :key="n.id" :command="n.id" :disabled="!n.online">{{ n.online ? n.name : `${n.name}（离线）` }}</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        <button type="button" :class="chip" data-testid="who-chip" @click="whoOpen = true">
          <Bot class="size-3.5 shrink-0" />
          <span class="truncate">{{ whoLabel }}</span>
          <ChevronDown class="size-3 shrink-0" />
        </button>
      </div>
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
    <el-dialog v-model="whoOpen" title="谁来做" width="420px" class="at-dialog w-[calc(100%-2rem)] max-w-[420px]" append-to-body>
      <div class="flex flex-col gap-4">
        <Field v-slot="{ id }" label="岗位" hint="按这个成员岗位的配置起员工">
          <el-select :id="id" v-model="role" placeholder="选择岗位">
            <el-option v-for="r in members" :key="r.role" :value="r.role" :label="r.title || r.role" />
          </el-select>
        </Field>
        <Field v-slot="{ id }" label="模型">
          <ModelPicker :id="id" v-model="model" live :runtime="runtime" label="模型" placeholder="跟随岗位配置" />
        </Field>
      </div>
      <template #footer><el-button type="primary" @click="whoOpen = false">完成</el-button></template>
    </el-dialog>
  </div>
</template>

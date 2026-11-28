<script setup lang="ts">
// 新建 (a session), like Claude Code's new session: a clean page with one line in the middle and the
// box at the bottom. In the box: what to do and, folded into a chip, the repositories (each from a
// branch) and the work branch's name. Under it one row (NewSessionOptions): node (自动选择 or one of
// the user's nodes), role (a template only), agent (one of the role's runtimes) and model, then
// effort and permission mode. Node, role and agent are fixed once the session starts.
// Enter sends (Shift+Enter: new line). The choices are remembered for the next session.
import { $t } from "@/i18n";
import { ChevronDown, FolderGit2 } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { sessions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useNodes, useRoles } from "@/api/queries";
import AttachButton from "@/components/app/AttachButton.vue";
import AttachmentStrip from "@/components/app/AttachmentStrip.vue";
import { attachmentHandlers, directUploader, useAttachments } from "@/components/app/attachments";
import { attachmentsField } from "@/lib/images";
import SendButton from "@/components/app/SendButton.vue";
import { standaloneUploads } from "@/lib/upload";
import NewSessionOptions from "@/components/app/session/NewSessionOptions.vue";
import NewSessionControls from "@/components/app/session/controls/NewSessionControls.vue";
import SessionRepos from "@/components/app/session/SessionRepos.vue";
import { DEFAULTS_KEY, nodeAgentProblem, parseDefaults, reposLabel, sessionReposPayload, usableNode, type SessionRepo } from "@/components/app/session/session";
import { runtimeLabel } from "@/lib/runtimes";
import Field from "@/components/common/Field.vue";
import { MODEL_FORMAT_HINT, runtimesOf, validModel } from "@/lib/models";
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
// Files for the first message: uploaded before the session (and its project) exists through the
// project-less POST /api/uploads; sessions.create moves them into the new project. No base64
// fallback here (that one needs a project).
const files = useAttachments("", directUploader(() => standaloneUploads(), { legacy: false }));
const dragging = ref(false);
const dropHandlers = attachmentHandlers(files, (on) => (dragging.value = on));

const members = computed(() => (roles.data.value?.roles ?? []).filter((r) => r.tier === "member"));
watch(
  members,
  (list) => {
    if (list.length && !list.some((r) => r.role === role.value)) role.value = list[0].role;
  },
  { immediate: true },
);
const picked = computed(() => members.value.find((r) => r.role === role.value));
/** The agent (CLI): one of the picked role's runtimes, its first by default; fixed once the session starts. */
const agent = ref(saved?.agent ?? "");
watch(
  picked,
  (r) => {
    if (!r) return;
    const list = runtimesOf(r);
    if (!list.includes(agent.value)) agent.value = list[0];
  },
  { immediate: true },
);
// A model typed for one agent means nothing to another.
watch(agent, (now, before) => before && now !== before && (model.value = ""));
const nodeList = computed(() => nodes.data.value ?? []);
// Repositories are optional: without any the employee works in a local folder on its node.
const ready = computed(() => message.value.trim() !== "" && role.value !== "" && !files.uploading.value);

const create = useAction(
  () =>
    sessions.create({
      message: message.value.trim(),
      repos: sessionReposPayload(repos.value),
      role: role.value,
      runtime: agent.value || undefined,
      model: model.value.trim() || undefined,
      permission_mode: (mode.value || undefined) as never,
      effort: effort.value || undefined,
      node_id: node.value === AUTO ? undefined : node.value,
      branch: branch.value.trim() || undefined,
      // SessionInput.attachments (agreed with the server, q10): only sent with files, so a session
      // without attachments posts exactly what it did before. Typed once the schema has it.
      ...attachmentsField(files.ids()),
    }),
  {
    invalidate: [keys.projects],
    onSuccess: (s) => {
      files.discard(files.take());
      writeLocal(DEFAULTS_KEY, JSON.stringify({ repos: repos.value, node: node.value, role: role.value, agent: agent.value }));
      void router.push(`/sessions/${s.project.id}`);
    },
  },
);
function submit() {
  if (!message.value.trim() || create.isPending.value) return;
  if (files.uploading.value) return void toast.warning($t("附件还在上传，传完再发送"));
  if (files.items.value.some((x) => x.error)) return void toast.error($t("有附件没有传上去，先移除它再发送"));
  if (!role.value) return void toast.error($t("团队里还没有成员岗位，先在 设置 → 团队 里添加一个"));
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
// A remembered (or picked) node that was deleted, is offline or cannot run the picked agent falls back to 自动选择.
watch(
  [nodeList, agent],
  ([list, rt]) => {
    if (!nodes.data.value) return;
    node.value = usableNode(node.value, list, AUTO);
    const n = list.find((x) => x.id === node.value);
    if (n && nodeAgentProblem(n, rt, runtimeLabel)) node.value = AUTO;
  },
  { immediate: true },
);
const reposText = computed(() => (branch.value.trim() ? `${reposLabel(repos.value)} · ${branch.value.trim()}` : reposLabel(repos.value)));
</script>

<template>
  <div class="flex h-full min-h-0 flex-col bg-panel">
    <div class="flex flex-1 items-center justify-center px-6 text-center">
      <p class="text-[15px] text-muted-foreground" data-testid="new-session-hint">{{ $t("要做点什么？可以选仓库，也可以不选，员工直接在节点上的本地文件夹里干。") }}</p>
    </div>
    <form class="mx-auto w-full max-w-[780px] shrink-0 px-4 pb-6" data-testid="new-session" @submit.prevent="submit">
      <div
        :class="['flex flex-col rounded-2xl border bg-background px-3 pb-2 pt-3 shadow-sm focus-within:border-foreground/30', dragging && 'border-primary']"
        v-on="dropHandlers"
      >
        <AttachmentStrip :a="files" class="px-1 pb-2" />
        <el-input
          v-model="message"
          type="textarea"
          :autosize="{ minRows: 2, maxRows: 10 }"
          resize="none"
          autofocus
          :placeholder="$t('描述任务或提出问题')"
          :aria-label="$t('要做什么')"
          class="at-bare-textarea px-1"
          @keydown="onKey"
        />
        <div class="mt-1 flex items-center gap-2">
          <AttachButton :a="files" :size="28" />
          <button
            type="button"
            class="inline-flex h-7 min-w-0 max-w-[60%] items-center gap-1.5 rounded-md px-2 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
            data-testid="repos-chip"
            :title="$t('仓库：{reposText}', { reposText })"
            @click="reposOpen = true"
          >
            <FolderGit2 class="size-3.5 shrink-0" />
            <span class="truncate">{{ reposText }}</span>
            <ChevronDown class="size-3 shrink-0" />
          </button>
          <SendButton :label="$t('发送')" class="ml-auto" :busy="create.isPending.value" :disabled="!ready" />
        </div>
      </div>
      <NewSessionOptions v-model:node="node" v-model:role="role" v-model:agent="agent" v-model:model="model" :nodes="nodeList" :roles="members" :auto="AUTO" class="mt-1.5 px-1">
        <NewSessionControls v-model:mode="mode" v-model:effort="effort" :role="role" :agent="agent" :model="model" />
      </NewSessionOptions>
    </form>
    <el-dialog v-model="reposOpen" :title="$t('仓库')" width="600px" class="at-dialog w-[calc(100%-2rem)] max-w-[600px]" append-to-body>
      <div class="flex flex-col gap-4">
        <p class="text-[13px] leading-5 text-muted-foreground">{{ $t("每个仓库是工作目录下的一个子文件夹，员工从选的分支切出自己的会话分支（session/…），改完推上去、开 PR。会话里随时可以增删。") }}</p>
        <p class="text-[13px] leading-5 text-muted-foreground" data-testid="repos-optional">{{ $t("仓库是可选的：一个都不选时，员工在节点上为这个会话建的本地文件夹里干活，文件只在那台机器上，会话固定在那台节点，点“已解决”后文件夹也保留，删除会话时才清理。") }}</p>
        <SessionRepos v-model="repos" />
        <Field v-slot="{ id }" :label="$t('工作分支名（可选）')" :hint="$t('不填就取标题里的英文，标题全是中文时用仓库名')">
          <el-input :id="id" v-model="branch" maxlength="60" :placeholder="$t('例如：fix-login-redirect')" />
        </Field>
      </div>
      <template #footer><el-button type="primary" @click="reposOpen = false">{{ $t("完成") }}</el-button></template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
/** 添加仓库 / 编辑仓库 (docs/v2/git-repos.md §11.2). Paste a URL → transport, host and provider are
 * inferred; pick a credential of that host or create one (token, pasted key, or a key pair the
 * platform generates so the browser only ever sees the public key); 测试连接 before saving.
 * Editing changes the display name and the credential (the URL is the repository's identity). */
import { computed, ref, watch } from "vue";
import { gitCredentials, gitRepos } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { GitCredentialInput, GitCredentialView, GitRepoView, GitTestResult } from "@/api/views";
import Callout from "@/components/common/Callout.vue";
import CopyBlock from "@/components/common/CopyBlock.vue";
import Field from "@/components/common/Field.vue";
import FormDialog from "@/components/common/FormDialog.vue";
import Spinner from "@/components/common/Spinner.vue";
import { credentialsFor, parseRepoURL, PROVIDER_LABEL, repoTitle } from "@/lib/gitRepos";
import TestResult from "./TestResult.vue";

type Auth = "https_token" | "ssh_key" | "public";

const open = defineModel<boolean>("open", { required: true });
const props = defineProps<{ repo?: GitRepoView | null; credentials: GitCredentialView[] }>();

const url = ref("");
const name = ref("");
const auth = ref<Auth>("https_token");
const credId = ref("new");
const credName = ref("");
const username = ref("");
const token = ref("");
const expires = ref<Date | null>(null);
const keySource = ref<"generate" | "paste">("generate");
const privateKey = ref("");
const passphrase = ref("");
const generated = ref<GitCredentialView | null>(null);
const result = ref<GitTestResult | null>(null);
const editing = computed(() => props.repo ?? null);

watch(
  open,
  (v) => {
    if (!v) return;
    const r = editing.value;
    url.value = r?.url ?? "";
    name.value = r?.name ?? "";
    auth.value = r ? (r.credential_id ? (r.transport === "ssh" ? "ssh_key" : "https_token") : "public") : "https_token";
    credId.value = r?.credential_id ?? "new";
    credName.value = token.value = privateKey.value = passphrase.value = "";
    expires.value = null;
    keySource.value = "generate";
    generated.value = null;
    result.value = null;
  },
  { immediate: true },
);

const parsed = computed(() => parseRepoURL(url.value));
watch(
  () => [parsed.value.transport, parsed.value.username] as const,
  ([transport, user]) => {
    if (!transport || editing.value) return;
    if (auth.value !== "public") auth.value = transport === "ssh" ? "ssh_key" : "https_token";
    username.value = user ?? "";
  },
);
const sameHost = computed(() => credentialsFor(props.credentials, parsed.value.host, parsed.value.transport));
watch(sameHost, (list) => {
  if (credId.value === "new" && list.length && !editing.value && !generated.value) credId.value = list[0].id;
});
const creating = computed(() => auth.value !== "public" && credId.value === "new");
const summary = computed(() => {
  const p = parsed.value;
  return p.ok ? `${p.transport === "ssh" ? "SSH" : "HTTPS"} · ${p.host} · ${p.path} · 识别为 ${PROVIDER_LABEL[p.provider ?? "generic"]}` : "";
});

/** The unsaved credential typed in the form (a generated key is saved at once, so never here). */
function draftCredential(): GitCredentialInput | undefined {
  if (!creating.value) return undefined;
  const base = { name: credName.value.trim() || undefined, username: username.value.trim() || undefined, kind: auth.value as "https_token" | "ssh_key" };
  if (auth.value === "https_token") return { ...base, token: token.value.trim(), expires_at: expires.value?.toISOString() };
  return { ...base, private_key: privateKey.value, passphrase: passphrase.value || undefined };
}
const credentialID = () => (auth.value === "public" || credId.value === "new" ? undefined : credId.value);

const problem = computed(() => {
  if (!parsed.value.ok) return parsed.value.problem ?? "请填仓库地址";
  if (!creating.value) return "";
  if (auth.value === "https_token" && !token.value.trim()) return "请填令牌";
  if (auth.value === "ssh_key" && keySource.value === "paste" && !privateKey.value.trim()) return "请粘贴私钥";
  if (auth.value === "ssh_key" && keySource.value === "generate") return "请先生成密钥对";
  return "";
});

const generate = useAction(
  () =>
    gitCredentials.create({ name: credName.value.trim() || `${parsed.value.path?.split("/").pop()} 部署密钥`, host: parsed.value.host, kind: "ssh_key", username: username.value.trim() || "git", generate: true }),
  {
    invalidate: [keys.gitCredentials],
    onSuccess: (c) => {
      generated.value = c;
      credId.value = c.id;
    },
  },
);
const test = useAction(
  () => (editing.value && !creating.value && credId.value === (editing.value.credential_id ?? "new") ? gitRepos.test(editing.value.id) : gitRepos.testDraft({ url: url.value.trim(), credential_id: credentialID(), credential: draftCredential() })),
  { onSuccess: (r) => (result.value = r) },
);
const save = useAction(
  async () => {
    const r = editing.value;
    if (r) return gitRepos.update(r.id, { name: name.value.trim(), credential_id: credentialID() ?? "" });
    return gitRepos.create({ url: url.value.trim(), name: name.value.trim() || undefined, credential_id: credentialID(), credential: draftCredential() });
  },
  {
    invalidate: [keys.gitRepos, keys.gitCredentials],
    success: () => (editing.value ? "已保存" : "仓库已添加"),
    onSuccess: () => (open.value = false),
  },
);
const touched = ref(false);
watch(open, () => (touched.value = false));
function submit() {
  touched.value = true;
  if (!problem.value && !(editing.value && creating.value)) save.mutate();
}
</script>

<template>
  <FormDialog
    v-model:open="open"
    :title="editing ? `编辑仓库 ${repoTitle(editing)}` : '添加仓库'"
    description="凭据加密保存在平台上。员工用到时平台把它交给节点，只留在内存里，只有这个项目员工会话里的 git 能用，不写到磁盘或配置文件。保存后凭据不能再查看，只能替换。"
    :submit-label="editing ? '保存' : '添加'"
    :busy="save.isPending.value"
    width="38rem"
    @submit="submit"
  >
    <Field v-slot="{ id }" label="仓库地址" :hint="editing ? '地址是仓库的身份，不能修改；要换地址请删除后重新添加' : '粘贴克隆地址：https://… 或 git@…:…'">
      <el-input :id="id" v-model="url" class="at-mono" placeholder="https://gitlab.com/group/project.git" :disabled="!!editing" />
    </Field>
    <p v-if="parsed.ok && !editing" class="-mt-2 text-xs text-muted-foreground">{{ summary }}</p>
    <p v-else-if="parsed.problem && url" class="-mt-2 text-xs text-bad">{{ parsed.problem }}</p>
    <Field v-slot="{ id }" label="显示名称（可选）" hint="不填就用仓库名；员工和工具里用的是 主机/路径">
      <el-input :id="id" v-model="name" :placeholder="parsed.path?.split('/').pop() ?? ''" />
    </Field>
    <Field label="认证方式">
      <el-radio-group v-model="auth" aria-label="认证方式">
        <el-radio-button value="https_token" :disabled="parsed.transport === 'ssh'">HTTPS 令牌</el-radio-button>
        <el-radio-button value="ssh_key" :disabled="parsed.transport === 'https'">SSH 私钥</el-radio-button>
        <el-radio-button value="public">公开仓库（只读）</el-radio-button>
      </el-radio-group>
    </Field>
    <template v-if="auth !== 'public'">
      <Field v-slot="{ id }" label="凭据" :hint="`同一主机（${parsed.host ?? '…'}）的仓库可以共用一条凭据`">
        <el-select :id="id" v-model="credId">
          <el-option v-for="c in sameHost" :key="c.id" :value="c.id" :label="`${c.name}（${c.repos} 个仓库在用）`" />
          <el-option value="new" :label="editing ? '新建凭据（请到下方“凭据”里添加后再选）' : '新建凭据…'" :disabled="!!editing" />
        </el-select>
      </Field>
      <div v-if="creating && !editing" class="flex flex-col gap-4 rounded-xl border bg-well/40 p-4">
        <Field v-slot="{ id }" label="凭据名称（可选）" hint="不填就用主机名">
          <el-input :id="id" v-model="credName" :placeholder="auth === 'ssh_key' ? 'deploy 部署密钥' : 'GitLab 个人令牌'" />
        </Field>
        <template v-if="auth === 'https_token'">
          <div class="grid gap-3 sm:grid-cols-[10rem_1fr]">
            <Field v-slot="{ id }" label="用户名" hint="GitLab 填 oauth2">
              <el-input :id="id" v-model="username" class="at-mono" />
            </Field>
            <Field v-slot="{ id }" label="令牌" hint="需要读写仓库的权限（GitLab：read_repository + write_repository）">
              <el-input :id="id" v-model="token" type="password" show-password autocomplete="new-password" placeholder="glpat-…" />
            </Field>
          </div>
          <Field v-slot="{ id }" label="到期日（可选）" hint="填了会在到期前 14 天提醒你换">
            <el-date-picker :id="id" v-model="expires" type="date" placeholder="选择日期" class="!w-48" />
          </Field>
        </template>
        <template v-else>
          <Field label="私钥来源">
            <el-radio-group v-model="keySource" aria-label="私钥来源">
              <el-radio-button value="generate">平台生成密钥对（推荐）</el-radio-button>
              <el-radio-button value="paste">粘贴已有私钥</el-radio-button>
            </el-radio-group>
          </Field>
          <el-button v-if="keySource === 'generate'" class="self-start" :disabled="!parsed.ok || generate.isPending.value" @click="generate.mutate()">
            <Spinner v-if="generate.isPending.value" /> 生成密钥对
          </el-button>
          <template v-else>
            <Field v-slot="{ id }" label="私钥" hint="OpenSSH 或 PEM 格式（-----BEGIN … PRIVATE KEY-----）">
              <el-input :id="id" v-model="privateKey" type="textarea" :rows="4" class="at-mono" spellcheck="false" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" />
            </Field>
            <Field v-slot="{ id }" label="私钥口令（可选）">
              <el-input :id="id" v-model="passphrase" type="password" show-password autocomplete="new-password" />
            </Field>
          </template>
        </template>
      </div>
      <template v-if="generated && credId === generated.id">
        <Field label="公钥" hint="私钥由平台生成后直接加密保存，你和员工都看不到它">
          <CopyBlock :text="generated.public_key" />
        </Field>
        <Callout tone="info">把上面的公钥添加为这个仓库的<strong>部署密钥</strong>，并勾选“允许写入”，然后点“测试连接”。</Callout>
      </template>
    </template>
    <Callout tone="warn">
      单独添加的仓库没有 PR 和 CI：讨论串的改动留在讨论串分支上，完成即交付，合并由你在仓库平台上做。要用 PR 和 CI，请用 GitHub App 连接。
    </Callout>
    <p v-if="touched && problem" class="text-[13px] text-bad" role="alert">{{ problem }}</p>
    <TestResult v-if="result" :result="result" />
    <el-button class="self-start" :disabled="!parsed.ok || test.isPending.value || (creating && keySource === 'generate' && auth === 'ssh_key')" @click="test.mutate()">
      <Spinner v-if="test.isPending.value" /> 测试连接
    </el-button>
  </FormDialog>
</template>

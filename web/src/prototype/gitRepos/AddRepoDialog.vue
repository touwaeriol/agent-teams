<script setup lang="ts">
/** Prototype (docs/v2/git-repos.md §11.2): 添加仓库. Paste a URL → transport / host / provider are
 * inferred; pick an existing credential for that host or create one (token, pasted key, or a key
 * pair the platform generates so the browser only ever sees the public key); 测试连接 before saving. */
import { CircleCheck } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import Callout from "@/components/common/Callout.vue";
import CopyBlock from "@/components/common/CopyBlock.vue";
import Field from "@/components/common/Field.vue";
import FormDialog from "@/components/common/FormDialog.vue";
import { credentials, parseUrl, PROVIDER_LABEL } from "./fixtures";

type Auth = "https_token" | "ssh_key" | "public";
type KeySource = "generate" | "paste";

const open = defineModel<boolean>("open", { required: true });
const props = defineProps<{ preset?: "https" | "ssh"; tested?: boolean }>();

const PRESETS = {
  https: { url: "https://gitlab.com/prophey/sup2api.git", name: "sup2api" },
  ssh: { url: "ssh://git@git.prophey.ai:2222/infra/deploy.git", name: "" },
};
const url = ref("");
const name = ref("");
const auth = ref<Auth>("https_token");
const credId = ref("new");
const credName = ref("");
const username = ref("");
const token = ref("");
const expires = ref("");
const keySource = ref<KeySource>("generate");
const generated = ref(false);
const privateKey = ref("");
const showResult = ref(false);

watch(
  open,
  (v) => {
    if (!v) return;
    const p = PRESETS[props.preset ?? "https"];
    url.value = p.url;
    name.value = p.name;
    generated.value = props.preset === "ssh";
    showResult.value = !!props.tested;
    credId.value = props.preset === "ssh" ? "new" : "gc_1";
  },
  { immediate: true },
);

const parsed = computed(() => parseUrl(url.value));
watch(
  parsed,
  (p) => {
    if (!p.ok) return;
    auth.value = p.transport === "ssh" ? "ssh_key" : "https_token";
    username.value = p.username ?? "";
  },
  { immediate: true },
);
const sameHost = computed(() => credentials.filter((c) => c.host === parsed.value.host && c.kind === auth.value));
const creating = computed(() => auth.value !== "public" && (credId.value === "new" || !sameHost.value.some((c) => c.id === credId.value)));
const PUBLIC_KEY = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7pX2c9vQe4nWm1tYb8sLr3uJd6hA0fGzP5kE2oNiQx agent-teams";
const summary = computed(() => {
  const p = parsed.value;
  return p.ok ? `${p.transport === "ssh" ? "SSH" : "HTTPS"} · ${p.host} · ${p.path} · 识别为 ${PROVIDER_LABEL[p.provider ?? "generic"]}` : "";
});
</script>

<template>
  <FormDialog
    v-model:open="open"
    title="添加仓库"
    description="凭据只保存在平台上，节点和员工都拿不到：节点上的 git 经平台中转读写，每次只带 1 小时有效的中转令牌。保存后凭据不能再查看，只能替换。"
    submit-label="添加"
    width="38rem"
  >
    <Field v-slot="{ id }" label="仓库地址" hint="粘贴克隆地址：https://… 或 git@…:…">
      <el-input :id="id" v-model="url" class="at-mono" placeholder="https://gitlab.com/group/project.git" />
    </Field>
    <p v-if="parsed.ok" class="-mt-2 text-xs text-muted-foreground">{{ summary }}</p>
    <p v-else-if="parsed.problem" class="-mt-2 text-xs text-bad">{{ parsed.problem }}</p>
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
          <el-option value="new" label="新建凭据…" />
        </el-select>
      </Field>
      <div v-if="creating" class="flex flex-col gap-4 rounded-xl border bg-well/40 p-4">
        <Field v-slot="{ id }" label="凭据名称">
          <el-input :id="id" v-model="credName" :placeholder="auth === 'ssh_key' ? 'deploy 部署密钥' : 'GitLab 个人令牌'" />
        </Field>
        <template v-if="auth === 'https_token'">
          <div class="grid grid-cols-[10rem_1fr] gap-3">
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
          <template v-if="keySource === 'generate'">
            <template v-if="generated">
              <Field label="公钥" hint="私钥由平台生成后直接加密保存，你和员工都看不到它">
                <CopyBlock :text="PUBLIC_KEY" />
              </Field>
              <Callout tone="info">把上面的公钥添加为这个仓库的<strong>部署密钥</strong>，并勾选“允许写入”，然后点“测试连接”。</Callout>
            </template>
            <el-button v-else class="self-start" @click="generated = true">生成密钥对</el-button>
          </template>
          <template v-else>
            <Field v-slot="{ id }" label="私钥" hint="OpenSSH 或 PEM 格式（-----BEGIN … PRIVATE KEY-----）">
              <el-input :id="id" v-model="privateKey" type="textarea" :rows="4" class="at-mono" spellcheck="false" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" />
            </Field>
            <Field v-slot="{ id }" label="私钥口令（可选）">
              <el-input :id="id" type="password" show-password autocomplete="new-password" />
            </Field>
          </template>
        </template>
      </div>
    </template>
    <Callout tone="warn">
      单独添加的仓库没有 PR 和 CI：讨论串完成后平台推送讨论串分支，或在平台里验收后由平台合并。要用 PR 和 CI，请用 GitHub App 连接。
    </Callout>
    <div v-if="showResult" class="flex flex-col gap-2 rounded-xl border border-ok/40 bg-ok/5 px-4 py-3 text-[13px]" role="status">
      <span class="flex items-center gap-2 font-medium text-ok"><CircleCheck class="size-4" />连接成功 · 可读写</span>
      <span class="text-muted-foreground">默认分支 <span class="font-mono text-foreground">{{ preset === "ssh" ? "main" : "develop" }}</span> · 共 {{ preset === "ssh" ? 12 : 37 }} 个分支</span>
      <template v-if="preset === 'ssh'">
        <span class="text-muted-foreground">主机密钥（第一次连接，已固定）</span>
        <span class="break-all font-mono text-xs">ED25519 SHA256:4mS2pQx8wE1rT7yU0iO9aZ3sD6fG5hJ2kL8zX1cV4bN</span>
        <span class="text-xs text-muted-foreground">在服务器上核对：ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</span>
      </template>
    </div>
    <el-button class="self-start" @click="showResult = true">测试连接</el-button>
  </FormDialog>
</template>

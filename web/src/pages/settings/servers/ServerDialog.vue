<script setup lang="ts">
/** 添加 / 编辑服务器. The credential is write-only: editing shows “已保存，留空不修改”, and changing host,
 * port, user or the auth kind requires entering it again (the server seals it for those values). */
import { computed, ref, watch } from "vue";
import { servers as serverActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ServerView } from "@/api/views";
import Callout from "@/components/common/Callout.vue";
import Field from "@/components/common/Field.vue";
import FormDialog from "@/components/common/FormDialog.vue";
import { AUTH_LABEL, createBody, credentialRequired, emptyServerForm, patchBody, serverForm, serverFormProblem, type AuthKind, type ServerForm } from "@/lib/servers";

const open = defineModel<boolean>("open", { required: true });
const props = defineProps<{ server?: ServerView | null }>();
const emit = defineEmits<{ saved: [server: ServerView] }>();
const form = ref<ServerForm>(emptyServerForm());
const saved = computed(() => props.server ?? null);
watch(open, (v) => v && (form.value = saved.value ? serverForm(saved.value) : emptyServerForm()), { immediate: true });
const required = computed(() => credentialRequired(saved.value, form.value));
const problem = computed(() => serverFormProblem(saved.value, form.value));
const touched = ref(false);
watch(open, () => (touched.value = false));
const keep = computed(() => saved.value && !required.value);
const placeholder = computed(() => (keep.value ? "已保存，留空不修改" : ""));
const kindChanged = (k: AuthKind) => {
  form.value.kind = k;
  form.value.password = form.value.privateKey = form.value.passphrase = "";
};
const save = useAction(
  async () => {
    const s = saved.value;
    return s ? serverActions.update(s.id, patchBody(s, form.value)) : serverActions.create(createBody(form.value));
  },
  {
    invalidate: [keys.servers],
    success: () => (saved.value ? "已保存" : "服务器已添加"),
    onSuccess: (s) => {
      open.value = false;
      emit("saved", s);
    },
  },
);
function submit() {
  touched.value = true;
  if (!problem.value) save.mutate();
}
</script>

<template>
  <FormDialog
    v-model:open="open"
    :title="saved ? `编辑服务器 ${saved.name}` : '添加服务器'"
    description="员工的会话通过主节点登录这台服务器，节点上拿不到密码或私钥。保存后凭证不能再查看，只能替换。"
    :submit-label="saved ? '保存' : '添加'"
    :busy="save.isPending.value"
    width="36rem"
    @submit="submit"
  >
    <Field v-slot="{ id }" label="名称" hint="会话里用 ssh 名称 连接，例如 ssh prod；小写字母、数字和 -">
      <el-input :id="id" v-model="form.name" class="at-mono" placeholder="prod" />
    </Field>
    <div class="grid grid-cols-[1fr_7rem] gap-3">
      <Field v-slot="{ id }" label="主机" hint="域名或 IP">
        <el-input :id="id" v-model="form.host" class="at-mono" placeholder="203.0.113.10" />
      </Field>
      <Field v-slot="{ id }" label="端口">
        <el-input-number :id="id" v-model="form.port" :min="1" :max="65535" :controls="false" class="!w-full" />
      </Field>
    </div>
    <Field v-slot="{ id }" label="用户">
      <el-input :id="id" v-model="form.username" class="at-mono" placeholder="deploy" />
    </Field>
    <Field label="认证方式">
      <el-radio-group :model-value="form.kind" aria-label="认证方式" @update:model-value="(k) => kindChanged(k as AuthKind)">
        <el-radio-button v-for="(label, k) in AUTH_LABEL" :key="k" :value="k">{{ label }}</el-radio-button>
      </el-radio-group>
    </Field>
    <Callout v-if="saved && required" tone="warn">改了主机、端口、用户或认证方式，旧凭证随之作废，请重新填写{{ form.kind === "password" ? "密码" : "私钥" }}。</Callout>
    <Field v-if="form.kind === 'password'" v-slot="{ id }" label="密码">
      <el-input :id="id" v-model="form.password" type="password" show-password autocomplete="new-password" :placeholder="placeholder" />
    </Field>
    <template v-else>
      <Field v-slot="{ id }" label="私钥" hint="粘贴 OpenSSH 或 PEM 格式的私钥（-----BEGIN … PRIVATE KEY-----）">
        <el-input :id="id" v-model="form.privateKey" type="textarea" :rows="5" class="at-mono" spellcheck="false" :placeholder="placeholder || '-----BEGIN OPENSSH PRIVATE KEY-----'" />
      </Field>
      <Field v-slot="{ id }" label="私钥口令（可选）" hint="私钥加了 passphrase 才需要填">
        <el-input :id="id" v-model="form.passphrase" type="password" show-password autocomplete="new-password" :placeholder="keep && saved?.has_passphrase ? '已保存，留空不修改' : ''" />
      </Field>
    </template>
    <p v-if="touched && problem" class="text-[13px] text-bad" role="alert">{{ problem }}</p>
  </FormDialog>
</template>

<script setup lang="ts">
/** 替换凭据: rename, change the username, set the expiry, and replace the value (a new token, a pasted
 * key, or a newly generated key pair). Every repository using it switches at once. */
import { computed, ref, watch } from "vue";
import { gitCredentials } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { GitCredentialView } from "@/api/views";
import Callout from "@/components/common/Callout.vue";
import Field from "@/components/common/Field.vue";
import FormDialog from "@/components/common/FormDialog.vue";

const open = defineModel<boolean>("open", { required: true });
const props = defineProps<{ credential: GitCredentialView | null }>();
const name = ref("");
const username = ref("");
const token = ref("");
const privateKey = ref("");
const passphrase = ref("");
const regenerate = ref(false);
const expires = ref<Date | null>(null);
watch(
  open,
  (v) => {
    const c = props.credential;
    if (!v || !c) return;
    name.value = c.name;
    username.value = c.username;
    token.value = privateKey.value = passphrase.value = "";
    regenerate.value = false;
    expires.value = c.expires_at ? new Date(c.expires_at) : null;
  },
  { immediate: true },
);
const isToken = computed(() => props.credential?.kind === "https_token");
const hasSecret = computed(() => (isToken.value ? !!token.value.trim() : regenerate.value || !!privateKey.value.trim()));
const userChanged = computed(() => !!props.credential && username.value.trim() !== props.credential.username);
const problem = computed(() => (userChanged.value && !hasSecret.value ? "改了用户名需要同时填新的凭据" : ""));
const save = useAction(
  () => {
    const c = props.credential!;
    const secret = !hasSecret.value ? undefined : isToken.value ? { token: token.value.trim() } : regenerate.value ? { generate: true } : { private_key: privateKey.value, passphrase: passphrase.value || undefined };
    return gitCredentials.update(c.id, {
      name: name.value.trim(),
      username: username.value.trim(),
      secret,
      ...(isToken.value ? (expires.value ? { expires_at: expires.value.toISOString() } : { no_expiry: true }) : {}),
    });
  },
  {
    invalidate: [keys.gitCredentials, keys.gitRepos],
    success: () => (regenerate.value ? "已生成新密钥：复制新公钥，替换仓库上的部署密钥" : "凭据已保存"),
    onSuccess: () => (open.value = false),
  },
);
function submit() {
  if (!problem.value) save.mutate();
}
</script>

<template>
  <FormDialog
    v-if="credential"
    v-model:open="open"
    :title="`替换凭据 ${credential.name}`"
    :description="`主机 ${credential.host}。保存后所有用它的仓库（${credential.repos} 个）立即改用新值；旧值不能再查看。`"
    submit-label="保存"
    :busy="save.isPending.value"
    width="34rem"
    @submit="submit"
  >
    <Field v-slot="{ id }" label="名称">
      <el-input :id="id" v-model="name" />
    </Field>
    <Field v-slot="{ id }" label="用户名">
      <el-input :id="id" v-model="username" class="at-mono" />
    </Field>
    <template v-if="isToken">
      <Field v-slot="{ id }" label="新令牌" hint="留空不修改">
        <el-input :id="id" v-model="token" type="password" show-password autocomplete="new-password" placeholder="已保存，留空不修改" />
      </Field>
      <Field v-slot="{ id }" label="到期日（可选）">
        <el-date-picker :id="id" v-model="expires" type="date" placeholder="不过期" class="!w-48" />
      </Field>
    </template>
    <template v-else>
      <el-checkbox v-model="regenerate">重新生成密钥对（之后要在仓库上换成新公钥）</el-checkbox>
      <template v-if="!regenerate">
        <Field v-slot="{ id }" label="新私钥" hint="留空不修改">
          <el-input :id="id" v-model="privateKey" type="textarea" :rows="4" class="at-mono" spellcheck="false" placeholder="已保存，留空不修改" />
        </Field>
        <Field v-slot="{ id }" label="私钥口令（可选）">
          <el-input :id="id" v-model="passphrase" type="password" show-password autocomplete="new-password" />
        </Field>
      </template>
    </template>
    <Callout v-if="problem" tone="warn">{{ problem }}</Callout>
  </FormDialog>
</template>

<script setup lang="ts">
import { $t } from "@/i18n";
/** 新建用户: email, display name, role, a generated or typed first password and the forced
 * change. A generated password is shown once afterwards; a typed one is never echoed. Both
 * leave memory when their dialog closes. */
import { useMutation, useQueryClient } from "@tanstack/vue-query";
import { computed, ref, watch } from "vue";
import { admin } from "@/api/actions";
import { keys } from "@/api/queries";
import CopyBlock from "@/components/common/CopyBlock.vue";
import Field from "@/components/common/Field.vue";
import FormDialog from "@/components/common/FormDialog.vue";
import InfoDialog from "@/components/common/InfoDialog.vue";
import { toast } from "@/lib/toast";
import { createUserError, emptyNewUserForm, MIN_PASSWORD_LENGTH, newUserBody, newUserProblem, type FormProblem, type NewUserForm } from "./admin";

const open = defineModel<boolean>("open", { required: true });
const form = ref<NewUserForm>(emptyNewUserForm());
const touched = ref(false);
const serverProblem = ref<FormProblem | null>(null);
watch(open, () => {
  form.value = emptyNewUserForm();
  touched.value = false;
  serverProblem.value = null;
});
// Editing a field clears the server's complaint about it.
watch(form, () => (serverProblem.value = null), { deep: true });
const problem = computed(() => serverProblem.value ?? (touched.value ? newUserProblem(form.value) : null));
const errorFor = (field: FormProblem["field"]) => (problem.value?.field === field ? problem.value.message : "");

const created = ref<{ email: string; password: string } | null>(null);
const shown = computed({ get: () => Boolean(created.value), set: (v: boolean) => !v && (created.value = null) });

const queryClient = useQueryClient();
let sending = false;
const create = useMutation({
  mutationFn: () => admin.createUser(newUserBody(form.value)),
  onSuccess: (r) => {
    void queryClient.invalidateQueries({ queryKey: keys.adminUsers });
    open.value = false;
    if (r.temp_password) created.value = { email: r.user.email, password: r.temp_password };
    else toast.success($t("已创建 {email}", { email: r.user.email }));
  },
  onError: (e) => (serverProblem.value = createUserError(e)),
  onSettled: () => (sending = false),
});
function submit() {
  touched.value = true;
  if (sending || newUserProblem(form.value)) return;
  sending = true;
  create.mutate();
}
</script>

<template>
  <FormDialog v-model:open="open" :title="$t('新建用户')" :description="$t('不受“开放注册”影响。创建后把邮箱和初始密码交给对方。')" :submit-label="$t('创建')" :busy="create.isPending.value" @submit="submit">
    <Field v-slot="{ id }" :label="$t('邮箱')">
      <el-input :id="id" v-model="form.email" type="email" autocomplete="off" placeholder="name@example.com" />
      <p v-if="errorFor('email')" class="text-[13px] text-bad" role="alert" data-error="email">{{ errorFor("email") }}</p>
    </Field>
    <Field v-slot="{ id }" :label="$t('显示名')" :hint="$t('可选，最多 100 个字')">
      <el-input :id="id" v-model="form.displayName" autocomplete="off" maxlength="100" />
      <p v-if="errorFor('display_name')" class="text-[13px] text-bad" role="alert">{{ errorFor("display_name") }}</p>
    </Field>
    <Field :label="$t('角色')">
      <el-radio-group v-model="form.role" :aria-label="$t('角色')">
        <el-radio-button value="user">{{ $t("成员") }}</el-radio-button>
        <el-radio-button value="admin">{{ $t("管理员") }}</el-radio-button>
      </el-radio-group>
    </Field>
    <Field :label="$t('初始密码')">
      <el-radio-group v-model="form.passwordMode" :aria-label="$t('初始密码')">
        <el-radio-button value="generated">{{ $t("系统生成") }}</el-radio-button>
        <el-radio-button value="manual">{{ $t("手动填写") }}</el-radio-button>
      </el-radio-group>
    </Field>
    <Field v-if="form.passwordMode === 'manual'" v-slot="{ id }" :label="$t('密码')" :hint="$t('至少 {MIN_PASSWORD_LENGTH} 位；创建后不再显示', { MIN_PASSWORD_LENGTH })">
      <el-input :id="id" v-model="form.password" type="password" show-password autocomplete="new-password" />
      <p v-if="errorFor('password')" class="text-[13px] text-bad" role="alert" data-error="password">{{ errorFor("password") }}</p>
    </Field>
    <p v-else class="-mt-2 text-xs leading-5 text-muted-foreground">{{ $t("创建后显示一次，请复制给对方。") }}</p>
    <el-checkbox v-model="form.mustChangePassword">{{ $t("首次登录必须修改密码") }}</el-checkbox>
    <p v-if="errorFor('')" class="text-[13px] text-bad" role="alert">{{ errorFor("") }}</p>
  </FormDialog>
  <InfoDialog v-model:open="shown" :title="$t('初始密码')" :description="$t('把它交给 {email}。只显示这一次，关闭后无法再查看。', { email: created?.email })">
    <CopyBlock v-if="created" :text="created.password" />
  </InfoDialog>
</template>

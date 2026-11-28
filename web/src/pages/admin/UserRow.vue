<script setup lang="ts">
import { $t } from "@/i18n";
/** One account: display name, role, nodes, running tasks, active, last login, joined; menu for everyone but you. */
import { MoreHorizontal } from "lucide-vue-next";
import { ref } from "vue";
import { admin } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { AdminUserView } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import DropdownMenu from "@/components/common/DropdownMenu.vue";
import { timeAgo } from "@/lib/utils";
import { shortDate } from "./admin";

const props = defineProps<{ user: AdminUserView; self: boolean }>();
const emit = defineEmits<{ temp: [password: string] }>();
const deleting = ref(false);
const invalidate = [keys.adminUsers];
const update = useAction((body: { role?: "admin" | "user"; active?: boolean }) => admin.updateUser(props.user.id, body), { invalidate });
const reset = useAction(() => admin.resetPassword(props.user.id), { onSuccess: (r) => emit("temp", r.temp_password) });
const remove = useAction(() => admin.removeUser(props.user.id), { invalidate, success: $t("账号已删除") });
const COMMANDS: Record<string, () => void> = {
  role: () => update.mutate({ role: props.user.role === "admin" ? "user" : "admin" }),
  active: () => update.mutate({ active: !props.user.active }),
  reset: () => reset.mutate(),
  remove: () => (deleting.value = true),
};
const TD = "px-4 py-3";
</script>

<template>
  <tr class="border-t border-[#F1EFEA] dark:border-border">
    <td :class="TD">
      {{ user.email }}{{ self ? $t("（你）") : "" }}<span v-if="user.must_change_password" class="ml-1.5 text-xs text-warn">{{ $t("待改密码") }}</span>
    </td>
    <td :class="[TD, user.display_name ? '' : 'text-muted-foreground']">{{ user.display_name || "—" }}</td>
    <td :class="TD">{{ user.role === "admin" ? $t("管理员") : $t("成员") }}</td>
    <td :class="TD">{{ user.nodes }}</td>
    <td :class="TD">{{ user.running_tasks }}</td>
    <td :class="[TD, user.active ? 'text-ok' : 'text-destructive']">{{ user.active ? $t("启用") : $t("停用") }}</td>
    <td :class="[TD, 'text-muted-foreground']">{{ user.last_login_at ? timeAgo(user.last_login_at) : "—" }}</td>
    <td :class="[TD, 'text-muted-foreground']">{{ shortDate(user.created_at) }}</td>
    <td class="px-2 py-3">
      <DropdownMenu v-if="!self" placement="bottom-end" @command="(c: string) => COMMANDS[c]?.()">
        <button type="button" :aria-label="$t('{email} 的操作', { email: user.email })" class="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-raised">
          <MoreHorizontal class="size-4" />
        </button>
        <template #menu>
          <el-dropdown-item command="role">{{ user.role === "admin" ? $t("设为成员") : $t("设为管理员") }}</el-dropdown-item>
          <el-dropdown-item command="active">{{ user.active ? $t("停用") : $t("启用") }}</el-dropdown-item>
          <el-dropdown-item command="reset">{{ $t("重置密码") }}</el-dropdown-item>
          <el-dropdown-item command="remove" divided class="is-destructive">{{ $t("删除账号") }}</el-dropdown-item>
        </template>
      </DropdownMenu>
      <ConfirmAction v-model:open="deleting" :title="$t('删除 {email}？', { email: user.email })" :description="$t('会断开他的节点、删除他的项目和记录，不能恢复。')" :confirm-label="$t('删除')" @confirm="remove.mutate()" />
    </td>
  </tr>
</template>

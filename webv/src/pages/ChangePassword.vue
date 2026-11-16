<script setup lang="ts">
/** Shown instead of the app while `must_change_password` is set. */
import { useQueryClient } from "@tanstack/vue-query";
import { ref } from "vue";
import { auth } from "@/api/actions";
import { errorText } from "@/api/mutations";
import { keys } from "@/api/queries";
import Spinner from "@/components/common/Spinner.vue";
import { AUTH_INPUT, AUTH_SUBMIT } from "./auth";
import AuthShell from "./AuthShell.vue";

const qc = useQueryClient();
const current = ref("");
const next = ref("");
const busy = ref(false);
const error = ref("");

async function submit() {
  busy.value = true;
  error.value = "";
  try {
    await auth.changePassword({ current: current.value, new: next.value });
    await qc.invalidateQueries({ queryKey: keys.me });
  } catch (err) {
    error.value = errorText(err);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <AuthShell subtitle="管理员重置了你的密码，请先设一个新密码。">
    <form class="flex flex-col gap-4 rounded-2xl border bg-panel p-6" @submit.prevent="submit">
      <label class="flex flex-col gap-1.5">
        <span class="text-[13px] font-medium">临时密码</span>
        <input v-model="current" type="password" required autocomplete="current-password" :class="AUTH_INPUT" />
      </label>
      <label class="flex flex-col gap-1.5">
        <span class="text-[13px] font-medium">新密码</span>
        <input v-model="next" type="password" required minlength="8" autocomplete="new-password" :class="AUTH_INPUT" />
      </label>
      <p v-if="error" role="alert" class="text-[13px] text-destructive">{{ error }}</p>
      <button type="submit" :disabled="busy" :class="AUTH_SUBMIT">
        <Spinner v-if="busy" />
        保存新密码
      </button>
    </form>
  </AuthShell>
</template>

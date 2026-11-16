<script setup lang="ts">
// Login board (登录 / 注册 tabs). A successful login writes the user into the `me` query, which
// switches the router to the app (router.ts guards on it).
import { useQueryClient } from "@tanstack/vue-query";
import { ref } from "vue";
import { auth } from "@/api/actions";
import { errorText } from "@/api/mutations";
import { keys } from "@/api/queries";
import Spinner from "@/components/common/Spinner.vue";
import { cn } from "@/lib/utils";
import { AUTH_INPUT, AUTH_SUBMIT } from "./auth";
import AuthShell from "./AuthShell.vue";

type Mode = "login" | "register";
const qc = useQueryClient();
const mode = ref<Mode>("login");
const email = ref("");
const password = ref("");
const busy = ref(false);
const error = ref("");

async function submit() {
  busy.value = true;
  error.value = "";
  try {
    const call = mode.value === "login" ? auth.login : auth.register;
    qc.setQueryData(keys.me, await call({ email: email.value.trim(), password: password.value }));
  } catch (err) {
    error.value = errorText(err);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <AuthShell subtitle="把需求交给项目统筹，看团队在你的机器上把活干完。">
    <form class="flex flex-col gap-4 rounded-2xl border bg-panel p-6" @submit.prevent="submit">
      <div role="tablist" aria-label="登录或注册" class="grid grid-cols-2 gap-1 rounded-[10px] bg-sidebar p-1">
        <button
          v-for="m in ['login', 'register'] as Mode[]"
          :key="m"
          type="button"
          role="tab"
          :aria-selected="mode === m"
          :class="cn('h-[34px] rounded-lg', mode === m ? 'bg-white font-semibold shadow-[0_1px_2px_rgba(0,0,0,0.08)] dark:bg-raised' : '')"
          @click="mode = m"
        >
          {{ m === "login" ? "登录" : "注册" }}
        </button>
      </div>
      <label class="flex flex-col gap-1.5">
        <span class="text-[13px] font-medium">邮箱</span>
        <input v-model="email" type="email" required autocomplete="email" placeholder="you@example.com" :class="AUTH_INPUT" />
      </label>
      <label class="flex flex-col gap-1.5">
        <span class="text-[13px] font-medium">密码</span>
        <input
          v-model="password"
          type="password"
          required
          :minlength="mode === 'register' ? 8 : undefined"
          :autocomplete="mode === 'login' ? 'current-password' : 'new-password'"
          :class="AUTH_INPUT"
        />
        <span v-if="mode === 'register'" class="text-xs text-muted-foreground">至少 8 位。第一个注册的账号自动成为管理员。</span>
      </label>
      <p v-if="error" role="alert" class="text-[13px] text-destructive">{{ error }}</p>
      <button type="submit" :disabled="busy" :class="AUTH_SUBMIT">
        <Spinner v-if="busy" />
        {{ mode === "login" ? "登录" : "注册并登录" }}
      </button>
    </form>
  </AuthShell>
</template>

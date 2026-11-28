<script setup lang="ts">
// 外观与语言: theme (this browser only), interface language (saved on the account), plus the account
// basics: change password and signed-in sessions.
import { Moon, Sun } from "lucide-vue-next";
import { ref } from "vue";
import { auth } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useSessions } from "@/api/queries";
import Card from "@/components/app/Card.vue";
import CardTitle from "@/components/app/CardTitle.vue";
import Field from "@/components/common/Field.vue";
import NavButton from "@/components/shell/NavButton.vue";
import { $t, storedChoice } from "@/i18n";
import { languageOptions, useSaveLanguage } from "@/components/app/language/language";
import { useTheme, type Theme } from "@/lib/theme";
import { cn, timeAgo } from "@/lib/utils";

const { theme, setTheme } = useTheme();
const OPTIONS: { value: Theme; label: string; icon: typeof Sun }[] = [
  { value: "light", label: $t("亮色"), icon: Sun },
  { value: "dark", label: $t("暗色"), icon: Moon },
];

// "" follows the browser; a pick is saved on the account, then the page reloads in that language.
const language = ref(storedChoice());
const LANGUAGES = languageOptions();
const saveLanguage = useSaveLanguage();

const current = ref("");
const next = ref("");
const change = useAction(() => auth.changePassword({ current: current.value, new: next.value }), {
  invalidate: [keys.sessions],
  success: $t("密码已修改，其他设备上的登录已退出"),
  onSuccess: () => {
    current.value = "";
    next.value = "";
  },
});

const sessions = useSessions();
const revoke = useAction((id: string) => auth.revokeSession(id), { invalidate: [keys.sessions], success: $t("已退出该设备") });
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[760px] flex-col gap-5">
      <div class="flex items-start gap-1">
        <NavButton />
        <div class="flex flex-col gap-1.5">
          <h1 class="text-2xl font-semibold">{{ $t("外观与账号") }}</h1>
          <p class="text-[13px] text-muted-foreground">{{ $t("主题只影响这台设备上的浏览器，界面语言跟着账号走。") }}</p>
        </div>
      </div>
      <Card class="gap-3.5">
        <CardTitle>{{ $t("主题") }}</CardTitle>
        <div class="grid grid-cols-2 gap-3">
          <button
            v-for="o in OPTIONS"
            :key="o.value"
            type="button"
            :aria-pressed="theme === o.value"
            :class="cn('flex items-center gap-3 rounded-xl border-[1.5px] p-4 text-left', theme === o.value ? 'border-foreground' : 'border-border hover:border-input')"
            @click="setTheme(o.value)"
          >
            <component :is="o.icon" class="size-5 text-muted-foreground" />
            <span class="font-medium">{{ o.label }}</span>
          </button>
        </div>
      </Card>
      <Card class="gap-3.5">
        <CardTitle>{{ $t("界面语言") }}</CardTitle>
        <p class="-mt-1.5 text-[13px] text-muted-foreground">{{ $t("默认跟随浏览器语言；手动选择后保存在账号上，所有设备通用。") }}</p>
        <el-select
          v-model="language"
          class="!w-60"
          :aria-label="$t('界面语言')"
          :disabled="saveLanguage.isPending.value"
          @change="(v: string) => saveLanguage.mutate(v)"
        >
          <el-option v-for="o in LANGUAGES" :key="o.value" :value="o.value" :label="o.label" />
        </el-select>
      </Card>
      <Card class="gap-3.5">
        <CardTitle>{{ $t("修改密码") }}</CardTitle>
        <form class="grid gap-3 sm:grid-cols-[1fr_1fr_auto] sm:items-end" @submit.prevent="change.mutate()">
          <Field v-slot="{ id }" :label="$t('当前密码')">
            <el-input :id="id" v-model="current" type="password" autocomplete="current-password" />
          </Field>
          <Field v-slot="{ id }" :label="$t('新密码（至少 8 位）')">
            <el-input :id="id" v-model="next" type="password" autocomplete="new-password" :minlength="8" />
          </Field>
          <el-button type="primary" native-type="submit" :disabled="!current || next.length < 8 || change.isPending.value">{{ $t("保存") }}</el-button>
        </form>
      </Card>
      <Card class="gap-3">
        <CardTitle>{{ $t("登录的设备") }}</CardTitle>
        <div v-for="s in sessions.data.value ?? []" :key="s.id" class="flex items-center gap-3 rounded-lg bg-background px-3 py-2.5 text-[13px]">
          <span class="min-w-0 flex-1">
            <span class="block truncate">{{ s.user_agent || $t("未知浏览器") }}</span>
            <span class="text-xs text-muted-foreground">{{ s.current ? $t("这台设备 · ") : "" }}{{ $t("最近活动 {ago}", { ago: timeAgo(s.last_seen_at ?? s.created_at) }) }}</span>
          </span>
          <el-button v-if="!s.current" size="small" @click="revoke.mutate(s.id)">{{ $t("退出") }}</el-button>
        </div>
      </Card>
    </div>
  </main>
</template>

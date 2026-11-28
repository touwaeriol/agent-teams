<script setup lang="ts">
// Admin console sidebar: back to the user side, then the admin pages.
import { $t } from "@/i18n";
import { ArrowLeft, Boxes, FileClock, Github, MessageSquareText, SlidersHorizontal, Timer, Users } from "lucide-vue-next";
import { computed } from "vue";
import { RouterLink } from "vue-router";
import type { UserView } from "@/api/views";
import Initial from "@/components/app/Initial.vue";
import Logo from "@/components/common/Logo.vue";
import { cn } from "@/lib/utils";

const props = defineProps<{ me: UserView }>();
const emit = defineEmits<{ navigate: [] }>();
const ITEMS = [
  { to: "/admin/users", label: $t("用户"), hint: $t("账号、角色和节点"), icon: Users },
  { to: "/admin/platform", label: $t("平台设置"), hint: $t("注册、限额和语音输入"), icon: SlidersHorizontal },
  { to: "/admin/library", label: $t("内置命令和插件"), hint: $t("内置命令、插件、技能"), icon: Boxes },
  { to: "/admin/feedback", label: $t("反馈"), hint: $t("用户提交的反馈"), icon: MessageSquareText },
  { to: "/admin/jobs", label: $t("后台任务"), hint: $t("定时运行的任务和运行记录"), icon: Timer },
  { to: "/admin/github-app", label: $t("GitHub App"), hint: $t("平台 App 的创建和状态"), icon: Github },
  { to: "/admin/audit", label: $t("审计日志"), hint: $t("管理操作记录"), icon: FileClock },
];
const name = computed(() => props.me.display_name || props.me.email.split("@")[0]);
</script>

<template>
  <nav :aria-label="$t('管理后台')" class="flex h-full w-[264px] shrink-0 flex-col border-r bg-sidebar">
    <div class="flex h-14 items-center gap-2.5 px-[18px] font-semibold">
      <Logo class="size-5" />
      {{ $t("管理后台") }}
    </div>
    <div class="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto px-3">
      <RouterLink to="/projects" class="flex h-9 shrink-0 items-center gap-2.5 rounded-lg px-2.5 text-foreground/80 hover:bg-accent/60" @click="emit('navigate')">
        <ArrowLeft class="size-4" :stroke-width="1.8" />
        {{ $t("返回用户端") }}
      </RouterLink>
      <span class="block px-2.5 pb-1.5 pt-4 text-xs text-muted-foreground">{{ $t("管理") }}</span>
      <RouterLink v-for="i in ITEMS" :key="i.to" v-slot="{ href, navigate, isActive }" :to="i.to" custom>
        <a
          :href="href"
          :aria-current="isActive ? 'page' : undefined"
          :class="cn('flex shrink-0 items-start gap-2.5 rounded-lg px-2.5 py-2', isActive ? 'bg-accent font-semibold' : 'hover:bg-accent/60')"
          @click="
            (e) => {
              navigate(e);
              emit('navigate');
            }
          "
        >
          <component :is="i.icon" class="mt-0.5 size-4 shrink-0 text-muted-foreground" :stroke-width="1.8" />
          <span class="flex flex-col gap-px">
            <span class="text-sm">{{ i.label }}</span>
            <span class="text-[11px] font-normal text-muted-foreground">{{ i.hint }}</span>
          </span>
        </a>
      </RouterLink>
    </div>
    <div class="flex items-center gap-2.5 border-t p-3">
      <Initial :name="name" bg="#F4E3D7" fg="#A24E1E" :size="32" round />
      <span class="flex flex-col">
        <span class="text-[13px]">{{ name }}</span>
        <span class="text-[11px] text-muted-foreground">{{ $t("管理员") }}</span>
      </span>
    </div>
  </nav>
</template>

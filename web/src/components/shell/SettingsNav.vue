<script setup lang="ts">
// Settings sidebar (SettingsNav board): back to projects, then the settings pages.
// No admin entry here: the admin console lives at /admin.
import { $t } from "@/i18n";
import { ArrowLeft } from "lucide-vue-next";
import { computed } from "vue";
import { RouterLink } from "vue-router";
import type { UserView } from "@/api/views";
import Initial from "@/components/app/Initial.vue";
import Logo from "@/components/common/Logo.vue";
import { cn } from "@/lib/utils";

const props = defineProps<{ me: UserView }>();
const emit = defineEmits<{ navigate: [] }>();
const ITEMS = [
  { to: "/settings/team", label: $t("团队岗位"), hint: $t("统筹、组长和成员模板") },
  { to: "/settings/workflows", label: $t("工作流程"), hint: $t("流程图：哪一步交给哪个岗位") },
  { to: "/settings/nodes", label: $t("节点"), hint: $t("运行成员的机器") },
  { to: "/settings/git", label: $t("Git 仓库"), hint: $t("GitHub 连接和其他 Git 仓库") },
  { to: "/settings/servers", label: $t("服务器"), hint: $t("员工能访问的远程机器") },
  { to: "/settings/storage", label: $t("共享文件夹"), hint: $t("挂到每个节点上的持久目录") },
  { to: "/settings/memory", label: $t("我的记忆"), hint: $t("所有项目共用的偏好") },
  { to: "/settings/appearance", label: $t("外观与语言"), hint: $t("主题和界面语言") },
];
const name = computed(() => props.me.display_name || props.me.email.split("@")[0]);
</script>

<template>
  <nav :aria-label="$t('设置')" class="flex h-full w-[264px] shrink-0 flex-col border-r bg-sidebar">
    <div class="flex h-14 items-center gap-2.5 px-[18px] font-semibold">
      <Logo class="size-5" />
      Agent Teams
    </div>
    <div class="flex flex-col gap-0.5 px-3">
      <RouterLink to="/projects" class="flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-[#4A4843] hover:bg-accent/60" @click="emit('navigate')">
        <ArrowLeft class="size-4" :stroke-width="1.8" />
        {{ $t("返回项目") }}
      </RouterLink>
      <span class="block px-2.5 pb-1.5 pt-4 text-xs text-muted-foreground">{{ $t("设置") }}</span>
      <RouterLink v-for="i in ITEMS" :key="i.to" v-slot="{ href, navigate, isActive }" :to="i.to" custom>
        <a
          :href="href"
          :class="cn('flex flex-col gap-px rounded-lg px-2.5 py-2', isActive ? 'bg-accent font-semibold' : 'hover:bg-accent/60')"
          @click="
            (e) => {
              navigate(e);
              emit('navigate');
            }
          "
        >
          <span class="text-sm">{{ i.label }}</span>
          <span class="text-[11px] font-normal text-muted-foreground">{{ i.hint }}</span>
        </a>
      </RouterLink>
    </div>
    <div class="flex-1" />
    <div class="flex items-center gap-2.5 border-t p-3">
      <Initial :name="name" bg="#F4E3D7" fg="#A24E1E" :size="32" round />
      <span class="flex flex-col">
        <span class="text-[13px]">{{ name }}</span>
        <span class="text-[11px] text-muted-foreground">{{ me.role === "admin" ? $t("管理员") : $t("成员") }}</span>
      </span>
    </div>
  </nav>
</template>

<script setup lang="ts">
// Settings sidebar (SettingsNav board): back to projects, then the settings pages.
// No admin entry here: the admin console lives at /admin.
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
  { to: "/settings/team", label: "团队岗位", hint: "统筹、组长和成员模板" },
  { to: "/settings/nodes", label: "节点", hint: "运行成员的机器" },
  { to: "/settings/git", label: "GitHub 连接", hint: "授权你的仓库" },
  { to: "/settings/servers", label: "服务器", hint: "员工能访问的远程机器" },
  { to: "/settings/appearance", label: "外观", hint: "亮色 / 暗色" },
];
const name = computed(() => props.me.display_name || props.me.email.split("@")[0]);
</script>

<template>
  <nav aria-label="设置" class="flex h-full w-[264px] shrink-0 flex-col border-r bg-sidebar">
    <div class="flex h-14 items-center gap-2.5 px-[18px] font-semibold">
      <Logo class="size-5" />
      Agent Teams
    </div>
    <div class="flex flex-col gap-0.5 px-3">
      <RouterLink to="/projects" class="flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-[#4A4843] hover:bg-accent/60" @click="emit('navigate')">
        <ArrowLeft class="size-4" :stroke-width="1.8" />
        返回项目
      </RouterLink>
      <span class="block px-2.5 pb-1.5 pt-4 text-xs text-muted-foreground">设置</span>
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
        <span class="text-[11px] text-muted-foreground">{{ me.role === "admin" ? "管理员" : "成员" }}</span>
      </span>
    </div>
  </nav>
</template>

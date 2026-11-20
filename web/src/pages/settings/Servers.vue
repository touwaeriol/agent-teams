<script setup lang="ts">
// 服务器: remote machines employees may reach over SSH through the console (not 节点, which run the
// agents). Configured here once; each project picks some and grants them to roles (项目设置 → 环境).
import { computed, ref } from "vue";
import { useServers } from "@/api/queries";
import type { ServerView } from "@/api/views";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import NavButton from "@/components/shell/NavButton.vue";
import ServerCard from "./servers/ServerCard.vue";
import ServerDialog from "./servers/ServerDialog.vue";

const servers = useServers();
const list = computed(() => servers.data.value ?? []);
const dialog = ref(false);
const editing = ref<ServerView | null>(null);
function openDialog(s: ServerView | null) {
  editing.value = s;
  dialog.value = true;
}
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[960px] flex-col gap-5">
      <header class="flex flex-wrap items-end justify-between gap-3">
        <div class="flex items-start gap-1">
          <NavButton />
          <div class="flex flex-col gap-1.5">
            <h1 class="text-2xl font-semibold">服务器</h1>
            <p class="max-w-[640px] text-[13px] leading-[22px] text-muted-foreground">
              员工可以 ssh 上去部署、排查的远程机器。在这里配置一次，再到项目设置 → 环境里选用并按岗位授权。员工的会话经主节点登录，节点上拿不到密码或私钥；每次连接和命令都有访问记录。
            </p>
          </div>
        </div>
        <el-button type="primary" class="h-[38px]" @click="openDialog(null)">添加服务器</el-button>
      </header>
      <Loading v-if="servers.isLoading.value" />
      <EmptyState v-if="servers.data.value && list.length === 0" title="还没有服务器" description="点“添加服务器”，填主机、用户和密码或私钥，然后测试连接。" />
      <ServerCard v-for="s in list" :key="s.id" :server="s" @edit="openDialog(s)" />
    </div>
    <ServerDialog v-model:open="dialog" :server="editing" />
  </main>
</template>

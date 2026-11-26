<script setup lang="ts">
// 共享文件夹: the user's JuiceFS volume, mounted on every node of theirs. Each project gets
// projects/<id>, and shared/ is open to all their projects. Switched on once here; nodes mount it
// themselves (fs_mount) or through the Docker volume plugin (fs_external).
import { computed, ref } from "vue";
import { sharedFolder } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useSharedFolder } from "@/api/queries";
import type { components } from "@/api/schema";
import Callout from "@/components/common/Callout.vue";
import Loading from "@/components/common/Loading.vue";
import Panel from "@/components/common/Panel.vue";
import NavButton from "@/components/shell/NavButton.vue";

type Docker = components["schemas"]["FsDockerVolume"];
const STATE: Record<string, { label: string; type: "success" | "warning" | "danger" | "info" }> = {
  provisioning: { label: "开通中", type: "warning" },
  ready: { label: "可用", type: "success" },
  error: { label: "出错", type: "danger" },
  deleting: { label: "删除中", type: "info" },
};

const fs = useSharedFolder();
const view = computed(() => fs.data.value);
const vol = computed(() => view.value?.volume);
const state = computed(() => STATE[vol.value?.state ?? ""] ?? null);
const docker = ref<Docker | null>(null);
const typed = ref("");
const showSetup = computed(() => !vol.value || vol.value.state === "error");

const enable = useAction(() => sharedFolder.enable(), { invalidate: [keys.sharedFolder], success: "共享文件夹已开通" });
const remove = useAction(() => sharedFolder.remove(typed.value), {
  invalidate: [keys.sharedFolder],
  success: "共享文件夹已删除",
  onSuccess: () => ((typed.value = ""), (docker.value = null)),
});
const makeDocker = useAction(() => sharedFolder.dockerVolume() as Promise<Docker>, { onSuccess: (d) => (docker.value = d) });
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[960px] flex-col gap-5">
      <header class="flex items-start gap-1">
        <NavButton />
        <div class="flex flex-col gap-1.5">
          <h1 class="text-2xl font-semibold">共享文件夹</h1>
          <p class="max-w-[680px] text-[13px] leading-[22px] text-muted-foreground">
            一个属于你的网络文件夹，挂在你所有节点上，员工像本地目录一样读写，会话结束也不会丢。每个项目有自己的目录，另有一个所有项目都能用的公共目录。代码仍放在各节点的 Git 工作区。
          </p>
        </div>
      </header>

      <Loading v-if="fs.isLoading.value" />
      <template v-else-if="view">
        <Callout v-if="!view.configured" tone="warn">控制面板还没有配置共享存储（AGENT_TEAMS_FS_*），请管理员先配置。</Callout>

        <template v-else>
          <Panel v-if="showSetup" title="开通共享文件夹">
            <p class="text-[13px] leading-[22px] text-muted-foreground">
              开通即用，节点连上后会自动挂载。SQLite 这类数据库可以放进去，但同一个库同一时间只能由一个会话写入。
            </p>
            <Callout v-if="vol?.state === 'error'" tone="bad">{{ vol.error || "开通失败" }}</Callout>
            <div>
              <el-button type="primary" :loading="enable.isPending.value" @click="enable.mutate()">{{ vol ? "重试" : "开通" }}</el-button>
            </div>
          </Panel>
        </template>

        <template v-if="view.configured && vol">
          <Panel title="状态">
            <template #action>
              <el-tag v-if="state" :type="state.type" disable-transitions>{{ state.label }}</el-tag>
            </template>
            <dl class="grid grid-cols-[110px_1fr] gap-y-2 text-[13px]">
              <dt class="text-muted-foreground">卷名</dt>
              <dd class="font-mono">{{ vol.volume }}</dd>
              <dt class="text-muted-foreground">容量上限</dt>
              <dd>{{ vol.capacity_gb > 0 ? `${vol.capacity_gb} GB` : "不限" }}</dd>
              <dt class="text-muted-foreground">项目目录</dt>
              <dd class="font-mono">{{ view.project_dir }}</dd>
              <dt class="text-muted-foreground">公共目录</dt>
              <dd class="font-mono">{{ view.shared_dir }}</dd>
            </dl>
            <p class="text-[13px] leading-[22px] text-muted-foreground">
              员工会话里用环境变量 <code>$AGENT_TEAMS_PROJECT_DIR</code> 和 <code>$AGENT_TEAMS_SHARED_DIR</code> 找到这两个目录。节点的挂载状态在“节点”页查看。
            </p>
          </Panel>

          <Panel v-if="vol.state === 'ready'" title="Docker 节点">
            <p class="text-[13px] leading-[22px] text-muted-foreground">
              装在宿主机上的节点会自己挂载。Docker 节点建议用 JuiceFS 官方卷插件由宿主机挂载，节点容器不需要任何特权，也拿不到存储密码。
            </p>
            <div><el-button :loading="makeDocker.isPending.value" @click="makeDocker.mutate()">生成 Docker 卷配置</el-button></div>
            <template v-if="docker">
              <Callout tone="warn">下面的配置含存储密码，只放在节点所在的宿主机上，不要提交到代码仓库。</Callout>
              <p class="text-[13px] font-medium">1. 在宿主机上装一次插件</p>
              <pre class="overflow-x-auto rounded-lg bg-raised p-3 text-[12px] leading-5">{{ docker.plugin }}</pre>
              <p class="text-[13px] font-medium">2. 合进节点的 compose.yml，然后 docker compose up -d</p>
              <pre class="overflow-x-auto rounded-lg bg-raised p-3 text-[12px] leading-5">{{ docker.compose }}</pre>
              <p class="text-[13px] text-muted-foreground">容器里挂在 <code>{{ docker.mount }}</code>。</p>
            </template>
          </Panel>

          <Panel v-if="vol.state !== 'deleting'" title="删除" tone="danger">
            <p class="text-[13px] leading-[22px] text-muted-foreground">
              删除会清空卷里的所有文件，无法恢复。确认请输入卷名 <span class="font-mono">{{ vol.volume }}</span>。</p>
            <div class="flex flex-wrap gap-2">
              <el-input v-model="typed" class="max-w-[280px]" :placeholder="vol.volume" aria-label="输入卷名确认删除" />
              <el-button type="danger" :disabled="typed !== vol.volume" :loading="remove.isPending.value" @click="remove.mutate()">删除共享文件夹</el-button>
            </div>
          </Panel>
        </template>
      </template>
    </div>
  </main>
</template>

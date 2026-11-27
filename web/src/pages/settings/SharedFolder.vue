<script setup lang="ts">
// 共享文件夹: the user's folder (a prefix of the platform's object-store bucket), mounted
// (geesefs) on every node of theirs.
// Each project gets projects/<id>, and shared/ is open to all their projects. It is on by default:
// the console provisions it by itself (a failed attempt can be retried here); nodes mount it
// themselves (fs_s3_mount, Docker nodes too: the container needs /dev/fuse).
import { computed } from "vue";
import { sharedFolder } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useSharedFolder } from "@/api/queries";
import { formatBytes } from "@/lib/bytes";
import Callout from "@/components/common/Callout.vue";
import Loading from "@/components/common/Loading.vue";
import Panel from "@/components/common/Panel.vue";
import NavButton from "@/components/shell/NavButton.vue";

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
// 已用空间, with the share of the cap when there is one.
const used = computed(() => {
  const u = view.value?.usage;
  if (!u) return "";
  const cap = vol.value?.capacity_gb ?? 0;
  const share = cap > 0 ? `，占上限 ${Math.min(100, Math.round((u.used_bytes / (cap * 2 ** 30)) * 100))}%` : "";
  return `${formatBytes(u.used_bytes)}（${u.objects.toLocaleString("zh-CN")} 个对象${share}）`;
});

const retry = useAction(() => sharedFolder.enable(), { invalidate: [keys.sharedFolder], success: "已重新开始开通" });
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[960px] flex-col gap-5">
      <header class="flex items-start gap-1">
        <NavButton />
        <div class="flex flex-col gap-1.5">
          <h1 class="text-2xl font-semibold">共享文件夹</h1>
          <p class="max-w-[680px] text-[13px] leading-[22px] text-muted-foreground">
            一个属于你的网络文件夹，默认开通，挂在你所有节点上，员工像本地目录一样读写，会话结束也不会丢。每个项目有自己的目录，另有一个所有项目都能用的公共目录；交付物也存在这里。代码仍放在各节点的 Git 工作区。它是对象存储挂成的目录：Git 仓库、SQLite 这类数据库和 node_modules 不要放进去（不支持文件锁和硬链接）；别的节点改过的文件，最多几秒后可见。
          </p>
        </div>
      </header>

      <Loading v-if="fs.isLoading.value" />
      <template v-else-if="view">
        <Callout v-if="!view.configured" tone="warn">控制面板还没有配置共享存储（AGENT_TEAMS_FS_*），请管理员先配置。</Callout>

        <Callout v-else-if="!vol" tone="info">正在自动开通你的共享文件夹，通常一分钟内完成。</Callout>

        <template v-if="view.configured && vol">
          <Panel title="状态">
            <template #action>
              <el-tag v-if="state" :type="state.type" disable-transitions>{{ state.label }}</el-tag>
            </template>
            <dl class="grid grid-cols-[110px_1fr] gap-y-2 text-[13px]">
              <dt class="text-muted-foreground">存储位置</dt>
              <dd class="font-mono break-all">{{ vol.bucket }}/{{ vol.prefix }}</dd>
              <template v-if="used">
                <dt class="text-muted-foreground">已用空间</dt>
                <dd>{{ used }}</dd>
              </template>
              <dt class="text-muted-foreground">容量上限</dt>
              <dd>{{ vol.capacity_gb > 0 ? `${vol.capacity_gb} GB` : "不限" }}</dd>
              <dt class="text-muted-foreground">项目目录</dt>
              <dd class="font-mono">{{ view.project_dir }}</dd>
              <dt class="text-muted-foreground">公共目录</dt>
              <dd class="font-mono">{{ view.shared_dir }}</dd>
            </dl>
            <Callout v-if="vol.read_only" tone="warn">
              已超出容量上限，节点上的共享文件夹暂时只能读和删除、不能写入。删掉一些文件降到上限以内后，15 分钟内自动恢复写入。
            </Callout>
            <Callout v-if="vol.state === 'error'" tone="bad">
              开通失败：{{ vol.error || "未知原因" }}。系统会每 10 分钟自动重试，也可以现在重试。
              <el-button class="ml-2" size="small" :loading="retry.isPending.value" @click="retry.mutate()">重试</el-button>
            </Callout>
            <p class="text-[13px] leading-[22px] text-muted-foreground">
              员工会话里用环境变量 <code>$AGENT_TEAMS_PROJECT_DIR</code> 和 <code>$AGENT_TEAMS_SHARED_DIR</code> 找到这两个目录。节点的挂载状态在“节点”页查看。
            </p>
          </Panel>

          <Panel title="Docker 节点">
            <p class="text-[13px] leading-[22px] text-muted-foreground">
              节点自己挂载共享文件夹，Docker 里的节点也一样，但容器要能用 FUSE：在 compose.yml 的节点服务里加上
              <code>devices: ["/dev/fuse"]</code> 和 <code>cap_add: ["SYS_ADMIN"]</code>（宿主机开了 AppArmor 时再加
              <code>security_opt: ["apparmor:unconfined"]</code>）。挂载用的密钥只发给你的节点，不会出现在配置文件里。
            </p>
          </Panel>

        </template>
      </template>
    </div>
  </main>
</template>

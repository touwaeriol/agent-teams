<script setup lang="ts">
/**
 * The one-time token and the three ways to start the node with it: install-node.sh on the host, docker run,
 * or a compose.yml. The public Docker guide (/node-docker.md) is linked so it can be handed to an AI as is.
 */
import { computed, ref, watch } from "vue";
import type { Enrollment } from "@/api/views";
import CopyBlock from "@/components/common/CopyBlock.vue";
import InfoDialog from "@/components/common/InfoDialog.vue";
import { dockerGuideUrl, enrollModes, enrollText, type EnrollMode, loadEnrollMode, saveEnrollMode } from "./enroll";

const props = defineProps<{ title: string; data: Enrollment | null }>();
const open = defineModel<boolean>("open", { required: true });
const mode = ref<EnrollMode>(loadEnrollMode());
watch(mode, saveEnrollMode);
const current = computed(() => enrollModes.find((m) => m.id === mode.value) ?? enrollModes[0]);
const text = computed(() => (props.data ? enrollText(props.data, mode.value) : ""));
// The guide on the node registration address (agent-backend), not the page origin (a forwarder).
const guide = computed(() => props.data?.guide || dockerGuideUrl());
</script>

<template>
  <InfoDialog v-model:open="open" :title="title" description="选一种方式在节点机器上启动。令牌只显示这一次。" class="max-w-2xl">
    <div class="flex flex-col gap-3">
      <el-radio-group v-model="mode" size="small" aria-label="启动方式">
        <el-radio-button v-for="m in enrollModes" :key="m.id" :value="m.id">{{ m.label }}</el-radio-button>
      </el-radio-group>
      <p class="text-[13px] leading-5 text-muted-foreground">{{ current.hint }}</p>
      <CopyBlock :text="text" :class="mode === 'compose' ? 'max-h-[320px] overflow-y-auto scroll-thin' : ''" />
      <ul v-if="mode !== 'host'" class="list-disc space-y-1 pl-5 text-[13px] leading-5 text-muted-foreground">
        <li>Claude 授权：在配置里填 <code class="at-mono">CLAUDE_CODE_OAUTH_TOKEN</code> / <code class="at-mono">ANTHROPIC_API_KEY</code>，或启动后执行 <code class="at-mono">{{ current.exec }} claude login</code> 再重启容器。</li>
        <li>需要代理时加 <code class="at-mono">HTTPS_PROXY</code> / <code class="at-mono">HTTP_PROXY</code> / <code class="at-mono">NO_PROXY</code>，控制面板连接、git、Claude 和 Codex 都会用。</li>
        <li>登录、仓库和会话存在命名卷里，重建容器不会丢。守护进程更新在本页点“更新”即可。</li>
      </ul>
      <div class="flex flex-col gap-1.5 rounded-lg border px-3 py-2.5">
        <span class="text-[13px] font-medium">Docker 部署完整说明</span>
        <span class="text-xs leading-5 text-muted-foreground">公开文档，不用登录，可以直接把链接发给 AI 让它照着部署（把令牌单独告诉它）。</span>
        <CopyBlock :text="guide" />
      </div>
    </div>
  </InfoDialog>
</template>

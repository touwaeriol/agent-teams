<script setup lang="ts">
/** A host key the user should check by eye: type, SHA256 fingerprint and the command that prints it on the server. */
import { computed } from "vue";
import CopyBlock from "@/components/common/CopyBlock.vue";
import { fingerprintCommand } from "@/lib/servers";

const props = withDefaults(defineProps<{ type: string; fingerprint: string; label?: string; tone?: "normal" | "bad"; command?: boolean }>(), { label: undefined, tone: "normal", command: true });
const cmd = computed(() => fingerprintCommand(props.type));
</script>

<template>
  <div :class="['flex flex-col gap-1.5 rounded-lg border px-3 py-2.5', tone === 'bad' ? 'border-bad/40 bg-bad/5' : 'bg-well']" data-host-key>
    <span class="text-xs text-muted-foreground">{{ label ?? "主机密钥" }}<template v-if="type"> · {{ type }}</template></span>
    <span class="break-all font-mono text-[13px] font-semibold">{{ fingerprint }}</span>
    <template v-if="props.command">
      <span class="text-xs leading-5 text-muted-foreground">在服务器上运行下面的命令，核对输出里的 SHA256 指纹和这里一致：</span>
      <CopyBlock :text="cmd" />
    </template>
  </div>
</template>

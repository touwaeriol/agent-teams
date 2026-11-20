<script setup lang="ts">
/** The result of 测试连接: success or the reason, the host key to check, and 重置主机密钥 on a mismatch. */
import { CircleAlert, CircleCheck, ShieldAlert } from "lucide-vue-next";
import { computed } from "vue";
import type { ServerTestResult } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import { testView } from "@/lib/servers";
import HostKeyBox from "./HostKeyBox.vue";

const props = defineProps<{ result: ServerTestResult; resetting?: boolean }>();
const emit = defineEmits<{ reset: [] }>();
const v = computed(() => testView(props.result));
const TONE = { ok: "border-ok/40 bg-ok/5", warn: "border-warn/40 bg-warn/5", danger: "border-bad/50 bg-bad/5" };
const TEXT = { ok: "text-ok", warn: "text-warn", danger: "text-bad" };
</script>

<template>
  <div :class="['flex flex-col gap-2.5 rounded-xl border px-4 py-3', TONE[v.tone]]" data-test-result :data-tone="v.tone" role="status">
    <span :class="['flex items-center gap-2 font-medium', TEXT[v.tone]]">
      <CircleCheck v-if="v.tone === 'ok'" class="size-4" />
      <ShieldAlert v-else-if="v.tone === 'danger'" class="size-4" />
      <CircleAlert v-else class="size-4" />
      {{ v.title }}
    </span>
    <p v-if="v.detail" class="text-[13px] leading-5 text-foreground/85">{{ v.detail }}</p>
    <template v-if="v.expected">
      <HostKeyBox :type="v.hostKey?.type ?? ''" :fingerprint="v.expected" label="已固定的主机密钥" tone="bad" />
      <HostKeyBox v-if="v.hostKey" :type="v.hostKey.type" :fingerprint="v.hostKey.fingerprint" label="这次服务器出示的主机密钥" />
    </template>
    <HostKeyBox v-else-if="v.hostKey && (v.hostKey.pinnedNow || v.tone !== 'ok')" :type="v.hostKey.type" :fingerprint="v.hostKey.fingerprint" :label="v.hostKey.pinnedNow ? '已固定的主机密钥（本次）' : '服务器出示的主机密钥'" />
    <div v-if="v.canReset" class="flex flex-wrap items-center gap-2">
      <ConfirmAction
        title="重置主机密钥？"
        description="确认服务器确实重装过或换过密钥后再重置。重置后下一次测试连接会把服务器当前的主机密钥重新固定下来；如果是有人冒充，凭证会被发给冒充者。"
        confirm-label="重置"
        variant="danger"
        size="small"
        @confirm="emit('reset')"
      >
        {{ resetting ? "正在重置…" : "重置主机密钥" }}
      </ConfirmAction>
    </div>
  </div>
</template>

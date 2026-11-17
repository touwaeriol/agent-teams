<script setup lang="ts">
/**
 * 语音输入: the OpenAI key and model every user's mic button uses. The key is write-only (sealed
 * on the server, only its last four characters come back); 测试 mints a client secret with it.
 */
import { computed, reactive, ref, watch } from "vue";
import { useAction } from "@/api/mutations";
import { useVoiceAdmin, voice, voiceKeys, type VoiceAdmin, type VoiceAdminInput } from "@/api/voice";
import Card from "@/components/app/Card.vue";
import Field from "@/components/common/Field.vue";
import Loading from "@/components/common/Loading.vue";

const saved = useVoiceAdmin();
const form = reactive({ enabled: false, model: "", base_url: "", noise_reduction: "near_field", delay: "", transport: "relay", api_key: "" });
const result = ref<{ ok: boolean; text: string } | null>(null);

function load(v: VoiceAdmin | undefined) {
  if (!v) return;
  Object.assign(form, { enabled: v.enabled, model: v.model, base_url: v.base_url, noise_reduction: v.noise_reduction, delay: v.delay, transport: v.transport || "relay", api_key: "" });
}
watch(() => saved.data.value, load, { immediate: true });

const v = computed(() => saved.data.value);
const isWhisper = computed(() => (form.model || v.value?.default_model) === v.value?.default_model);
const canTest = computed(() => Boolean(form.api_key.trim() || v.value?.key_set));
const keyHint = computed(() => (v.value?.key_set ? `已保存，末四位 ${v.value.key_last4 || "****"}；留空则不改` : "OpenAI 的 API Key，保存后只显示末四位"));

const body = (patch: Partial<VoiceAdminInput> = {}): VoiceAdminInput => ({
  enabled: form.enabled,
  model: form.model.trim(),
  base_url: form.base_url.trim(),
  noise_reduction: form.noise_reduction as VoiceAdminInput["noise_reduction"],
  delay: (isWhisper.value ? form.delay : "") as VoiceAdminInput["delay"],
  transport: form.transport as VoiceAdminInput["transport"],
  ...(form.api_key.trim() ? { api_key: form.api_key.trim() } : {}),
  ...patch,
});

const save = useAction((b: VoiceAdminInput) => voice.save(b), { invalidate: [voiceKeys.admin, voiceKeys.config], success: "已保存" });
const clearKey = useAction(() => voice.save({ ...body(), enabled: false, clear_key: true, api_key: undefined }), {
  invalidate: [voiceKeys.admin, voiceKeys.config],
  success: "已清除 API Key，语音输入已关闭",
});
const test = useAction(() => voice.test({ api_key: form.api_key.trim() || undefined, model: form.model.trim(), base_url: form.base_url.trim() }), {
  onSuccess: () => (result.value = { ok: true, text: "可以用：OpenAI 签发了临时密钥" }),
});
watch(test.error, (e) => e && (result.value = { ok: false, text: e.message }));
watch(() => [form.api_key, form.model, form.base_url], () => (result.value = null));
</script>

<template>
  <Loading v-if="!v" />
  <Card v-else class="gap-4" data-testid="voice-settings-card">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <span class="flex flex-col gap-0.5">
        <span class="font-semibold">语音输入</span>
        <span class="text-xs text-muted-foreground">开启后，所有用户的输入框旁都有麦克风按钮，说话实时转成文字。声音经本平台服务器转发给 OpenAI，API Key 只在服务器上。</span>
      </span>
      <label class="flex items-center gap-2.5 text-[13px]">
        <el-switch v-model="form.enabled" aria-label="启用语音输入" />
        启用
      </label>
    </div>
    <div class="grid gap-4 sm:grid-cols-2">
      <Field label="服务商" hint="目前支持 OpenAI 实时转写。">
        <el-input model-value="OpenAI" disabled />
      </Field>
      <Field v-slot="{ id }" label="API Key" :hint="keyHint">
        <el-input :id="id" v-model="form.api_key" type="password" show-password autocomplete="off" :placeholder="v.key_set ? `••••${v.key_last4}` : 'sk-…'" />
      </Field>
      <Field v-slot="{ id }" label="模型" :hint="`默认 ${v.default_model}（OpenAI 的实时语音转写模型）`">
        <el-input :id="id" v-model="form.model" :placeholder="v.default_model" />
      </Field>
      <Field v-slot="{ id }" label="接口地址（可选）" :hint="`留空用 https://api.openai.com/v1${form.transport === 'webrtc' ? '；浏览器也会连这个地址' : ''}。改地址时要重新填写 API Key。`">
        <el-input :id="id" v-model="form.base_url" placeholder="https://api.openai.com/v1" />
      </Field>
      <Field v-slot="{ id }" label="连接方式" hint="直连时声音不经过本平台，但用户的浏览器连不上 OpenAI 时麦克风用不了。">
        <el-select :id="id" v-model="form.transport" data-testid="voice-transport">
          <el-option value="relay" label="经服务器中转（默认）" />
          <el-option value="webrtc" label="浏览器直连 OpenAI（更低延迟，需要浏览器能访问 api.openai.com）" />
        </el-select>
      </Field>
      <Field v-slot="{ id }" label="降噪">
        <el-select :id="id" v-model="form.noise_reduction">
          <el-option value="near_field" label="近场（耳麦、笔记本麦克风）" />
          <el-option value="far_field" label="远场（会议室麦克风）" />
          <el-option value="off" label="关闭" />
        </el-select>
      </Field>
      <Field v-if="isWhisper" v-slot="{ id }" label="延迟" hint="越低文字出来越快，越高越准。">
        <el-select :id="id" v-model="form.delay">
          <el-option value="" label="默认" />
          <el-option value="minimal" label="最低（minimal）" />
          <el-option value="low" label="低（low）" />
          <el-option value="medium" label="中（medium）" />
          <el-option value="high" label="高（high）" />
          <el-option value="xhigh" label="最高（xhigh）" />
        </el-select>
      </Field>
    </div>
    <div class="flex flex-wrap items-center gap-2">
      <el-button type="primary" :loading="save.isPending.value" @click="save.mutate(body())">保存</el-button>
      <el-button :disabled="!canTest" :loading="test.isPending.value" @click="test.mutate()">测试</el-button>
      <el-button v-if="v.key_set" text :loading="clearKey.isPending.value" @click="clearKey.mutate()">清除 API Key</el-button>
      <span v-if="result" :class="['text-[13px]', result.ok ? 'text-[#2E7D32]' : 'text-destructive']" data-testid="voice-test-result">{{ result.text }}</span>
      <span v-else-if="v.enabled && !v.usable" class="text-[13px] text-destructive">已启用但不可用：服务器读不出 API Key，请重新填写</span>
    </div>
  </Card>
</template>

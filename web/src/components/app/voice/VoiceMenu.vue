<script setup lang="ts">
/** The mic dropdown: microphone, language (自动 + searchable list), 按住录音. */
import { Check, Search } from "lucide-vue-next";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { askMicPermission, listMics, micErrorText, type MicDevice } from "@/lib/voice/audio";
import { filterLanguages, type LanguageOption, type VoicePrefs } from "@/lib/voice/prefs";

const props = defineProps<{ prefs: VoicePrefs; languages: LanguageOption[]; shortcut: string }>();
const emit = defineEmits<{ change: [patch: Partial<VoicePrefs>] }>();

const mics = ref<MicDevice[]>([]);
const labelled = ref(true);
const micNote = ref("");
const q = ref("");
const shown = computed(() => filterLanguages(props.languages, q.value));

async function refresh() {
  try {
    const r = await listMics();
    mics.value = r.devices;
    labelled.value = r.labelled || r.devices.length === 0;
    micNote.value = r.devices.length ? "" : "没有找到麦克风";
  } catch {
    micNote.value = "读不到麦克风列表";
  }
}

async function allow() {
  try {
    await askMicPermission();
    await refresh();
  } catch (e) {
    micNote.value = micErrorText(e);
  }
}

onMounted(() => {
  void refresh();
  navigator.mediaDevices?.addEventListener?.("devicechange", refresh);
});
onBeforeUnmount(() => navigator.mediaDevices?.removeEventListener?.("devicechange", refresh));

const row = "flex h-8 w-full shrink-0 items-center gap-2 rounded-md px-2 text-left text-[13px] hover:bg-raised";
</script>

<template>
  <div class="flex w-[260px] flex-col gap-1 text-[13px]" data-testid="voice-menu">
    <span class="px-2 pt-1 text-xs text-muted-foreground">麦克风</span>
    <div role="radiogroup" aria-label="麦克风" class="flex flex-col">
      <button type="button" role="radio" :aria-checked="!prefs.deviceId" :class="row" @click="emit('change', { deviceId: '' })">
        <span class="min-w-0 flex-1 truncate">系统默认</span>
        <Check v-if="!prefs.deviceId" class="size-4 shrink-0" />
      </button>
      <button
        v-for="m in mics"
        :key="m.deviceId"
        type="button"
        role="radio"
        :aria-checked="prefs.deviceId === m.deviceId"
        :class="row"
        @click="emit('change', { deviceId: m.deviceId })"
      >
        <span class="min-w-0 flex-1 truncate">{{ m.label }}</span>
        <Check v-if="prefs.deviceId === m.deviceId" class="size-4 shrink-0" />
      </button>
    </div>
    <p v-if="!labelled" class="flex items-center justify-between gap-2 px-2 text-xs text-muted-foreground">
      允许使用麦克风后显示设备名
      <button type="button" class="shrink-0 underline underline-offset-2 hover:text-foreground" @click="allow">允许</button>
    </p>
    <p v-if="micNote" class="px-2 text-xs text-muted-foreground">{{ micNote }}</p>

    <span class="mt-1 border-t px-2 pt-2 text-xs text-muted-foreground">语言</span>
    <label class="mx-1 flex h-8 items-center gap-1.5 rounded-md border border-input px-2">
      <Search class="size-3.5 text-muted-foreground" />
      <input v-model="q" class="min-w-0 flex-1 bg-transparent text-[13px] outline-none" placeholder="搜索语言" aria-label="搜索语言" />
    </label>
    <div role="radiogroup" aria-label="语言" class="scroll-thin flex max-h-[180px] flex-col overflow-y-auto">
      <button v-if="!q" type="button" role="radio" :aria-checked="prefs.language === 'auto'" :class="row" @click="emit('change', { language: 'auto' })">
        <span class="flex-1">自动</span>
        <span class="text-xs text-muted-foreground">识别说的语言</span>
        <Check v-if="prefs.language === 'auto'" class="size-4 shrink-0" />
      </button>
      <button
        v-for="l in shown"
        :key="l.code"
        type="button"
        role="radio"
        :aria-checked="prefs.language === l.code"
        :class="row"
        @click="emit('change', { language: l.code })"
      >
        <span class="min-w-0 flex-1 truncate">{{ l.name }}</span>
        <span v-if="l.zh !== l.name" class="text-xs text-muted-foreground">{{ l.zh }}</span>
        <Check v-if="prefs.language === l.code" class="size-4 shrink-0" />
      </button>
      <p v-if="q && !shown.length" class="px-2 py-1.5 text-xs text-muted-foreground">没有这个语言</p>
    </div>

    <label class="mt-1 flex items-center justify-between gap-2 border-t px-2 pb-1 pt-2">
      <span class="flex flex-col">
        <span>按住录音</span>
        <span class="text-xs text-muted-foreground">{{ prefs.pushToTalk ? `按住按钮或 ${shortcut} 说话，松开结束` : `点一下开始，再点一下结束（${shortcut}）` }}</span>
      </span>
      <el-switch :model-value="prefs.pushToTalk" aria-label="按住录音" @change="(v: string | number | boolean) => emit('change', { pushToTalk: Boolean(v) })" />
    </label>
  </div>
</template>

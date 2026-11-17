<script setup lang="ts">
/**
 * The mic dropdown (like the Claude app): 麦克风 rows with exactly one checked, a 语言 row that
 * opens a searchable language list in place, and 按住录音 with a switch.
 */
import { Check, ChevronLeft, ChevronRight, Search } from "lucide-vue-next";
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import { askMicPermission, listMics, micErrorText, selectedMic, type MicDevice } from "@/lib/voice/audio";
import { filterLanguages, languageLabel, type LanguageOption, type VoicePrefs } from "@/lib/voice/prefs";
import { cn } from "@/lib/utils";

const props = defineProps<{ prefs: VoicePrefs; languages: LanguageOption[]; shortcut: string }>();
const emit = defineEmits<{ change: [patch: Partial<VoicePrefs>] }>();

const mics = ref<MicDevice[]>([{ deviceId: "", label: "系统默认" }]);
const labelled = ref(true);
const micNote = ref("");
const view = ref<"main" | "language">("main");
const q = ref("");
const search = ref<HTMLInputElement | null>(null);
const shown = computed(() => filterLanguages(props.languages, q.value));
const checkedMic = computed(() => selectedMic(mics.value, props.prefs.deviceId));
const language = computed(() => languageLabel(props.languages, props.prefs.language));

async function refresh() {
  try {
    const r = await listMics();
    mics.value = r.devices;
    labelled.value = r.labelled || r.devices.length <= 1;
    micNote.value = r.devices.length > 1 ? "" : "没有找到麦克风";
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

function pickMic(m: MicDevice) {
  if (m.placeholder) return void allow();
  emit("change", { deviceId: m.deviceId });
}

function openLanguages() {
  view.value = "language";
  q.value = "";
  void nextTick(() => search.value?.focus());
}

function pickLanguage(code: string) {
  emit("change", { language: code });
  view.value = "main";
}

onMounted(() => {
  void refresh();
  navigator.mediaDevices?.addEventListener?.("devicechange", refresh);
});
onBeforeUnmount(() => navigator.mediaDevices?.removeEventListener?.("devicechange", refresh));

const row = "flex h-9 w-full shrink-0 items-center gap-2 rounded-lg px-2.5 text-left text-sm text-foreground hover:bg-raised";
const label = "px-2.5 pb-1 pt-1.5 text-xs text-muted-foreground";
</script>

<template>
  <div class="flex w-full flex-col text-sm" data-testid="voice-menu">
    <template v-if="view === 'main'">
      <span :class="label">麦克风</span>
      <div role="radiogroup" aria-label="麦克风" class="scroll-thin flex max-h-[240px] flex-col overflow-y-auto">
        <button
          v-for="m in mics"
          :key="m.deviceId"
          type="button"
          role="radio"
          :aria-checked="!m.placeholder && checkedMic === m.deviceId"
          :title="m.label"
          :class="cn(row, !m.placeholder && checkedMic === m.deviceId && 'bg-raised', m.placeholder && 'text-muted-foreground')"
          data-testid="voice-mic-row"
          @click="pickMic(m)"
        >
          <span class="min-w-0 flex-1 truncate">{{ m.label }}</span>
          <Check v-if="!m.placeholder && checkedMic === m.deviceId" class="size-4 shrink-0 text-[#2F6FDB]" />
        </button>
      </div>
      <p v-if="!labelled" class="flex items-center justify-between gap-2 px-2.5 py-1 text-xs text-muted-foreground">
        允许使用麦克风后显示设备名
        <button type="button" class="shrink-0 underline underline-offset-2 hover:text-foreground" @click="allow">允许</button>
      </p>
      <p v-if="micNote" class="px-2.5 py-1 text-xs text-muted-foreground">{{ micNote }}</p>

      <div class="mx-1 my-1 border-t" />
      <button type="button" :class="row" aria-haspopup="true" data-testid="voice-language-row" @click="openLanguages">
        <span class="flex-1">语言</span>
        <span class="truncate text-[13px] text-muted-foreground">{{ language }}</span>
        <ChevronRight class="size-4 shrink-0 text-muted-foreground" />
      </button>

      <div class="mx-1 my-1 border-t" />
      <label :class="cn(row, 'cursor-pointer hover:bg-transparent')" :title="prefs.pushToTalk ? `按住按钮或 ${shortcut} 说话，松开结束` : `点一下开始，再点一下结束（${shortcut}）`">
        <span class="flex-1">按住录音</span>
        <span class="text-xs text-muted-foreground">{{ shortcut }}</span>
        <el-switch :model-value="prefs.pushToTalk" size="small" aria-label="按住录音" @change="(v: string | number | boolean) => emit('change', { pushToTalk: Boolean(v) })" />
      </label>
    </template>

    <template v-else>
      <button type="button" :class="cn(row, 'font-medium')" data-testid="voice-language-back" @click="view = 'main'">
        <ChevronLeft class="size-4 shrink-0 text-muted-foreground" />
        <span class="flex-1">语言</span>
      </button>
      <label class="mx-1 my-1 flex h-8 items-center gap-1.5 rounded-lg border border-input px-2">
        <Search class="size-3.5 text-muted-foreground" />
        <input ref="search" v-model="q" class="min-w-0 flex-1 bg-transparent text-[13px] outline-none" placeholder="搜索语言" aria-label="搜索语言" />
      </label>
      <div role="radiogroup" aria-label="语言" class="scroll-thin flex max-h-[260px] flex-col overflow-y-auto">
        <button
          v-if="!q"
          type="button"
          role="radio"
          :aria-checked="prefs.language === 'auto'"
          :class="cn(row, prefs.language === 'auto' && 'bg-raised')"
          @click="pickLanguage('auto')"
        >
          <span class="flex-1">自动</span>
          <span class="text-xs text-muted-foreground">识别说的语言</span>
          <Check v-if="prefs.language === 'auto'" class="size-4 shrink-0 text-[#2F6FDB]" />
        </button>
        <button
          v-for="l in shown"
          :key="l.code"
          type="button"
          role="radio"
          :aria-checked="prefs.language === l.code"
          :class="cn(row, prefs.language === l.code && 'bg-raised')"
          @click="pickLanguage(l.code)"
        >
          <span class="min-w-0 flex-1 truncate">{{ l.name }}</span>
          <span v-if="l.zh !== l.name" class="text-xs text-muted-foreground">{{ l.zh }}</span>
          <Check v-if="prefs.language === l.code" class="size-4 shrink-0 text-[#2F6FDB]" />
        </button>
        <p v-if="q && !shown.length" class="px-2.5 py-1.5 text-xs text-muted-foreground">没有这个语言</p>
      </div>
    </template>
  </div>
</template>

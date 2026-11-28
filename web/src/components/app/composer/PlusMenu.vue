<script setup lang="ts">
import { $t } from "@/i18n";
/** The "+" menu of a message box: 添加文件或照片, 斜杠命令, 引用. */
import { AtSign, Paperclip, Plus, SquareSlash } from "lucide-vue-next";
import { ref } from "vue";
import DropdownMenu from "@/components/common/DropdownMenu.vue";
import AttachButton from "../AttachButton.vue";
import type { Attachments } from "../attachments";
import type { ComposerAssist } from "./useComposerAssist";

const props = withDefaults(defineProps<{ assist: ComposerAssist; a?: Attachments; size?: number }>(), { a: undefined, size: 32 });
const picker = ref<InstanceType<typeof AttachButton> | null>(null);
const run: Record<string, () => void> = {
  file: () => picker.value?.open(),
  slash: () => void props.assist.startSlash(),
  mention: () => void props.assist.startMention(),
};
</script>

<template>
  <DropdownMenu placement="top-start" popper-class="w-48" @command="(c: string) => run[c]?.()">
    <button
      type="button"
      :aria-label="$t('添加')"
      :title="$t('添加文件、斜杠命令或引用')"
      class="flex shrink-0 items-center justify-center rounded-lg text-muted-foreground hover:bg-raised hover:text-foreground aria-expanded:bg-raised"
      :style="{ width: `${size}px`, height: `${size}px` }"
    >
      <Plus class="size-[18px]" :stroke-width="1.8" />
    </button>
    <template #menu>
      <el-dropdown-item v-if="a" command="file"><Paperclip class="mr-2 size-4" />{{ $t("添加文件或照片") }}</el-dropdown-item>
      <el-dropdown-item command="slash"><SquareSlash class="mr-2 size-4" />{{ $t("斜杠命令") }}</el-dropdown-item>
      <el-dropdown-item command="mention"><AtSign class="mr-2 size-4" />{{ $t("引用") }}</el-dropdown-item>
    </template>
  </DropdownMenu>
  <AttachButton v-if="a" ref="picker" :a="a" hide-button />
</template>

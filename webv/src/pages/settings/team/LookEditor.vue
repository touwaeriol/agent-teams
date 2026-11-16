<script setup lang="ts">
/** The role's look: palette, outfit, accessory, or a random one, with the avatar preview. */
import { computed } from "vue";
import { roles as roleActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import type { Look, RoleView } from "@/api/views";
import Avatar from "@/components/app/avatar/Avatar.vue";
import type { OfficeLayout } from "@/office";
import { previewTags } from "@/lib/roles";
import ToggleChip from "./ToggleChip.vue";

const props = defineProps<{ role: RoleView; layout: OfficeLayout }>();
const emit = defineEmits<{ change: [look: Look] }>();
const tags = computed(() => previewTags(props.role));
const look = computed(() => props.role.look);
const set = (patch: Partial<Look>) => emit("change", { ...look.value, ...patch });
const random = useAction(() => roleActions.randomLook(props.role.role), { onSuccess: (l) => emit("change", l) });
</script>

<template>
  <div class="flex items-center gap-3.5 rounded-[10px] border border-[#EFEDE8] px-3 py-2.5">
    <div class="flex h-[84px] w-16 shrink-0 items-end justify-center rounded-[10px] bg-[#F7F2EA] pb-1">
      <Avatar :layout="layout" :look="look" :role="role.role" :tier="role.tier" :label="tags[0]" :scale="4" />
    </div>
    <div class="flex min-w-0 flex-1 flex-col gap-2">
      <span class="flex flex-wrap items-center gap-1.5">
        <span class="w-[30px] text-xs text-muted-foreground">颜色</span>
        <button
          v-for="hex in layout.looks.colors"
          :key="hex"
          type="button"
          :aria-label="`颜色 ${hex}`"
          :aria-pressed="look.color === hex"
          class="size-5 rounded-full border-2 p-0"
          :style="{ background: hex, borderColor: look.color === hex ? '#1F1E1C' : '#FFFFFF' }"
          @click="set({ color: hex })"
        />
      </span>
      <span class="flex flex-wrap items-center gap-1.5">
        <span class="w-[30px] text-xs text-muted-foreground">衣着</span>
        <ToggleChip v-for="(piece, key) in layout.looks.outfits" :key="key" :on="look.outfit === key" @click="set({ outfit: String(key) })">{{ piece.label }}</ToggleChip>
      </span>
      <span class="flex flex-wrap items-center gap-1.5">
        <span class="w-[30px] text-xs text-muted-foreground">配饰</span>
        <ToggleChip v-for="(piece, key) in layout.looks.accessories" :key="key" :on="look.accessory === key" @click="set({ accessory: String(key) })">{{ piece.label }}</ToggleChip>
        <button type="button" :disabled="random.isPending.value" class="ml-auto h-6 rounded-md border border-dashed border-[#C9C5BC] bg-white px-2 text-xs" @click="random.mutate()">随机一个</button>
      </span>
      <span class="text-[11px] text-[#8A877F]">同岗位的成员衣着一致，发色和编号自动区分（{{ tags[0] }}、{{ tags[1] }}…）</span>
    </div>
  </div>
</template>

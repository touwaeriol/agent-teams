<script setup lang="ts">
// 通用: name and icon, intro, goal (context), models, auto-resume, archive, delete.
import { Archive, ArchiveRestore } from "lucide-vue-next";
import type { ProjectView } from "@/api/views";
import DeleteRow from "./DeleteRow.vue";
import Divider from "./Divider.vue";
import GoalField from "./GoalField.vue";
import Heading from "./Heading.vue";
import InlineText from "./InlineText.vue";
import ModelFields from "./ModelFields.vue";
import NameAndIcon from "./NameAndIcon.vue";
import Row from "./Row.vue";
import SwitchRow from "./SwitchRow.vue";
import { useUpdate } from "./useUpdate";

const props = defineProps<{ project: ProjectView }>();
const emit = defineEmits<{ close: [] }>();
const update = useUpdate(() => props.project.id);
</script>

<template>
  <div class="flex flex-col">
    <Heading>通用</Heading>
    <Row title="名称和图标"><NameAndIcon :project="project" /></Row>
    <Row title="一句话简介">
      <InlineText :value="project.description" placeholder="在项目列表里显示" label="一句话简介" @save="(description: string) => update.mutate({ description })" />
    </Row>
    <GoalField :project="project" />
    <ModelFields :project="project" />
    <Divider />
    <SwitchRow
      title="使用限额重置后自动继续讨论串"
      desc="Claude 使用限额用完时，受影响的讨论串会自动暂停；打开后到重置时间自动继续。"
      :checked="project.auto_resume"
      @change="(v: boolean) => update.mutate({ auto_resume: v })"
    />
    <Row title="归档项目" :desc="project.archived ? '已归档：侧栏收在“已归档”里。' : '从侧栏收起，随时可以恢复。'">
      <el-button size="small" @click="update.mutate({ archived: !project.archived })">
        <ArchiveRestore v-if="project.archived" /><Archive v-else />
        {{ project.archived ? "恢复" : "归档" }}
      </el-button>
    </Row>
    <span class="pt-3 text-[13px] text-muted-foreground">危险区域</span>
    <DeleteRow :project="project" @close="emit('close')" />
  </div>
</template>

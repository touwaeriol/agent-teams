<script setup lang="ts">
/** A chat card by message kind (todos, plan, artifact, resumed, image, relay); other kinds draw nothing. */
import type { MessageView } from "@/api/views";
import ArtifactCard from "./ArtifactCard.vue";
import ImageCard from "./ImageCard.vue";
import PlanCard from "./PlanCard.vue";
import RelayCard from "./RelayCard.vue";
import ResumedNote from "./ResumedNote.vue";
import TodoCard from "./TodoCard.vue";

defineProps<{ message: MessageView; pid: string }>();
</script>

<template>
  <TodoCard v-if="message.kind === 'todos'" :message="message" />
  <PlanCard v-else-if="message.kind === 'plan'" :message="message" />
  <ArtifactCard v-else-if="message.kind === 'artifact'" :message="message" :pid="pid" />
  <ResumedNote v-else-if="message.kind === 'resumed'" :message="message" />
  <ImageCard v-else-if="message.kind === 'image'" :message="message" />
  <RelayCard v-else-if="message.kind === 'relay' && message.meta.relay" :message="message" :pid="pid" />
</template>

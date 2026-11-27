<script setup lang="ts">
// 我的记忆: memory every project of mine reads (preferences and habits: "回复用中文"). The
// coordinator writes what I say across projects; with the shared folder on it is the files in
// shared/.memory/ that employees can edit too.
import Panel from "@/components/common/Panel.vue";
import NavButton from "@/components/shell/NavButton.vue";
import MemoryFileList from "@/pages/projectSettings/MemoryFileList.vue";
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[960px] flex-col gap-5">
      <header class="flex items-start gap-1">
        <NavButton />
        <div class="flex flex-col gap-1.5">
          <h1 class="text-2xl font-semibold">我的记忆</h1>
          <p class="text-sm text-muted-foreground">所有项目共用的偏好和习惯，比如“回复用中文”“PR 尽量小”。每个项目的每个会话开始时都会读到 MEMORY.md；和某个项目的记忆冲突时，以项目里的为准。你在任何项目里说“以后都……”，统筹会记到这里。开了共享文件夹时，它们是 shared/.memory/ 里的文件。</p>
        </div>
      </header>
      <Panel title="记忆文件">
        <div class="flex flex-col">
          <MemoryFileList :pid="null" label="所有项目" heading="每个会话都会读" />
        </div>
      </Panel>
    </div>
  </main>
</template>

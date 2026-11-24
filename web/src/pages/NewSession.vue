<script setup lang="ts">
// 新建会话: what to do, the repositories (each from a branch), and who does it (a member role,
// its model) on which node. The session opens at once and the employee starts on the message.
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { sessions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useNodes, useRoles } from "@/api/queries";
import Card from "@/components/app/Card.vue";
import CardTitle from "@/components/app/CardTitle.vue";
import ModelPicker from "@/components/app/models/ModelPicker.vue";
import SessionRepos from "@/components/app/session/SessionRepos.vue";
import { sessionReposPayload, type SessionRepo } from "@/components/app/session/session";
import Field from "@/components/common/Field.vue";
import Spinner from "@/components/common/Spinner.vue";
import NavButton from "@/components/shell/NavButton.vue";
import { MODEL_FORMAT_HINT, validModel, type Runtime } from "@/lib/models";
import { toast } from "@/lib/toast";

const router = useRouter();
const roles = useRoles();
const nodes = useNodes();
const message = ref("");
const repos = ref<SessionRepo[]>([]);
const role = ref("");
const model = ref("");
const node = ref("");
const members = computed(() => (roles.data.value?.roles ?? []).filter((r) => r.tier === "member"));
watch(
  members,
  (list) => {
    if (!role.value && list.length) role.value = list[0].role;
  },
  { immediate: true },
);
const picked = computed(() => members.value.find((r) => r.role === role.value));
const runtime = computed<Runtime>(() => (picked.value?.runtime === "codex" ? "codex" : "claude"));
const nodeList = computed(() => nodes.data.value ?? []);
const ready = computed(() => message.value.trim() !== "" && repos.value.length > 0 && role.value !== "");

const create = useAction(
  () =>
    sessions.create({
      message: message.value.trim(),
      repos: sessionReposPayload(repos.value),
      role: role.value,
      model: model.value.trim() || undefined,
      node_id: node.value || undefined,
    }),
  { invalidate: [keys.projects], success: (s) => `会话「${s.project.name}」已开始`, onSuccess: (s) => void router.push(`/sessions/${s.project.id}`) },
);
const submit = () => {
  if (!ready.value) return;
  if (!validModel(model.value)) return void toast.error(MODEL_FORMAT_HINT);
  create.mutate();
};
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-10">
    <form class="mx-auto flex max-w-[760px] flex-col gap-6" @submit.prevent="submit">
      <div class="flex items-start gap-1">
        <NavButton />
        <div class="flex flex-col gap-1.5">
          <h1 class="text-2xl font-semibold">新建会话</h1>
          <p class="text-[13px] leading-[22px] text-muted-foreground">选几个仓库，它们会被并排拉到同一个文件夹里，一个员工直接在里面干活，你直接和它对话。没有统筹，也不用组队。</p>
        </div>
      </div>
      <Card class="gap-4">
        <Field v-slot="{ id }" label="要做什么" hint="会原样作为第一条消息发给员工；第一行就是会话标题。">
          <el-input :id="id" v-model="message" type="textarea" :rows="5" maxlength="50000" placeholder="例如：修复登录后的跳转，前端在 org/web，接口在 org/api" />
        </Field>
      </Card>
      <Card class="gap-4">
        <CardTitle>代码仓库</CardTitle>
        <p class="text-[13px] text-muted-foreground">每个仓库是工作目录下的一个子文件夹；员工会从选的分支切出自己的会话分支（session/…），改完推上去、开 PR。会话里随时可以增删仓库。</p>
        <SessionRepos v-model="repos" />
      </Card>
      <Card class="gap-4">
        <CardTitle>谁来做</CardTitle>
        <div class="grid gap-3 sm:grid-cols-3">
          <Field v-slot="{ id }" label="岗位" hint="按这个成员岗位的配置起员工">
            <el-select :id="id" v-model="role" placeholder="选择岗位" class="at-select-36">
              <el-option v-for="r in members" :key="r.role" :value="r.role" :label="r.title || r.role" />
            </el-select>
          </Field>
          <Field v-slot="{ id }" label="模型">
            <ModelPicker :id="id" v-model="model" live :runtime="runtime" label="模型" placeholder="跟随岗位配置" class="at-select-36" />
          </Field>
          <Field v-slot="{ id }" label="节点" hint="自动：挑剩余用量最多的在线节点">
            <el-select :id="id" v-model="node" class="at-select-36">
              <el-option value="" label="自动分配" />
              <el-option v-for="n in nodeList" :key="n.id" :value="n.id" :label="n.online ? n.name : `${n.name}（离线）`" />
            </el-select>
          </Field>
        </div>
        <p v-if="roles.data.value && members.length === 0" class="text-[13px] text-muted-foreground">团队里还没有成员岗位，先在 设置 → 团队 里添加一个。</p>
      </Card>
      <div class="flex justify-end gap-2">
        <el-button size="large" @click="router.back()">取消</el-button>
        <el-button type="primary" size="large" native-type="submit" :disabled="!ready || create.isPending.value">
          <Spinner v-if="create.isPending.value" /> 开始会话
        </el-button>
      </div>
    </form>
  </main>
</template>

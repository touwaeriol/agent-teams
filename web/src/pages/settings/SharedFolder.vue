<script setup lang="ts">
// 共享文件夹: the user's JuiceFS volume, mounted on every node of theirs. Each project gets
// projects/<id>, and shared/ is open to all their projects. Switched on once here; nodes mount it
// themselves (fs_mount) or through the Docker volume plugin (fs_external).
import { computed, ref } from "vue";
import { sharedFolder } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useSharedFolder } from "@/api/queries";
import type { components } from "@/api/schema";
import Callout from "@/components/common/Callout.vue";
import Field from "@/components/common/Field.vue";
import Loading from "@/components/common/Loading.vue";
import Panel from "@/components/common/Panel.vue";
import NavButton from "@/components/shell/NavButton.vue";

type Docker = components["schemas"]["FsDockerVolume"];
type Kind = "platform" | "s3" | "juicefs";

const KIND_LABEL: Record<Kind, string> = { platform: "平台默认存储", s3: "自己的 S3", juicefs: "自己的 JuiceFS" };

const STATE: Record<string, { label: string; type: "success" | "warning" | "danger" | "info" }> = {
  provisioning: { label: "开通中", type: "warning" },
  ready: { label: "可用", type: "success" },
  error: { label: "出错", type: "danger" },
  deleting: { label: "删除中", type: "info" },
};

const fs = useSharedFolder();
const view = computed(() => fs.data.value);
const vol = computed(() => view.value?.volume);
const state = computed(() => STATE[vol.value?.state ?? ""] ?? null);
const docker = ref<Docker | null>(null);
const typed = ref("");
const kind = ref<Kind>("platform");
const form = ref({ s3_endpoint: "", bucket: "", access_key: "", secret_key: "", meta_url: "", meta_password: "" });
const showSetup = computed(() => !vol.value || vol.value.state === "error");
const formProblem = computed(() => {
  const f = form.value;
  if (kind.value === "s3" && (!f.s3_endpoint || !f.bucket || !f.access_key || !f.secret_key)) return "请填完 S3 地址、bucket 和密钥";
  if (kind.value === "juicefs" && !f.meta_url) return "请填元数据地址";
  return "";
});
function body() {
  const f = form.value;
  if (kind.value === "s3") return { kind: "s3" as const, s3_endpoint: f.s3_endpoint, bucket: f.bucket, access_key: f.access_key, secret_key: f.secret_key };
  if (kind.value === "juicefs") return { kind: "juicefs" as const, meta_url: f.meta_url, ...(f.meta_password ? { meta_password: f.meta_password } : {}) };
  return undefined;
}

const enable = useAction(() => sharedFolder.enable(body()), {
  invalidate: [keys.sharedFolder],
  success: "共享文件夹已开通",
  onSuccess: () => (form.value = { s3_endpoint: "", bucket: "", access_key: "", secret_key: "", meta_url: "", meta_password: "" }),
});
const remove = useAction(() => sharedFolder.remove(typed.value), {
  invalidate: [keys.sharedFolder],
  success: "共享文件夹已删除",
  onSuccess: () => ((typed.value = ""), (docker.value = null)),
});
const makeDocker = useAction(() => sharedFolder.dockerVolume() as Promise<Docker>, { onSuccess: (d) => (docker.value = d) });
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[960px] flex-col gap-5">
      <header class="flex items-start gap-1">
        <NavButton />
        <div class="flex flex-col gap-1.5">
          <h1 class="text-2xl font-semibold">共享文件夹</h1>
          <p class="max-w-[680px] text-[13px] leading-[22px] text-muted-foreground">
            一个属于你的网络文件夹，挂在你所有节点上，员工像本地目录一样读写，会话结束也不会丢。每个项目有自己的目录，另有一个所有项目都能用的公共目录。代码仍放在各节点的 Git 工作区。
          </p>
        </div>
      </header>

      <Loading v-if="fs.isLoading.value" />
      <template v-else-if="view">
        <Callout v-if="!view.configured" tone="warn">控制面板还没有配置共享存储（AGENT_TEAMS_FS_*），请管理员先配置。</Callout>

        <template v-else>
          <Panel v-if="showSetup" :title="vol ? '重新设置存储' : '开通共享文件夹'">
            <p class="text-[13px] leading-[22px] text-muted-foreground">
              默认用平台的存储，开通即用；也可以用你自己的 S3 或 JuiceFS。节点连上后会自动挂载。SQLite 这类数据库可以放进去，但同一个库同一时间只能由一个会话写入。
            </p>
            <Callout v-if="vol?.state === 'error'" tone="bad">{{ vol.error || "开通失败" }}</Callout>
            <el-radio-group v-model="kind" aria-label="存储">
              <el-radio-button v-for="(label, k) in KIND_LABEL" :key="k" :value="k">{{ label }}</el-radio-button>
            </el-radio-group>
            <div v-if="kind === 's3'" class="grid gap-3 sm:grid-cols-2">
              <Field label="S3 地址"><el-input v-model="form.s3_endpoint" placeholder="https://s3.example.com" /></Field>
              <Field label="Bucket"><el-input v-model="form.bucket" placeholder="my-bucket" /></Field>
              <Field label="Access Key"><el-input v-model="form.access_key" autocomplete="off" /></Field>
              <Field label="Secret Key"><el-input v-model="form.secret_key" type="password" show-password autocomplete="new-password" /></Field>
              <p class="text-[12px] leading-5 text-muted-foreground sm:col-span-2">我们会把它格式化成 JuiceFS（文件放在 bucket 的 {{ "at-…/" }} 前缀下），元数据存在平台。密钥只用于挂载，保存后不再显示。删除共享文件夹会清掉这个前缀下的数据。</p>
            </div>
            <div v-else-if="kind === 'juicefs'" class="grid gap-3 sm:grid-cols-2">
              <Field label="元数据地址" class="sm:col-span-2"><el-input v-model="form.meta_url" placeholder="redis://host:6379/1 或 postgres://user@host:5432/db" /></Field>
              <Field label="元数据密码（可选）"><el-input v-model="form.meta_password" type="password" show-password autocomplete="new-password" /></Field>
              <p class="text-[12px] leading-5 text-muted-foreground sm:col-span-2">填一个已经 juicefs format 过的卷。你的节点要能连到这个地址；平台不会动它的数据，删除共享文件夹只是不再挂载。</p>
            </div>
            <p class="text-[12px] text-muted-foreground">换存储不会迁移文件。</p>
            <div>
              <el-button type="primary" :disabled="Boolean(formProblem)" :loading="enable.isPending.value" @click="enable.mutate()">{{ vol ? "重试" : "开通" }}</el-button>
              <span v-if="formProblem" class="ml-3 text-[12px] text-muted-foreground">{{ formProblem }}</span>
            </div>
          </Panel>
        </template>

        <template v-if="view.configured && vol">
          <Panel title="状态">
            <template #action>
              <el-tag v-if="state" :type="state.type" disable-transitions>{{ state.label }}</el-tag>
            </template>
            <dl class="grid grid-cols-[110px_1fr] gap-y-2 text-[13px]">
              <dt class="text-muted-foreground">卷名</dt>
              <dd class="font-mono">{{ vol.volume }}</dd>
              <dt class="text-muted-foreground">存储</dt>
              <dd>
                {{ KIND_LABEL[vol.backend] }}
                <span v-if="vol.summary.endpoint" class="font-mono text-muted-foreground">{{ vol.summary.endpoint }}/{{ vol.summary.bucket }}</span>
                <span v-if="vol.summary.meta" class="font-mono text-muted-foreground">{{ vol.summary.meta }}</span>
              </dd>
              <dt v-if="vol.backend !== 'juicefs'" class="text-muted-foreground">容量上限</dt>
              <dd v-if="vol.backend !== 'juicefs'">{{ vol.capacity_gb > 0 ? `${vol.capacity_gb} GB` : "不限" }}</dd>
              <dt class="text-muted-foreground">项目目录</dt>
              <dd class="font-mono">{{ view.project_dir }}</dd>
              <dt class="text-muted-foreground">公共目录</dt>
              <dd class="font-mono">{{ view.shared_dir }}</dd>
            </dl>
            <p class="text-[13px] leading-[22px] text-muted-foreground">
              员工会话里用环境变量 <code>$AGENT_TEAMS_PROJECT_DIR</code> 和 <code>$AGENT_TEAMS_SHARED_DIR</code> 找到这两个目录。节点的挂载状态在“节点”页查看。
            </p>
          </Panel>

          <Panel v-if="vol.state === 'ready'" title="Docker 节点">
            <p class="text-[13px] leading-[22px] text-muted-foreground">
              装在宿主机上的节点会自己挂载。Docker 节点建议用 JuiceFS 官方卷插件由宿主机挂载，节点容器不需要任何特权，也拿不到存储密码。
            </p>
            <div><el-button :loading="makeDocker.isPending.value" @click="makeDocker.mutate()">生成 Docker 卷配置</el-button></div>
            <template v-if="docker">
              <Callout tone="warn">下面的配置含存储密码，只放在节点所在的宿主机上，不要提交到代码仓库。</Callout>
              <p class="text-[13px] font-medium">1. 在宿主机上装一次插件</p>
              <pre class="overflow-x-auto rounded-lg bg-raised p-3 text-[12px] leading-5">{{ docker.plugin }}</pre>
              <p class="text-[13px] font-medium">2. 合进节点的 compose.yml，然后 docker compose up -d</p>
              <pre class="overflow-x-auto rounded-lg bg-raised p-3 text-[12px] leading-5">{{ docker.compose }}</pre>
              <p class="text-[13px] text-muted-foreground">容器里挂在 <code>{{ docker.mount }}</code>。</p>
            </template>
          </Panel>

          <Panel v-if="vol.state !== 'deleting'" title="删除" tone="danger">
            <p class="text-[13px] leading-[22px] text-muted-foreground">
              {{ vol.backend === "juicefs" ? "删除后节点不再挂载，你的 JuiceFS 和里面的文件不受影响。" : "删除会清空卷里的所有文件，无法恢复。" }}想换存储也要先删除（文件不迁移）。确认请输入卷名 <span class="font-mono">{{ vol.volume }}</span>。</p>
            <div class="flex flex-wrap gap-2">
              <el-input v-model="typed" class="max-w-[280px]" :placeholder="vol.volume" aria-label="输入卷名确认删除" />
              <el-button type="danger" :disabled="typed !== vol.volume" :loading="remove.isPending.value" @click="remove.mutate()">删除共享文件夹</el-button>
            </div>
          </Panel>
        </template>
      </template>
    </div>
  </main>
</template>

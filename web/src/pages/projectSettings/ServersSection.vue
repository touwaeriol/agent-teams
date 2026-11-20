<script setup lang="ts">
// 环境 → 服务器: which of the user's servers this project uses, and per server which roles' employees
// may reach it. The coordinator, the lead and read-only roles cannot be ticked (the server refuses
// them too). Every tick saves at once (PUT replaces the project's whole list).
import { computed } from "vue";
import { RouterLink } from "vue-router";
import { servers as serverActions } from "@/api/actions";
import { useQueryClient } from "@tanstack/vue-query";
import { useAction } from "@/api/mutations";
import { keys, useProjectServers } from "@/api/queries";
import Loading from "@/components/common/Loading.vue";
import { endpoint, grantableOnly, roleReason, toggleRole, toggleServer, type Grant } from "@/lib/servers";
import Heading from "./Heading.vue";
import ServerAuditSection from "./ServerAuditSection.vue";

const props = defineProps<{ projectId: string }>();
const view = useProjectServers(() => props.projectId);
const grants = computed<Grant[]>(() => (view.data.value?.servers ?? []).map((x) => ({ server_id: x.server.id, roles: [...x.roles] })));
const grantOf = (id: string) => grants.value.find((g) => g.server_id === id);
const roles = computed(() => view.data.value?.roles ?? []);
// The PUT answers with the new view: it goes straight into the cache, so the next tick builds on
// it rather than on a stale GET (two quick ticks must not undo each other). Ticking is disabled
// while a save or a refetch is in flight.
const queryClient = useQueryClient();
const put = useAction((list: Grant[]) => serverActions.setProjectServers(props.projectId, list), {
  invalidate: [keys.servers],
  onSuccess: (data) => queryClient.setQueryData(keys.projectServers(props.projectId), data),
});
const busy = computed(() => put.isPending.value || view.isFetching.value);
const save = (list: Grant[]) => !busy.value && put.mutate(grantableOnly(list, roles.value));
</script>

<template>
  <div class="flex flex-col gap-3 pt-6" data-section="servers">
    <Heading>服务器</Heading>
    <p class="-mt-2 text-[13px] leading-5 text-muted-foreground">
      勾选这个项目要用的服务器，再勾选哪些岗位的员工能访问。员工在会话里用 <code class="font-mono">ssh 名称</code> 连接，经主节点登录，拿不到密码或私钥。服务器在
      <RouterLink to="/settings/servers" class="text-foreground underline underline-offset-2">设置 → 服务器</RouterLink> 里添加。
    </p>
    <Loading v-if="view.isLoading.value" />
    <p v-else-if="view.data.value && view.data.value.available.length === 0" class="rounded-lg border border-dashed px-4 py-5 text-center text-[13px] text-muted-foreground">
      还没有服务器。先到 <RouterLink to="/settings/servers" class="text-foreground underline underline-offset-2">设置 → 服务器</RouterLink> 添加。
    </p>
    <div v-for="s in view.data.value?.available ?? []" :key="s.id" class="flex flex-col gap-2.5 rounded-xl border px-4 py-3" :data-project-server="s.name">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
        <el-checkbox :model-value="Boolean(grantOf(s.id))" :disabled="busy" @update:model-value="(v) => save(toggleServer(grants, s.id, Boolean(v)))">
          <span class="font-mono font-semibold">{{ s.name }}</span>
        </el-checkbox>
        <span class="font-mono text-xs text-muted-foreground">{{ endpoint(s) }}</span>
        <span v-if="!s.host_key_fp" class="text-xs text-warn">主机密钥还没固定，先在设置里测试连接</span>
      </div>
      <div v-if="grantOf(s.id)" class="flex flex-col gap-1.5 pl-6">
        <span class="text-xs text-muted-foreground">能访问的岗位</span>
        <div class="flex flex-wrap gap-x-4 gap-y-1">
          <el-tooltip v-for="r in roles" :key="r.role" :content="roleReason(r)" :disabled="r.grantable" placement="top">
            <span :data-role="r.role" :data-grantable="r.grantable">
              <el-checkbox
                :model-value="grantOf(s.id)?.roles.includes(r.role)"
                :disabled="!r.grantable || busy"
                @update:model-value="(v) => save(toggleRole(grants, s.id, r.role, Boolean(v)))"
              >
                {{ r.title || r.role }}<span class="ml-1 font-mono text-xs text-muted-foreground">{{ r.role }}</span>
              </el-checkbox>
            </span>
          </el-tooltip>
        </div>
        <span v-if="grantOf(s.id)?.roles.length === 0" class="text-xs text-warn">还没勾选岗位：现在谁都连不上它。</span>
        <span class="text-xs leading-5 text-muted-foreground">统筹不拿服务器凭证；组长和审查、决策这类只读岗位的会话不能执行 ssh，所以不能勾选。</span>
      </div>
    </div>
    <ServerAuditSection :project-id="projectId" :servers="view.data.value?.servers.map((x) => x.server) ?? []" />
  </div>
</template>

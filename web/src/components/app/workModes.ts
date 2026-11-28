import { $t } from "@/i18n";
export const WORK_MODES = [
  { id: "worktree" as const, title: $t("worktree（推荐）"), desc: $t("每个节点只克隆一份仓库；每个讨论串一个 worktree，讨论串里的每个成员再各开一个。省磁盘、开工快，并行互不干扰。") },
  { id: "clone" as const, title: $t("独立克隆"), desc: $t("每个成员一份完整克隆。最稳，但占磁盘，适合超大仓库或子模块很多的情况。") },
];

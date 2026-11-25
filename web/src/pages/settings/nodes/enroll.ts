// How a new node is started: host install script, docker run, or docker compose.
import type { Enrollment } from "@/api/views";

export type EnrollMode = "host" | "docker" | "compose";

export const enrollModes: { id: EnrollMode; label: string; field: "install" | "docker" | "compose"; hint: string; exec: string }[] = [
  { id: "host", label: "宿主机", field: "install", exec: "", hint: "在 Linux 机器上直接运行，装成 systemd 用户服务，用机器上已登录的 Claude Code / Codex。" },
  { id: "docker", label: "Docker", field: "docker", exec: "docker exec -it <容器名>", hint: "装了 Docker 的机器上运行这条命令。镜像里带好了守护进程、Claude Code、Codex、git 和 gh。" },
  { id: "compose", label: "Docker Compose", field: "compose", exec: "docker compose exec node", hint: "保存为 compose.yml（含令牌，建议 chmod 600），在同一目录执行 docker compose up -d。代理、授权、资源上限的写法在注释里。" },
];

const KEY = "at.nodes.enrollMode";

export function loadEnrollMode(): EnrollMode {
  try {
    const v = localStorage.getItem(KEY);
    return enrollModes.some((m) => m.id === v) ? (v as EnrollMode) : "host";
  } catch {
    return "host";
  }
}

export function saveEnrollMode(mode: EnrollMode) {
  try {
    localStorage.setItem(KEY, mode);
  } catch {
    /* private mode: not remembered */
  }
}

/** The public Docker guide, served by the console next to install-node.sh. */
export function dockerGuideUrl(origin = window.location.origin): string {
  return `${origin}/node-docker.md`;
}

/** The command or file text for a mode. */
export function enrollText(data: Enrollment, mode: EnrollMode): string {
  return data[enrollModes.find((m) => m.id === mode)?.field ?? "install"];
}

import { projects } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ProjectUpdate } from "@/api/views";

/** PATCH the project; switches and selects save at once. */
export function useUpdate(pid: () => string, success?: string) {
  return useAction((body: ProjectUpdate) => projects.update(pid(), body), { invalidate: () => [keys.project(pid()), keys.projects], success });
}

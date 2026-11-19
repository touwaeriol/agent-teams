import { projects } from "@/api/actions";
import { useQueryClient } from "@tanstack/vue-query";
import { useAction } from "@/api/mutations";
import { conversationModels, keys } from "@/api/queries";
import type { ProjectUpdate } from "@/api/views";

/** PATCH the project; switches and selects save at once. A model change refreshes the models the chat boxes show. */
export function useUpdate(pid: () => string, success?: string) {
  const client = useQueryClient();
  return useAction((body: ProjectUpdate) => projects.update(pid(), body), {
    invalidate: () => [keys.project(pid()), keys.projects],
    success,
    onSuccess: (_, body) => {
      if ("coordinator_model" in body || "thread_model" in body || "thread_codex_model" in body) void client.invalidateQueries(conversationModels);
    },
  });
}

import { useMutation, useQueryClient, type QueryKey } from "@tanstack/vue-query";
import { toast } from "@/lib/toast";

export function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export interface ActionOptions<T, V> {
  invalidate?: QueryKey[] | (() => QueryKey[]);
  success?: string | ((data: T) => string);
  onSuccess?: (data: T, vars: V) => void;
}

/**
 * A mutation with the platform's defaults: errors become a toast, listed
 * queries are refreshed and an optional success message is shown.
 * `invalidate` may be a function when the keys depend on reactive state.
 */
export function useAction<T = unknown, V = void>(fn: (vars: V) => Promise<T>, options: ActionOptions<T, V> = {}) {
  const client = useQueryClient();
  return useMutation<T, Error, V>({
    mutationFn: fn,
    onSuccess: (data, vars) => {
      const keys = typeof options.invalidate === "function" ? options.invalidate() : options.invalidate;
      keys?.forEach((queryKey) => client.invalidateQueries({ queryKey }));
      const message = typeof options.success === "function" ? options.success(data) : options.success;
      if (message) toast.success(message);
      options.onSuccess?.(data, vars);
    },
    onError: (e) => toast.error(errorText(e)),
  });
}

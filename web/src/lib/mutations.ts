import { useMutation, useQueryClient, type QueryKey } from "@tanstack/react-query";
import { toast } from "sonner";
import { errorText } from "@/components/common";

/**
 * A mutation with the platform's defaults: errors become a toast, listed
 * queries are refreshed and an optional success message is shown.
 */
export function useAction<T = unknown, V = void>(
  fn: (vars: V) => Promise<T>,
  options: { invalidate?: QueryKey[]; success?: string | ((data: T) => string); onSuccess?: (data: T, vars: V) => void } = {},
) {
  const client = useQueryClient();
  return useMutation<T, Error, V>({
    mutationFn: fn,
    onSuccess: (data, vars) => {
      options.invalidate?.forEach((queryKey) => client.invalidateQueries({ queryKey }));
      const message = typeof options.success === "function" ? options.success(data) : options.success;
      if (message) toast.success(message);
      options.onSuccess?.(data, vars);
    },
    onError: (e) => toast.error(errorText(e)),
  });
}

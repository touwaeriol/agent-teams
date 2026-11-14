import { useEffect } from "react";
import { useSearchParams } from "react-router-dom";
import { toast } from "sonner";

/**
 * Show a message that came back in the URL after a redirect (e.g. from
 * GitHub), then drop those parameters from the address bar.
 */
export function useFlash(messages: Record<string, (value: string) => string>, onAny?: () => void) {
  const [params, setParams] = useSearchParams();
  useEffect(() => {
    const error = params.get("error");
    const found = Object.keys(messages).filter((k) => params.has(k));
    if (!error && found.length === 0) return;
    if (error) toast.error(error);
    found.forEach((k) => toast.success(messages[k](params.get(k) ?? "")));
    onAny?.();
    const next = new URLSearchParams(params);
    ["error", ...found].forEach((k) => next.delete(k));
    setParams(next, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [params]);
}

import { useEffect, useState } from "react";

function matches(query: string): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia(query).matches;
}

/** Live media query. */
export function useMedia(query: string): boolean {
  const [on, setOn] = useState(() => matches(query));
  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const mq = window.matchMedia(query);
    const update = () => setOn(mq.matches);
    update();
    mq.addEventListener("change", update);
    return () => mq.removeEventListener("change", update);
  }, [query]);
  return on;
}

/** Phone layout (below Tailwind's md). */
export const useIsMobile = () => useMedia("(max-width: 767px)");

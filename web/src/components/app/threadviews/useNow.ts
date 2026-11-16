import { onBeforeUnmount, ref, toValue, watch, type MaybeRefOrGetter, type Ref } from "vue";

/** The wall clock, ticking every second while `on` (elapsed clocks). */
export function useNow(on: MaybeRefOrGetter<boolean>): Ref<number> {
  const now = ref(Date.now());
  let timer: ReturnType<typeof setInterval> | null = null;
  const stop = () => {
    if (timer) clearInterval(timer);
    timer = null;
  };
  watch(
    () => toValue(on),
    (v) => {
      stop();
      if (v) timer = setInterval(() => (now.value = Date.now()), 1000);
    },
    { immediate: true },
  );
  onBeforeUnmount(stop);
  return now;
}

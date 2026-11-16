import { inject, provide, type InjectionKey } from "vue";

/** Pages open the phone drawer through `useShell().openNav` (the ☰ button of their mobile header). */
export interface ShellApi {
  openNav: () => void;
}

const KEY: InjectionKey<ShellApi> = Symbol("shell");

export const provideShell = (api: ShellApi) => provide(KEY, api);
export const useShell = (): ShellApi => inject(KEY, { openNav: () => undefined });

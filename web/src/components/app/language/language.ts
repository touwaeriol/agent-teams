// Picking the interface language: "" follows the browser; a pick is saved on the account, then
// the page reloads in that language (settings page and the sidebar quick switch share this).
import { auth } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import { $t, LOCALE_NAMES, LOCALES, applyChoice } from "@/i18n";

export const languageOptions = () => [{ value: "", label: $t("跟随浏览器") }, ...LOCALES.map((l) => ({ value: l as string, label: LOCALE_NAMES[l] }))];

export const useSaveLanguage = () =>
  useAction((locale: string) => auth.setLocale(locale), {
    invalidate: [keys.me],
    onSuccess: (user) => applyChoice(user.locale ?? ""),
  });

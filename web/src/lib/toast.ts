// Toasts: Element Plus messages with the sonner API the React app used (toast(), .success,
// .error, .info). Top-centre, closable, themed in styles/element.css.
import { ElMessage, type MessageOptions } from "element-plus/es/components/message/index.mjs";

type Kind = "success" | "error" | "info" | "warning";

const DURATION = 4000;

function show(type: Kind | undefined, message: string): void {
  const options: MessageOptions = { message, type: type ?? "info", showClose: true, duration: DURATION, grouping: true, plain: true };
  ElMessage(options);
}

export const toast = Object.assign((message: string) => show(undefined, message), {
  success: (message: string) => show("success", message),
  error: (message: string) => show("error", message),
  info: (message: string) => show("info", message),
  warning: (message: string) => show("warning", message),
});

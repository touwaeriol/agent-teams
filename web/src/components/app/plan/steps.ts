// How each plan step status looks in the checklist card (PlanChecklist.vue): the icon, its
// colour and the title's style. Theme tokens only, so light and dark both work.
import { $t } from "@/i18n";
import { CircleDashed, CircleEllipsis, CircleSlash, CircleX } from "lucide-vue-next";
import { defineComponent, h, type Component } from "vue";
import { stepStatus, type StepStatus } from "@/lib/threadList";

/**
 * The filled grey circle with a tick (lucide only has the outline one). With an aria-label it is
 * an image screen readers announce; without one it is decoration and hidden from them.
 */
export const CircleDone = defineComponent({
  name: "CircleDone",
  props: { size: { type: [Number, String], default: 16 } },
  setup: (props, { attrs }) => () =>
    h("svg", { width: props.size, height: props.size, viewBox: "0 0 16 16", ...(attrs["aria-label"] ? { role: "img" } : { "aria-hidden": "true" }) }, [
      h("circle", { cx: 8, cy: 8, r: 7, fill: "currentColor" }),
      h("path", { d: "M4.9 8.2l2.1 2.1 4.2-4.4", fill: "none", stroke: "hsl(var(--panel))", "stroke-width": 1.7, "stroke-linecap": "round", "stroke-linejoin": "round" }),
    ]),
});

export interface StepLook {
  status: StepStatus;
  label: string;
  icon: Component;
  /** Icon colour. */
  iconClass: string;
  /** Title style. */
  textClass: string;
}

const LOOKS: Record<StepStatus, StepLook> = {
  done: { status: "done", label: $t("已完成"), icon: CircleDone, iconClass: "text-muted-foreground", textClass: "text-muted-foreground line-through" },
  doing: { status: "doing", label: $t("进行中"), icon: CircleEllipsis, iconClass: "text-foreground", textClass: "text-foreground" },
  todo: { status: "todo", label: $t("未开始"), icon: CircleDashed, iconClass: "text-muted-foreground", textClass: "text-foreground" },
  failed: { status: "failed", label: $t("失败"), icon: CircleX, iconClass: "text-bad", textClass: "text-foreground" },
  skipped: { status: "skipped", label: $t("已跳过"), icon: CircleSlash, iconClass: "text-muted-foreground", textClass: "text-muted-foreground" },
};

/** The look of a status; unknown ones render as todo. */
export const stepLook = (status: string): StepLook => LOOKS[stepStatus(status)];

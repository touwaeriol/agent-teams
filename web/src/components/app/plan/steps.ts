// How each plan step status looks in the checklist card (PlanChecklist.vue): the icon, its
// colour and the title's style. Theme tokens only, so light and dark both work.
import { CircleDashed, CircleEllipsis, CircleSlash, CircleX } from "lucide-vue-next";
import { defineComponent, h, type Component } from "vue";

/** The filled grey circle with a tick (lucide only has the outline one). */
export const CircleDone = defineComponent({
  name: "CircleDone",
  props: { size: { type: [Number, String], default: 16 } },
  setup: (props) => () =>
    h("svg", { width: props.size, height: props.size, viewBox: "0 0 16 16", "aria-hidden": "true" }, [
      h("circle", { cx: 8, cy: 8, r: 7, fill: "currentColor" }),
      h("path", { d: "M4.9 8.2l2.1 2.1 4.2-4.4", fill: "none", stroke: "hsl(var(--panel))", "stroke-width": 1.7, "stroke-linecap": "round", "stroke-linejoin": "round" }),
    ]),
});

export type StepStatus = "todo" | "doing" | "done" | "failed" | "skipped";

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
  done: { status: "done", label: "已完成", icon: CircleDone, iconClass: "text-muted-foreground", textClass: "text-muted-foreground line-through" },
  doing: { status: "doing", label: "进行中", icon: CircleEllipsis, iconClass: "text-foreground", textClass: "text-foreground" },
  todo: { status: "todo", label: "未开始", icon: CircleDashed, iconClass: "text-muted-foreground", textClass: "text-foreground" },
  failed: { status: "failed", label: "失败", icon: CircleX, iconClass: "text-bad", textClass: "text-foreground" },
  skipped: { status: "skipped", label: "已跳过", icon: CircleSlash, iconClass: "text-muted-foreground", textClass: "text-muted-foreground" },
};

/** The look of a status; unknown ones render as todo. */
export const stepLook = (status: string): StepLook => LOOKS[status as StepStatus] ?? LOOKS.todo;

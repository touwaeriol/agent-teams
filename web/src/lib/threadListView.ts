// The panel list's search and filters, per project, kept while a thread is open so the thread
// header's 上一个 / 下一个讨论串 walk the list the user was looking at.
import { reactive } from "vue";
import type { ListFilter, ListGroupId } from "./threadList";

export interface ListViewState {
  searching: boolean;
  q: string;
  groups: ListGroupId[];
  withDeliverable: boolean;
}

const states = new Map<string, ListViewState>();

export function listView(pid: string): ListViewState {
  let s = states.get(pid);
  if (!s) {
    s = reactive<ListViewState>({ searching: false, q: "", groups: [], withDeliverable: false });
    states.set(pid, s);
  }
  return s;
}

export const listFilterOf = (s: ListViewState): ListFilter => ({ q: s.q, groups: s.groups, withDeliverable: s.withDeliverable });

/** Forgets every project's search and filters (tests; signing out). */
export const resetListViews = () => states.clear();

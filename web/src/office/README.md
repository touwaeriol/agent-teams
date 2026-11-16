# Office renderer

Pixel-art project office driven by a layout JSON (`docs/v2/office-layout.md`).
Import from `@/office`.

```vue
<script setup lang="ts">
import { Office, useRemoteLayout } from "@/office";
const remote = useRemoteLayout(() => project.office_layout); // GET /api/office/layouts/{id}; call remote.reload() on SSE `layout`
</script>

<template>
  <Office
    :layout="remote.layout.value"
    :employees="snapshot.employees"
    :threads="snapshot.threads"
    :events="statusEvents"
    :data="{ board, nodes, mergeQueue, ci }"
    :focus="focusNo"
    :task-titles="{ t8: '计划与子任务展示' }"
    @focus-thread="(no) => (focusNo = no)"
    @select="openEmployeeCard"
    @action="openPanel"
  />
</template>
```

Vue port of `web/src/office`: every module except the components is the same TypeScript. `Office.tsx`
and `overlay.tsx` are Vue JSX (render functions; inline styles go through `sx()` because Vue does not add
`px` to numbers), `ReplayBar.vue` is an SFC, and `useHotLayout` / `useRemoteLayout` / `useReplay` are
composables returning refs. The callbacks (`onSelect`, `onFocusThread`, `onAction`) are props, so
`@select` etc. bind them and the overlay only draws click targets when someone listens.

| Prop | Meaning |
| --- | --- |
| `layout` | Validated layout. A new object recompiles in place; people keep state and position. |
| `employees` | Roster. Initial status from `status`; a changed `status` also applies. |
| `threads` | Threads; `layout.threadPods.layout.show` decides which get a work area. |
| `events` | Status events. Appended events walk people; any other change rebuilds instantly. |
| `focus`, `onFocusThread` | Focus mode: other threads dim to `focus.dimOpacity`. |
| `selected`, `onSelect` | Click a person to select (controlled or not). |
| `data` | Task board count, nodes (racks), merge queue, CI checks. |
| `onAction` | Widget and plate clicks. |
| `speed`, `now`, `seekKey` | Replay: walk speed, replay clock, teleport on scrub. |

Replay: `const r = useReplay(events)` then `<Office :events="r.events.value" :now="r.time.value" :speed="r.speed.value" :seek-key="r.seekKey.value" …/>`
and `<ReplayBar :replay="r" />`.

Hot reload: `useHotLayout(doc)` validates any new layout object (schema via ajv + semantic checks) and keeps the
last good one when it is invalid.

Files: `load.ts` (fetch/ETag, extends, ajv), `validate.ts` (semantic checks), `compile.ts` (scene), `nav.ts` (A*),
`director.ts` (rules, anchors, walking, sequences), `looks.ts` (looks + sprite cache), `render.ts` (canvas),
`overlay.tsx` (DOM text and click targets), `sx.ts` (px for numeric styles), `replay.ts`, `Office.tsx`, `ReplayBar.vue`.
`office-layout.schema.json` is a copy of `docs/v2/office-layout.schema.json` (a test keeps them equal).

Dev demo: `npm run dev`, open `/dev/office` (no backend needed). Edit `/office/default.json` and it hot-reloads.
Tests: `npm run test:office`.

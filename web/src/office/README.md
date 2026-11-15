# Office renderer

Pixel-art project office driven by a layout JSON (`docs/v2/office-layout.md`).
Import from `@/office`.

```tsx
const { layout, error, reload } = useRemoteLayout(project.office_layout); // GET /api/office/layouts/{id}; call reload() on SSE `layout`
<Office
  layout={layout}
  employees={snapshot.employees}        // EmployeeBrief + thread_no (+ status)
  threads={snapshot.threads}            // ThreadView
  events={statusEvents}                 // SSE `status` frames as {seq, at, employee, status}, in seq order
  data={{ board, nodes, mergeQueue, ci }}
  focus={focusNo}                       // null = all threads
  onFocusThread={setFocusNo}            // plate click
  onSelect={(id) => openEmployeeCard(id)}
  onAction={(a) => openPanel(a)}        // openTaskPool | openNode | openMergeQueue | openThread
  taskTitles={{ t8: "计划与子任务展示" }}
/>
```

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

Replay: `const r = useReplay(events)` then `<Office events={r.events} now={r.time} speed={r.speed} seekKey={r.seekKey} …/>`
and `<ReplayBar replay={r}/>`.

Hot reload: `useHotLayout(doc)` validates any new layout object (schema via ajv + semantic checks) and keeps the
last good one when it is invalid.

Files: `load.ts` (fetch/ETag, extends, ajv), `validate.ts` (semantic checks), `compile.ts` (scene), `nav.ts` (A*),
`director.ts` (rules, anchors, walking, sequences), `looks.ts` (looks + sprite cache), `render.ts` (canvas),
`overlay.tsx` (DOM text and click targets), `replay.ts`, `Office.tsx`.
`office-layout.schema.json` is a copy of `docs/v2/office-layout.schema.json` (a test keeps them equal).

Dev demo: `npm run dev`, open `/dev/office` (no backend needed). Edit `/office/default.json` and it hot-reloads.
Tests: `npm run test:office`.

# agent-teams web

The browser app: **Vue 3 + Vite 7 + TypeScript + Element Plus + Pinia + vue-router 4 +
@tanstack/vue-query 5**, Tailwind 3 for layout. It replaced the React app (same screens, same behaviour,
same design tokens; the React source is in git history before the swap) against the same API contract
(`src/api/openapi.json`, cookies, CSRF, SSE).

```
npm ci
npm run dev:mock      # mock API (mock/server.mjs, port 3001) + vite; log in with any email / 8+ char password
API_TARGET=http://127.0.0.1:3001 npm run dev   # vite against a real atctl
npm test              # vitest (node by default; component tests opt into jsdom)
npm run typecheck     # vue-tsc -b
npm run lint          # eslint (flat config: js + typescript-eslint + eslint-plugin-vue)
npm run build         # vue-tsc -b && vite build → dist/
npm run gen:api       # src/api/openapi.json → src/api/schema.d.ts (after `atctl openapi > src/api/openapi.json`)
```

`/dev/office` under `npm run dev` is the office renderer demo (no backend).

## Build and the server image

`npm run build` writes `dist/` (index.html + `assets/`); the Go server serves it from `WEB_DIR`.
`server/Dockerfile`'s `web` stage runs `npx vue-tsc -b && npx vite build --outDir /out/web` and the
runtime image serves `/srv/web`. The `/dev/office` demo imports `office/default.json`, which the web stage copies in.

## Layout

| Path | What |
| --- | --- |
| `src/main.ts` · `App.vue` · `router.ts` | app setup (vue-query, Pinia, router), the `me` gate (login / forced password change), every route |
| `src/api/client.ts` | the one openapi-fetch client: same-origin cookie, `X-CSRF-Token` from the `agent_teams_csrf` cookie on writes, `{detail, code, fields}` → `ApiError` |
| `src/api/schema.d.ts` · `openapi.json` | generated from the core's OpenAPI document |
| `src/api/orchestration.ts` · `views.ts` · `actions.ts` | orchestration paths, view types, every write |
| `src/api/queries.ts` | query keys + composables; arguments are `MaybeRefOrGetter` |
| `src/api/mutations.ts` | `useAction`: toast on error, invalidate, optional success toast |
| `src/api/events.ts` · `sse.ts` | SSE frames written into the vue-query cache; `openEventStream` (resume via `Last-Event-ID` on browser reconnects, `?last_event_id=` when we reopen, backoff) and the `useProjectEvents` / `useUserEvents` composables |
| `src/styles/` | `base.css` (tokens, light + `.dark`), `element.css` (Element Plus themed with the tokens), `index.css` (Tailwind utilities, loaded last) |
| `src/components/common` | platform building blocks (`Page`, `Panel`, `Field`, `FormDialog`, `ConfirmAction`, `DataTable`, `IconButton`, `IconMenu`, `DropdownMenu`…; see `index.ts`) |
| `src/components/app` | v2 building blocks: marks and pills, `Composer` (+ image attachments, drafts), `Chat`, waiting cards (`cards/`), chat cards (`chatcards/`), `ThreadList` / `ThreadMenu`, `SidePanel` (resizable right panel) |
| `src/components/shell` | `ProjectShell` / `SettingsShell` (sidebar on desktop, drawer on phones, live streams), `ProjectSidebar`, `ProjectSwitcher`, `SettingsNav`, `NavButton` |
| `src/components/Markdown.vue` · `lib/markdown.ts` | Markdown with react-markdown's exact pipeline (remark-gfm → hast → vnodes), the same CJK URL fencing and safe URL transform |
| `src/lib` | pure helpers (`present`, `live`, `chat`, `ask`, `library`, `roles`…) plus the Vue versions of `persist` (`useDraft`), `theme`, `media`, `flash`, and `toast` |
| `src/pages` | every page: `Main` (+ `main/`), `Overview`, `ThreadRoute` / `ThreadPage`, `Deliverables`, `DesignViewer`, `Office`, `NewProject`, `projectSettings/`, `settings/*`, `Login`, `ChangePassword` |
| `src/office` | the office renderer (see its README) |
| `mock/` | the dev mock API; not part of the build (also used by `deploy/e2e` screenshots) |

## Conventions

- **SFCs with `<script setup lang="ts">`**, one component per file, small flat functions. Render-function
  TSX only where it pays (the office renderer).
- **Server state is vue-query, always through `api/queries.ts` keys.** Never invent a new key shape for
  data the SSE layer updates; add the key to `keys` and the handler to `events.ts`. Pinia is for
  client-only state shared across pages (none needed by the shell yet; put stores in `src/stores/`).
  Per-viewer conveniences (drafts, panel width, theme) stay in guarded `localStorage` (`lib/persist.ts`).
- **Writes go through `useAction(fn, { invalidate, success, onSuccess })`** with the functions in
  `api/actions.ts`. When the keys depend on props, pass `invalidate: () => [...]`. Read state as
  `.value` (`action.isPending.value`).
- **Composables take `MaybeRefOrGetter`**: `useThreads(() => props.pid)`, `useMessages(cid)`.
- **ReactNode props become slots.** `title` / `description` are string props with a same-named slot for
  rich content; actions go in `#action` / `#actions`; card inserts in `Chat` use the `insert` slot.
- **Callback props become emits** (`@open`, `@confirm`, `@command`), except where the component must know
  whether anyone listens (office: `onAction` etc. are props; `Composer.onSend` is a prop because it is awaited).
- **Styling:** Tailwind classes copied from the React markup work unchanged (same `tailwind.config.ts`,
  same tokens). Components that accept a `class` merge it with `cn()` (tailwind-merge), so a caller's
  `leading-7` replaces the default `leading-6` exactly like in React. Colours are tokens
  (`bg-panel`, `text-muted-foreground`…) or the design's literal hex values.
- **Element Plus is the primitive layer** (themed in `styles/element.css`; tags are auto-imported per
  component by the resolver in `vite.config.ts`, see `src/components.d.ts`):

  | React (before the swap) | Vue |
  | --- | --- |
  | `Button` default / outline / destructive / ghost | `<el-button type="primary">` / `<el-button>` / `type="danger"` / `text` |
  | `Input`, `Textarea` | `<el-input>`, `<el-input type="textarea">` (bind `:id` from `Field`'s slot) |
  | `Select`, `Combobox`, `TagInput` | `<el-select>` (filterable / allow-create), `<el-input-tag>` |
  | `Switch`, `Tabs` | `<el-switch>`, `<el-tabs class="el-tabs--segmented">` |
  | `Dialog` / `AlertDialog` | `FormDialog` / `InfoDialog` / `ConfirmAction` (`v-model:open`) |
  | `DropdownMenu` | `DropdownMenu` (trigger in the default slot, `<el-dropdown-item command>` in `#menu`), or `IconMenu` for a 32px icon trigger with a tooltip |
  | `Tooltip` | `IconButton` (label = tooltip + accessible name) or `<el-tooltip effect="dark">` |
  | `toast` (sonner) | `toast` from `lib/toast.ts` (same API, Element Plus messages) |
  | `lucide-react` | `lucide-vue-next` (same icon names; `strokeWidth` → `:stroke-width`) |

- **Routing:** paths are those of docs/v2/api.md §4 (unchanged from the React app). Pages read params with
  `useRoute()`; the sidebar drawer closes on navigation.
- **Tests:** `src/**/*.test.ts`. Pure logic runs in node; component tests start with
  `// @vitest-environment jsdom` and mount with `@vue/test-utils` (see `__tests__/components.test.ts` for
  the vue-query + router setup and how API calls are asserted through a stubbed `fetch`). Add a test for
  every behaviour change.

## Porting status

Complete: every screen of the React app is ported (checked side by side against the mock in light and
dark, desktop and phone). The React
tests are ported too (`__tests__/cards.test.ts` holds the former chat card and mark tests).
